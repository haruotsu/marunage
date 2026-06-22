// Package loop is the PR-71 orchestrator that connects the Discovery,
// Dispatch, and Render layers into one tick:
//
//  1. discover: walk every plugin in the source.Registry, call Sincer.Since
//     when supported (else Plugin.List), and upsert results into the tasks
//     table. A plugin's failure does not abort the rest of the iteration —
//     other sources still run and the dispatch / render phases still fire.
//  2. dispatch: invoke dispatch.Dispatcher.Run with the configured
//     MaxParallel. The dispatcher honours the existing lock_key /
//     ClaimWorkspace concurrency contract.
//  3. render: invoke the Render hook so ~/.marunage/view.md reflects the
//     post-dispatch state.
//
// The package owns no I/O of its own beyond the discover-side store
// upserts: dispatch and render are passed in as narrow interfaces so the
// CLI wires the production implementations and tests can drop in fakes.
//
// Concurrency: a Loop is safe for concurrent RunOnce / Run calls only
// when WithLockKey is set; the kv_state-backed lock is the
// requirement.md "lock_key を尊重した並列ループ" guarantee. Without
// WithLockKey, callers are responsible for serialising their own ticks.
package loop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/manage"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
)

// ErrInvalidConfig signals a missing required Option at construction.
var ErrInvalidConfig = errors.New("loop: missing required option")

// ErrInvalidInterval is returned by Run when the supplied interval is
// non-positive — a zero-or-negative tick would either spin the loop hot
// or never fire, both of which are configuration bugs the daemon should
// flag rather than silently absorb.
var ErrInvalidInterval = errors.New("loop: interval must be > 0")

// ErrLockBusy is returned by RunOnce when WithLockKey is configured and
// another writer (another loop process, a stuck previous tick) currently
// holds the lock. The CLI translates this into "skip this tick" rather
// than failing the daemon.
var ErrLockBusy = errors.New("loop: lock busy")

// TaskRepo is the narrow write/read surface the loop needs against the
// tasks table. The full *store.TaskRepo satisfies it implicitly so the
// CLI can hand the concrete type in.
type TaskRepo interface {
	Insert(ctx context.Context, t store.Task) (int64, error)
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
}

// KVStateRepo is the narrow surface the loop needs against the kv_state
// table. *store.KVStateRepo satisfies it.
//
// InsertIfAbsent + DeleteIfValue are the atomic primitives the loop's
// lock acquire / release path uses. Together with an owner token they
// give mutual exclusion that survives a panic + a crash + a concurrent
// re-acquire by another process — see the matching primitives'
// godoc on *store.KVStateRepo for the SQL contract.
type KVStateRepo interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	InsertIfAbsent(ctx context.Context, key, value string) (bool, error)
	DeleteIfValue(ctx context.Context, key, expected string) (bool, error)
}

// Dispatcher is the dispatch surface the loop needs. *dispatch.Dispatcher
// satisfies it.
type Dispatcher interface {
	Run(ctx context.Context, opts dispatch.RunOptions) error
}

// ManageStore is the read/write surface the management pipeline needs against
// the tasks table. It is a superset of manage.Store (List + Get) plus the
// writes the persist step issues, so the production *store.TaskRepo satisfies
// it implicitly and can be passed straight to manage.Plan. Injected via
// WithManageStore; when absent, RunOnce keeps the legacy discover-and-insert
// path so existing callers see no behaviour change (redesign §8 strangler fig).
type ManageStore interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
	Get(ctx context.Context, id int64) (store.Task, error)
	Insert(ctx context.Context, t store.Task) (int64, error)
	GetBySourceExternalID(ctx context.Context, source, externalID string) (store.Task, error)
	SetPlan(ctx context.Context, id int64, label, reason string, score float64, rank int, plannedAt time.Time) error
	UpdateStatus(ctx context.Context, id int64, newStatus string) error
}

var _ ManageStore = (*store.TaskRepo)(nil)

// Render is the render surface the loop needs. The CLI wires a closure
// that calls internal/render.Render and writes ~/.marunage/view.md
// atomically; tests pass a fake to assert "render ran exactly once".
type Render interface {
	Render(ctx context.Context) error
}

