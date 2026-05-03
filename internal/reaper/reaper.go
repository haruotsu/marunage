// Package reaper implements PR-44's orphan / 24h-stuck recovery sweep
// over the tasks table. One Reaper.Run pass:
//
//  1. Lists every status=running row whose ws column is non-empty.
//  2. Diffs that set against cmux.ListWorkspaces. Rows whose workspace
//     vanished flip to failed via store.MarkFailedWithReason and emit
//     audit "reaper.failed" — invariant #5 "Crash safety".
//  3. For rows whose workspace is still alive, checks started_at +
//     stuckThreshold; rows past that age get one "stuck running over
//     <threshold>" warn appended to judgment_reason and audit
//     "reaper.warn". Status is intentionally NOT auto-transitioned —
//     human judgement decides whether to promote / fail / reopen.
//
// Daemonisation (PR-71) is out of scope: the CLI invokes Reaper.Run
// once per `marunage reaper` (and later, once per loop tick).
package reaper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/store"
)

// Store is the narrow read/write surface Reaper needs against the tasks
// table. Keeping it as an interface (rather than the concrete
// *store.TaskRepo) is the test seam: production wires the real repo,
// tests can swap in a fake. The method set is intentionally a subset of
// *store.TaskRepo so the concrete type satisfies it implicitly.
type Store interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
	// MarkFailedFromRunningWithReason is the status-guarded variant of
	// MarkFailedWithReason: it transitions only when the row is still
	// running, returning ErrInvalidTransition otherwise. PR-44 uses it
	// so reaper cannot overwrite a row that PR-43 atomic sentinel or a
	// human writer has moved past running between List and the per-row
	// write.
	MarkFailedFromRunningWithReason(ctx context.Context, id int64, reason string) error
	// AppendJudgmentReason appends suffix to the row's judgment_reason
	// (with "; " separator when an existing note is present). The
	// dedicated helper exists so the reaper's "stuck warn" path does
	// not have to do its own Get + Update non-atomic dance — see
	// store.TaskRepo.AppendJudgmentReason for the atomicity contract.
	AppendJudgmentReason(ctx context.Context, id int64, suffix string) error
}

// Cmux is the narrow read surface Reaper needs against the cmux client.
// Reaper only ever asks "what workspaces are alive right now?", so this
// interface is intentionally smaller than the full cmux.Client surface
// — production wires a real cmux.Client (which satisfies it), tests
// inject a fake that returns scripted live sets.
type Cmux interface {
	ListWorkspaces(ctx context.Context) ([]cmux.Workspace, error)
}

// Reaper is the long-running sweep coordinator. One instance per
// process; safe for concurrent Run calls in principle, though the CLI
// only fires one at a time today.
type Reaper struct {
	store          Store
	cmux           Cmux
	now            func() time.Time
	stuckThreshold time.Duration
	auditor        config.Auditor
}

// Option mutates Reaper construction. The functional-option shape
// matches dispatch.Option so wiring stays uniform across packages.
type Option func(*Reaper)

// WithStore injects the tasks-table repository. Required.
func WithStore(s Store) Option { return func(r *Reaper) { r.store = s } }

// WithCmux injects the cmux client (or any narrower implementation of
// the Cmux interface). Required.
func WithCmux(c Cmux) Option { return func(r *Reaper) { r.cmux = c } }

// WithClock injects a deterministic clock for the stuck-threshold
// comparison. Defaults to time.Now in production.
func WithClock(now func() time.Time) Option { return func(r *Reaper) { r.now = now } }

// WithStuckThreshold overrides the 24h default after which a still-
// running row earns a "stuck running" warn. Tests typically pass a few
// minutes so timing assertions stay snappy.
//
// A zero or negative value falls back to the 24h default rather than
// disabling the warn — disabling stuck detection is a config-layer
// concern (see execution.reaper_stuck_threshold) and a misconfigured
// Option should not silently turn off the safety net.
func WithStuckThreshold(d time.Duration) Option {
	return func(r *Reaper) { r.stuckThreshold = d }
}

// WithAuditor installs the audit-log sink. Every reaper.failed (cmux
// disappear) and reaper.warn (24h stuck) records one event so
// requirement.md L29 invariant #2 "No silent execution" is honoured for
// reaper too. Defaults to config.NopAuditor so the constructor stays
// usable from one-off CLI tooling.
func WithAuditor(a config.Auditor) Option {
	return func(r *Reaper) { r.auditor = a }
}

// defaultStuckThreshold is docs/requirement.md PR-44's "started_at +
// 24h 超の running を警告". The constant is the package-level fallback;
// production wires execution.reaper_stuck_threshold from config.toml.
const defaultStuckThreshold = 24 * time.Hour

// ErrInvalidConfig signals a missing required Option at construction.
var ErrInvalidConfig = errors.New("reaper: missing required option")