// Reaper is the orphan-recovery surface the loop needs. *reaper.Reaper
// satisfies it. Optional: when nil, the reaper phase is skipped.
// Matches requirement.md "discover → dispatch → render → notify → reaper".
type Reaper interface {
	Run(ctx context.Context) error
}

// Loop is the orchestrator. One instance per process; concurrency
// guarantees rest on WithLockKey.
type Loop struct {
	registry         *source.Registry
	repo             TaskRepo
	kv               KVStateRepo
	dispatcher       Dispatcher
	render           Render
	reaper           Reaper
	auditor          config.Auditor
	now              func() time.Time
	maxParallel      int
	lockKey          string
	dispatchInterval time.Duration
	// manageStore, when set, switches the discover phase to the
	// collect→manage→persist pipeline (redesign §2): Gather normalises every
	// source, manage.Plan classifies each candidate, and persist writes the
	// verdict (plan_*) + verdict-mapped status. nil keeps the legacy
	// discover-and-insert path.
	manageStore ManageStore
	manageOpts  []manage.Option
	// ownerToken is the per-Loop sentinel used as the kv_state lock
	// row's value so DeleteIfValue can detect "this defer's lock is no
	// longer mine" and skip the release. Generated once at New time so
	// the same Loop instance always presents the same owner identity.
	ownerToken string
}

// Option mutates Loop construction.
type Option func(*Loop)

// WithRegistry injects the plugin registry. Required.
func WithRegistry(r *source.Registry) Option { return func(l *Loop) { l.registry = r } }

// WithTaskRepo injects the tasks repository (Discovery upsert target).
// Required.
func WithTaskRepo(r TaskRepo) Option { return func(l *Loop) { l.repo = r } }

// WithKVStateRepo injects the kv_state repository for per-source
// checkpoints + the optional global lock. Optional: when nil, Sincer
// plugins receive an empty checkpoint string and WithLockKey is a no-op.
func WithKVStateRepo(r KVStateRepo) Option { return func(l *Loop) { l.kv = r } }

// WithDispatcher injects the dispatcher. Required.
func WithDispatcher(d Dispatcher) Option { return func(l *Loop) { l.dispatcher = d } }

// WithRender injects the render hook. Required.
func WithRender(r Render) Option { return func(l *Loop) { l.render = r } }

// WithAuditor installs the audit-log sink. Defaults to config.NopAuditor
// so tests / CLI paths that have not wired audit.log still build.
func WithAuditor(a config.Auditor) Option { return func(l *Loop) { l.auditor = a } }

// WithClock injects a deterministic clock. Defaults to time.Now in
// production. The clock is used for the kv_state checkpoint timestamp
// the loop writes after a successful per-plugin discover.
func WithClock(now func() time.Time) Option { return func(l *Loop) { l.now = now } }

// WithMaxParallel sets the dispatcher MaxParallel passed on each tick.
// Defaults to 1 so a misconfigured loop does not silently turn dispatch
// into a no-op.
func WithMaxParallel(n int) Option { return func(l *Loop) { l.maxParallel = n } }

// WithLockKey turns on the kv_state-backed exclusion lock. When set,
// RunOnce attempts to claim the key before starting; concurrent calls
// see ErrLockBusy. The lock is released in a deferred call so even a
// panic from one of the inner phases unblocks the next tick. Requires
// WithKVStateRepo to take effect.
func WithLockKey(key string) Option { return func(l *Loop) { l.lockKey = key } }

// WithReaper injects the orphan-recovery reaper. Optional: when nil,
// the reaper phase is skipped. The reaper runs after render so orphaned
// running rows are reclaimed before the next discover → dispatch cycle.
func WithReaper(r Reaper) Option { return func(l *Loop) { l.reaper = r } }

// WithManageStore turns on the collect→manage→persist pipeline (PR-R05),
// using s for both the manage.Plan read snapshot and the persist writes.
// When set, RunOnce replaces the legacy per-plugin discover-and-insert with
// Gather + Plan + persist so only rows the manager marks ready dispatch.
// Absent, the loop keeps the legacy path.
func WithManageStore(s ManageStore) Option { return func(l *Loop) { l.manageStore = s } }

// WithManageOptions forwards planner options (rules, verdict registry, cwd
// allowlist, lock keys, clock) to manage.Plan on each tick. Ignored unless
// WithManageStore is also set.
func WithManageOptions(opts ...manage.Option) Option {
	return func(l *Loop) { l.manageOpts = opts }
}

// WithDispatchInterval adds a second, shorter ticker that runs dispatch +
// render (but NOT discover) on every fire. When > 0, pending tasks added
// via the web UI are picked up without waiting for the full discovery
// cycle. A nil channel in the select blocks forever, so callers that omit
// this option see zero behaviour change.
func WithDispatchInterval(d time.Duration) Option {
	return func(l *Loop) { l.dispatchInterval = d }
}

// New builds a Loop. Required: WithRegistry, WithTaskRepo,
// WithDispatcher, WithRender. Returns ErrInvalidConfig naming the
// missing field so a buggy CLI wiring fails loud at startup.
func New(opts ...Option) (*Loop, error) {
	l := &Loop{
		now:         time.Now,
		auditor:     config.NopAuditor{},
		maxParallel: 1,
	}
	for _, opt := range opts {
		opt(l)
	}
	if l.registry == nil {
		return nil, fmt.Errorf("%w: WithRegistry", ErrInvalidConfig)
	}
	if l.repo == nil {
		return nil, fmt.Errorf("%w: WithTaskRepo", ErrInvalidConfig)
	}
	if l.dispatcher == nil {
		return nil, fmt.Errorf("%w: WithDispatcher", ErrInvalidConfig)
	}
	if l.render == nil {
		return nil, fmt.Errorf("%w: WithRender", ErrInvalidConfig)
	}
	tok, err := newOwnerToken()
	if err != nil {
		return nil, fmt.Errorf("loop: generate owner token: %w", err)
	}
	l.ownerToken = tok
	return l, nil
}

// newOwnerToken returns a 16-hex-char random sentinel used as the
// kv_state lock row's value. Long enough to be globally unique within
// any reasonable lifetime (8 random bytes), short enough that a stuck
// row inspected by an operator stays grep-friendly.
func newOwnerToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// checkpointKeyPrefix namespaces the per-plugin discover checkpoints
// inside the shared kv_state table. The prefix keeps loop-owned keys
// from colliding with plugin-internal ones (e.g. markdown's per-file
// mtime checkpoints).
const checkpointKeyPrefix = "loop.checkpoint."

// lockKeyPrefix namespaces loop locks under a single kv_state key
// space so an unrelated kv_state caller using the same key cannot
// collide.
const lockKeyPrefix = "loop.lock."

// RunOnce performs one full discover -> dispatch -> render iteration.
// Per-plugin Discovery failures are isolated: they record an audit entry
// and let the rest of the iteration continue. Dispatch / render errors
// are returned to the caller so Run can audit them and decide whether to
// keep ticking.
func (l *Loop) RunOnce(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.lockKey != "" && l.kv != nil {
		acquired, ackErr := l.acquireLock(ctx)
		if ackErr != nil {
			return ackErr
		}
		if !acquired {
			return ErrLockBusy
		}
		defer l.releaseLock(ctx)
	}
	l.auditor.Record(config.AuditEvent{Action: "loop.start"})
	defer func() {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.end",
			Value:  durationOrError(err),
		})
	}()

	if l.manageStore != nil {
		l.planAll(ctx)
	} else {
		l.discoverAll(ctx)
	}

	if err := l.dispatcher.Run(ctx, dispatch.RunOptions{MaxParallel: l.maxParallel}); err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.dispatch.fail",
			Value:  logging.Redact(err.Error()),
		})
		return fmt.Errorf("loop: dispatch: %w", err)
	}

	if err := l.render.Render(ctx); err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.render.fail",
			Value:  logging.Redact(err.Error()),
		})
		return fmt.Errorf("loop: render: %w", err)
	}

	if l.reaper != nil {
		// Reaper failure is non-fatal: orphan cleanup is best-effort and
		// must not stall the next discover → dispatch cycle.
		if err := l.reaper.Run(ctx); err != nil {
			slog.WarnContext(ctx, "loop: reaper failed", "err", err)
			l.auditor.Record(config.AuditEvent{
				Action: "loop.reaper.fail",
				Value:  logging.Redact(err.Error()),
			})
		}
	}

	return nil
}