// New builds a Reaper. Required: WithStore, WithCmux. Returns
// ErrInvalidConfig naming the missing field so a buggy CLI wiring
// fails loud at startup.
func New(opts ...Option) (*Reaper, error) {
	r := &Reaper{
		now:            time.Now,
		stuckThreshold: defaultStuckThreshold,
		auditor:        config.NopAuditor{},
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.store == nil {
		return nil, fmt.Errorf("%w: WithStore", ErrInvalidConfig)
	}
	if r.cmux == nil {
		return nil, fmt.Errorf("%w: WithCmux", ErrInvalidConfig)
	}
	if r.stuckThreshold <= 0 {
		r.stuckThreshold = defaultStuckThreshold
	}
	return r, nil
}

// reaperFailedReason is the judgment_reason marker stamped on rows
// whose cmux workspace vanished. Centralised so tests, audit consumers,
// and `marunage review` can grep the same literal.
const reaperFailedReason = "workspace disappeared (reaper)"

// stuckWarnPrefix is the literal prefix every stuck warn token starts
// with. The full token appended to judgment_reason is
// "[reaper] stuck running over <threshold>" — the threshold is
// stringified via time.Duration.String so 24h0m0s collapses to "24h"
// and a 30-minute test threshold renders as "30m0s".
const stuckWarnPrefix = "[reaper] stuck running over "

// Run executes one reaper sweep. Returns a non-nil error only for
// catastrophic failures (Store.List or Cmux.ListWorkspaces blew up);
// per-row failures are written into the row's judgment_reason and Run
// returns nil so a single broken row never wedges the whole sweep
// (mirrors dispatch.Run's "no巻き込み故障" stance).
func (r *Reaper) Run(ctx context.Context) error {
	rows, err := r.store.List(ctx, store.ListFilter{
		Statuses: []string{store.StatusRunning},
	})
	if err != nil {
		return fmt.Errorf("reaper: list running: %w", err)
	}
	live, err := r.cmux.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("reaper: list workspaces: %w", err)
	}
	alive := make(map[string]struct{}, len(live))
	for _, w := range live {
		alive[w.ID] = struct{}{}
	}
	now := r.now()
	warnToken := stuckWarnPrefix + r.stuckThreshold.String()

	for _, row := range rows {
		// Empty ws means the dispatcher has already flipped status to
		// running but lost the SetWorkspace race (or has not got that
		// far yet). The reaper's contract is "ws disappeared", not
		// "ws never set" — skip so the dispatcher's own retry path is
		// not pre-empted.
		if row.WS == "" {
			continue
		}
		if _, ok := alive[row.WS]; !ok {
			r.markDisappeared(ctx, row)
			continue
		}
		// Live ws — check the 24h stuck threshold. Zero started_at is
		// defensively skipped: dispatcher invariant guarantees it is
		// stamped, but a buggy migration / hand-crafted row should not
		// crash the sweep.
		if row.StartedAt.IsZero() {
			continue
		}
		if now.Sub(row.StartedAt) <= r.stuckThreshold {
			continue
		}
		// Idempotency: a previous Run already appended the warn token,
		// so skip the second write to keep audit.log and judgment_reason
		// from ballooning under loop / cron invocations.
		//
		// Match on the joined-segment level (split by judgmentReason
		// separator "; ") rather than substring so an operator note
		// that happens to embed the warn literal as prose
		// ("investigated [reaper] stuck running over 24h false alarm")
		// does NOT silently disable future genuine warns for the row.
		if hasReaperWarnToken(row.JudgmentReason, warnToken) {
			continue
		}
		r.markStuck(ctx, row, warnToken)
	}
	return nil
}

// markDisappeared transitions one row to failed and records audit. The
// audit record's Value carries the (now-vanished) ws id so a forensic
// reader can correlate against `cmux dashboard` history.
//
// Uses MarkFailedFromRunningWithReason so a row that another writer
// (PR-43 atomic sentinel, manual `marunage done`) moved past running
// between List and this call surfaces as ErrInvalidTransition rather
// than being silently overwritten. Both error branches are logged via
// slog so a long-running daemon does not lose the failure trail —
// requirement.md invariant #2 "No silent execution".
func (r *Reaper) markDisappeared(ctx context.Context, row store.Task) {
	err := r.store.MarkFailedFromRunningWithReason(ctx, row.ID, reaperFailedReason)
	if err == nil {
		r.auditor.Record(config.AuditEvent{
			Action: "reaper.failed",
			Key:    "task:" + strconv.FormatInt(row.ID, 10),
			Value:  row.WS,
		})
		return
	}
	if errors.Is(err, store.ErrInvalidTransition) {
		// Row moved out of running mid-sweep — expected race window,
		// not an error worth panicking on. Debug-level so daemon
		// monitoring can still see it under verbose logging.
		slog.Debug("reaper: skip disappeared write, row no longer running",
			"task_id", row.ID, "ws", row.WS, "err", err)
		return
	}
	slog.Warn("reaper: failed to mark disappeared row failed",
		"task_id", row.ID, "ws", row.WS, "err", err)
}

// hasReaperWarnToken returns true iff one of the "; "-joined segments
// of judgmentReason equals warnToken exactly. This is the strict form
// of strings.Contains used by the idempotency check — segment-level
// match avoids the "operator quoted the warn literal in prose"
// false-positive that would otherwise mute the next genuine warn.
func hasReaperWarnToken(judgmentReason, warnToken string) bool {
	for _, seg := range strings.Split(judgmentReason, "; ") {
		if seg == warnToken {
			return true
		}
	}
	return false
}

// markStuck appends the warn token to judgment_reason (preserving any
// prior content) and records audit. Status stays running — the spec
// reserves the failed transition for human judgement on stuck rows.
// Per-row write failures are slog'd (not silently swallowed) so a
// daemon does not drop the warn trail without trace.
func (r *Reaper) markStuck(ctx context.Context, row store.Task, warnToken string) {
	if err := r.store.AppendJudgmentReason(ctx, row.ID, warnToken); err != nil {
		slog.Warn("reaper: failed to append stuck-warn note",
			"task_id", row.ID, "ws", row.WS, "err", err)
		return
	}
	r.auditor.Record(config.AuditEvent{
		Action: "reaper.warn",
		Key:    "task:" + strconv.FormatInt(row.ID, 10),
		Value:  warnToken,
	})
}