// Run drives RunOnce immediately and then on every interval tick until
// ctx is cancelled. RunOnce errors are audited but do not stop the
// loop — the next tick still runs. This mirrors the dispatch contract:
// per-plugin failures do not poison the queue.
//
// When WithDispatchInterval is set, a second ticker fires at that shorter
// period and calls dispatchOnly (dispatch + render, no discover). A nil
// channel in the select blocks forever so callers without a dispatch
// interval see zero behaviour change.
func (l *Loop) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("%w: got %v", ErrInvalidInterval, interval)
	}
	if err := l.runTick(ctx); err != nil {
		return err
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	var dt <-chan time.Time
	if l.dispatchInterval > 0 {
		dispatchTicker := time.NewTicker(l.dispatchInterval)
		defer dispatchTicker.Stop()
		dt = dispatchTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := l.runTick(ctx); err != nil {
				return err
			}
		case <-dt:
			if err := l.runDispatchTick(ctx); err != nil {
				return err
			}
		}
	}
}

// dispatchOnly runs dispatcher.Run + render.Render + optional reaper.Run
// without calling discoverAll. Used by the dispatch-interval ticker so
// pending tasks added via the web UI are picked up on the shorter cycle.
func (l *Loop) dispatchOnly(ctx context.Context) error {
	if err := l.dispatcher.Run(ctx, dispatch.RunOptions{MaxParallel: l.maxParallel}); err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.dispatch.fail",
			Value:  logging.Redact(err.Error()),
		})
		return fmt.Errorf("loop: dispatch: %w", err)
	}
	if err := l.render.Render(ctx); err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.render.fail",
			Value:  logging.Redact(err.Error()),
		})
		return fmt.Errorf("loop: render: %w", err)
	}
	if l.reaper != nil {
		if err := l.reaper.Run(ctx); err != nil {
			slog.WarnContext(ctx, "loop: reaper failed", "err", err)
			l.auditor.Record(config.AuditEvent{
				Action: "loop.reaper.fail",
				Value:  logging.Redact(err.Error()),
			})
		}
	}
	return nil
}

// runDispatchTick calls dispatchOnly and translates ctx-cancel into a
// clean exit. Other errors are audited and swallowed — same pattern as
// runTick so the loop keeps ticking through transient dispatch failures.
func (l *Loop) runDispatchTick(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if err := l.dispatchOnly(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		l.auditor.Record(config.AuditEvent{
			Action: "loop.dispatch_tick.fail",
			Value:  logging.Redact(err.Error()),
		})
	}
	return nil
}

// runTick runs one RunOnce and translates ctx-cancel into a clean exit
// for the Run loop. Other errors are recorded and swallowed so the loop
// keeps ticking — only ctx-cancel breaks Run.
func (l *Loop) runTick(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if err := l.RunOnce(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		l.auditor.Record(config.AuditEvent{
			Action: "loop.tick.fail",
			Value:  logging.Redact(err.Error()),
		})
	}
	return nil
}

// planAll is the collect→manage→persist pipeline (redesign §2), used in place
// of discoverAll when WithManageStore is set. Gather normalises every source
// into candidates (running cheap early triage), manage.Plan classifies each
// one into a verdict + ordered ready列, and persist writes the verdict (plan_*)
// and the verdict-mapped status to tasks.db. Every decision is persisted —
// including drop / hold / needs-human — so nothing is silently lost
// (invariant #1); the dispatcher's plan_label='ready' filter then keeps the
// non-ready rows out of execution.
func (l *Loop) planAll(ctx context.Context) {
	candidates, err := collect.Gather(ctx, l.plugins(ctx), l.checkpointStore(), collect.WithClock(l.now))
	if err != nil {
		// Gather isolates per-source failures and returns the healthy
		// sources' candidates alongside the joined error, so we audit and
		// keep planning what we did collect (same "one bad plugin must not
		// blind the tick" contract discoverAll honours).
		l.auditor.Record(config.AuditEvent{
			Action: "loop.discover.fail",
			Value:  logging.Redact(err.Error()),
		})
	}

	plan, err := manage.Plan(ctx, candidates, l.manageStore, l.manageOpts...)
	if err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.manage.fail",
			Value:  logging.Redact(err.Error()),
		})
		return
	}

	plannedAt := l.now().UTC()
	for _, d := range plan.Decisions {
		if err := ctx.Err(); err != nil {
			return
		}
		l.persistDecision(ctx, d, plannedAt)
	}
}

// plugins materialises the registry into the []source.Plugin Gather consumes.
// A plugin the registry cannot resolve is audited and skipped — the same
// isolation discoverAll gives a single broken source.
func (l *Loop) plugins(ctx context.Context) []source.Plugin {
	names := l.registry.Names()
	out := make([]source.Plugin, 0, len(names))
	for _, name := range names {
		p, err := l.registry.Get(name)
		if err != nil {
			l.auditor.Record(config.AuditEvent{
				Action: "loop.discover.fail",
				Key:    "source:" + name,
				Value:  logging.Redact(err.Error()),
			})
			continue
		}
		out = append(out, p)
	}
	return out
}

// checkpointStore exposes the kv_state repo as the narrow collect.Checkpoint
// Gather needs, or nil when no kv was wired (Gather then treats every source
// as List-only). Returning an explicit nil — not a non-nil interface wrapping
// a nil repo — keeps Gather's `cp == nil` guard correct.
func (l *Loop) checkpointStore() collect.Checkpoint {
	if l.kv == nil {
		return nil
	}
	return l.kv
}

// persistDecision writes one planned candidate to tasks.db: it materialises a
// new row (or finds the already-discovered one) and records the verdict
// (plan_*) plus the verdict-mapped status. Per-row failures are audited and
// skipped so one bad candidate does not abort the rest of the plan.
func (l *Loop) persistDecision(ctx context.Context, d manage.PlannedCandidate, plannedAt time.Time) {
	c := d.Candidate
	// Upstream completion wins over the verdict's status: an already-done
	// item must not re-enter the queue as pending (mirrors the legacy
	// taskFromSource Done handling), and it carries no plan label because the
	// management verdict is moot for something already finished upstream.
	status := d.Status
	if c.Done {
		status = store.StatusDone
	}

	id, err := l.manageStore.Insert(ctx, taskFromCandidate(c, status))
	if err != nil {
		if !errors.Is(err, store.ErrDuplicateExternalID) {
			l.auditManageFail(c.Source, fmt.Sprintf("insert %q: %v", c.ExternalID, err))
			return
		}
		existing, getErr := l.manageStore.GetBySourceExternalID(ctx, c.Source, c.ExternalID)
		if getErr != nil {
			l.auditManageFail(c.Source, fmt.Sprintf("lookup %q: %v", c.ExternalID, getErr))
			return
		}
		id = existing.ID
		// A row the executor is running, or one that already reached a
		// terminal result (done / failed), is owned by the executor /
		// completion path — a fresh re-classification must touch neither its
		// status nor its plan label, so we bail before both. Planner-owned
		// states (pending / waiting_human / skipped) are re-evaluated in full
		// so the two never drift apart: a needs-human row that now carries
		// enough info promotes cleanly to ready + pending instead of becoming
		// a ready/waiting_human row the dispatcher can never pick.
		if executorOwned(existing.Status) {
			return
		}
		if status != existing.Status {
			if upErr := l.manageStore.UpdateStatus(ctx, id, status); upErr != nil {
				l.auditManageFail(c.Source, fmt.Sprintf("status %q: %v", c.ExternalID, upErr))
				return
			}
		}
	}

	if c.Done {
		return
	}
	// Redact the reason before it lands in plan_reason: collect.Candidate.Reason
	// can carry message-derived text from a custom early-triage rule, and its
	// own doc mandates routing through logging.Redact at any persistence sink
	// (matching the judgment_reason write in collect/triage.go).
	if err := l.manageStore.SetPlan(ctx, id, string(d.Verdict), logging.Redact(d.Reason), d.Score, d.Rank, plannedAt); err != nil {
		l.auditManageFail(c.Source, fmt.Sprintf("set plan %q: %v", c.ExternalID, err))
	}
}

// executorOwned reports whether a row's status is owned by the executor or the
// completion path rather than the planner. running means a session is live;
// done / failed are terminal results the completion / dispatch path recorded.
// The planner leaves these untouched when re-evaluating a re-emitted candidate.
func executorOwned(status string) bool {
	switch status {
	case store.StatusRunning, store.StatusDone, store.StatusFailed:
		return true
	default:
		return false
	}
}

// auditManageFail records one per-row management-persist failure, redacting
// the message so a candidate field that leaked a secret does not pin it into
// the audit sink.
func (l *Loop) auditManageFail(source, msg string) {
	l.auditor.Record(config.AuditEvent{
		Action: "loop.manage.fail",
		Key:    "source:" + source,
		Value:  logging.Redact(msg),
	})
}

// taskFromCandidate maps a collect.Candidate into the store.Task shape, with
// the same Source-forced and SourcePath→ExternalURL conventions taskFromSource
// applies on the legacy path. status is the verdict-mapped status the manage
// layer resolved (or done for an upstream-complete item).
func taskFromCandidate(c collect.Candidate, status string) store.Task {
	return store.Task{
		Source:      c.Source,
		ExternalID:  c.ExternalID,
		ExternalURL: c.SourcePath,
		Title:       candidateTitle(c),
		Body:        c.Body,
		Notes:       c.Notes,
		Status:      status,
	}
}

// candidateTitle returns the candidate's trimmed title, or a placeholder when
// it is empty. An empty title is exactly what the rule engine escalates to
// needs-human, but the store rejects a blank title — so without a placeholder
// the escalated row never persists and the candidate vanishes from the
// waiting_human queue (a silent escalation loss). The placeholder keeps the row
// visible to a human, who can rename it during triage.
func candidateTitle(c collect.Candidate) string {
	if title := strings.TrimSpace(c.Title); title != "" {
		return title
	}
	if c.ExternalID != "" {
		return fmt.Sprintf("(untitled %s item %s)", c.Source, c.ExternalID)
	}
	return fmt.Sprintf("(untitled %s item)", c.Source)
}

// discoverAll walks the registry and runs each plugin's Since/List in
// turn. A failure on any single plugin is recorded and skipped so the
// rest of the iteration proceeds — invariant from PR-71's brief and
// requirement.md "他のソースの discovery / 既存タスクの dispatch は
// 継続（巻き込み故障させない）".
func (l *Loop) discoverAll(ctx context.Context) {
	for _, name := range l.registry.Names() {
		if err := ctx.Err(); err != nil {
			return
		}
		plugin, err := l.registry.Get(name)
		if err != nil {
			l.auditor.Record(config.AuditEvent{
				Action: "loop.discover.fail",
				Key:    "source:" + name,
				Value:  logging.Redact(err.Error()),
			})
			continue
		}
		l.discoverOne(ctx, plugin)
	}
}

// discoverOne runs a single plugin's Since/List, upserts the tasks, and
// advances the per-plugin kv_state checkpoint on success. Failure
// records an audit entry and returns; the caller continues to the next
// plugin.
func (l *Loop) discoverOne(ctx context.Context, plugin source.Plugin) {
	name := plugin.Name()
	tasks, err := l.fetch(ctx, plugin)
	if err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.discover.fail",
			Key:    "source:" + name,
			Value:  logging.Redact(err.Error()),
		})
		return
	}
	for _, t := range tasks {
		if err := ctx.Err(); err != nil {
			return
		}
		row := taskFromSource(t, name)
		if _, insErr := l.repo.Insert(ctx, row); insErr != nil {
			if errors.Is(insErr, store.ErrDuplicateExternalID) {
				continue
			}
			l.auditor.Record(config.AuditEvent{
				Action: "loop.discover.fail",
				Key:    "source:" + name,
				Value:  logging.Redact(fmt.Sprintf("insert %q: %v", t.ExternalID, insErr)),
			})
			return
		}
	}
	l.advanceCheckpoint(ctx, name)
}

// fetch chooses Sincer.Since over Plugin.List when the plugin satisfies
// the Sincer interface — the manifest-side capability flag is not
// consulted here because the registry's ValidateAgainstManifest already
// guarantees the two agree at registration time.
func (l *Loop) fetch(ctx context.Context, plugin source.Plugin) ([]source.Task, error) {
	if s, ok := plugin.(source.Sincer); ok {
		checkpoint := ""
		if l.kv != nil {
			cp, err := l.kv.Get(ctx, checkpointKeyPrefix+plugin.Name())
			if err == nil {
				checkpoint = cp
			} else if !errors.Is(err, store.ErrKVNotFound) {
				return nil, fmt.Errorf("read checkpoint: %w", err)
			}
		}
		return s.Since(ctx, checkpoint)
	}
	return plugin.List(ctx)
}

// advanceCheckpoint stamps loop.checkpoint.<name> with the current
// clock as RFC3339Nano. Best-effort: a failure here is audited but does
// not break the iteration — the worst case is the next tick re-fetches
// items the previous tick already saw, which Insert's ErrDuplicateExternalID
// branch tolerates.
func (l *Loop) advanceCheckpoint(ctx context.Context, name string) {
	if l.kv == nil {
		return
	}
	value := l.now().UTC().Format(time.RFC3339Nano)
	if err := l.kv.Set(ctx, checkpointKeyPrefix+name, value); err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.checkpoint.fail",
			Key:    "source:" + name,
			Value:  logging.Redact(err.Error()),
		})
	}
}

// taskFromSource maps a source.Task into the store.Task shape with the
// loop's invariants applied:
//
//   - Source is forced to plugin.Name() so a misbehaving plugin cannot
//     smuggle rows under another source's name.
//   - SourcePath maps to ExternalURL — the queue schema's "where did
//     this come from" column. A `marunage show` row links back through
//     it, and the dispatcher's reversibility audit reads it. Plugins
//     with no notion of a path leave SourcePath empty and the column
//     stays NULL.
//   - Done = true is honoured by inserting with status = done, so an
//     already-finished upstream item is not silently re-dispatched on
//     every tick. Done = false leaves status zero so Insert defaults
//     to pending.
//   - source.Task.Priority (a plugin's free-form hint string) is
//     intentionally NOT mapped onto store.Task.Priority (an integer the
//     triage layer owns). Triage (PR-72) reads the hint via Notes /
//     Body and decides the numeric weight; honouring the hint here
//     would let plugins write directly into the queue's priority lane,
//     defeating the triage hand-off requirement.md describes.
//   - RawMetadata is dropped — the queue schema has no column for it
//     and the markdown adapter only uses it for line-number debugging.
func taskFromSource(t source.Task, sourceName string) store.Task {
	row := store.Task{
		Source:      sourceName,
		ExternalID:  t.ExternalID,
		ExternalURL: t.SourcePath,
		Title:       strings.TrimSpace(t.Title),
		Body:        t.Body,
		Notes:       t.Notes,
	}
	if t.Done {
		row.Status = store.StatusDone
	}
	return row
}

// acquireLock claims the configured lock via the atomic InsertIfAbsent
// primitive. Returns (true, nil) on a successful claim, (false, nil)
// when another holder owns the row, and (_, err) on a store-level
// failure. The single SQL statement avoids the Get → Set TOCTOU two
// design-review agents flagged: two callers racing both observing
// "absent" cannot both succeed.
func (l *Loop) acquireLock(ctx context.Context) (bool, error) {
	ok, err := l.kv.InsertIfAbsent(ctx, lockKeyPrefix+l.lockKey, l.ownerToken)
	if err != nil {
		return false, fmt.Errorf("loop: acquire lock: %w", err)
	}
	return ok, nil
}

// releaseLock drops the lock row only when its value still equals this
// Loop's owner token. The owner-tagged release primitive prevents the
// "stale defer stomps the live holder" bug — if another process
// re-acquired the lock after this one was forcibly evicted, our
// deferred release sees value mismatch and is a no-op. Best-effort: a
// store-level failure is redacted + audited but does not bubble.
func (l *Loop) releaseLock(ctx context.Context) {
	key := lockKeyPrefix + l.lockKey
	deleted, err := l.kv.DeleteIfValue(ctx, key, l.ownerToken)
	if err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.lock.release.fail",
			Key:    key,
			Value:  logging.Redact(err.Error()),
		})
		return
	}
	if !deleted {
		// Either the row was already gone or another owner claimed it.
		// Audit so an operator inspecting a "lock churn" symptom has a
		// trail to follow without parsing application logs.
		l.auditor.Record(config.AuditEvent{
			Action: "loop.lock.release.skipped",
			Key:    key,
			Value:  "owner token mismatch (another process holds the lock)",
		})
	}
}

// durationOrError returns "" on success and the redacted error string
// otherwise, so the loop.end audit entry's Value column carries enough
// context for a post-mortem without forcing the auditor schema to grow
// new fields. Redaction matches the per-phase failure audits so a
// secret cannot survive a wrapping fmt.Errorf into the loop.end entry.
func durationOrError(err error) string {
	if err == nil {
		return ""
	}
	return logging.Redact(err.Error())
}
