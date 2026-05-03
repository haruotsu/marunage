package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/store"
)

// Store is the narrow read/write surface the dispatcher needs against
// the tasks table. Keeping it as an interface (rather than the concrete
// *store.TaskRepo) is the test seam: production wires the real repo,
// tests can swap in a fake. The method set is intentionally a subset
// of *store.TaskRepo so the concrete type satisfies it implicitly.
type Store interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
	Get(ctx context.Context, id int64) (store.Task, error)
	AcquireLock(ctx context.Context, id int64, lockKey string) error
	ReleaseLock(ctx context.Context, id int64) error
	ClaimWorkspace(ctx context.Context, id int64, ws string) (bool, error)
	SetWorkspace(ctx context.Context, id int64, ws string) error
	UpdateStatus(ctx context.Context, id int64, newStatus string) error
	SetStartedAt(ctx context.Context, id int64, t time.Time) error
	MarkFailedWithReason(ctx context.Context, id int64, reason string) error
	EscalateToHuman(ctx context.Context, id int64, reason string) error
}

// PermissionMatcher abstracts the auto-accept allowlist resolver. The
// concrete implementation in internal/permission satisfies this; the
// interface lives here so test fakes do not need to construct a real
// permission.Matcher.
type PermissionMatcher interface {
	Allow(tool, args string) bool
}

// PermissionDecision is the dispatcher's verdict on one Claude tool
// permission request. The cmux/MCP shim that actually intercepts the
// prompt translates this into the protocol-level reply (allow / deny /
// re-prompt). This type lives in dispatch because the policy lives
// here too.
type PermissionDecision int

const (
	// PermissionAllow: matcher accepted the (tool, args) pair.
	PermissionAllow PermissionDecision = iota
	// PermissionEscalate: matcher denied AND on_unknown_permission =
	// "escalate"; the row has been moved to waiting_human and a
	// dispatch.escalate audit event recorded.
	PermissionEscalate
	// PermissionFail: matcher denied AND on_unknown_permission =
	// "fail"; the row has been moved to failed and a dispatch.fail
	// audit event recorded.
	PermissionFail
	// PermissionAsk: the dispatcher will not decide. Either no matcher
	// is configured (safe default) or the policy is "retry" — the
	// caller is expected to surface the prompt back to a human or to
	// a re-prompt loop.
	PermissionAsk
)

// String aids debug output and -v test failure messages.
func (d PermissionDecision) String() string {
	switch d {
	case PermissionAllow:
		return "allow"
	case PermissionEscalate:
		return "escalate"
	case PermissionFail:
		return "fail"
	case PermissionAsk:
		return "ask"
	}
	return fmt.Sprintf("unknown(%d)", int(d))
}

// Permission policy strings recognised by WithOnUnknownPermission.
// Mirrors config.allowedOnUnknownPermissions; the validation here is a
// defence-in-depth against a CLI / test that bypasses Config.Validate.
const (
	policyEscalate = "escalate"
	policyFail     = "fail"
	policyRetry    = "retry"
)

func validPermissionPolicy(p string) bool {
	switch p {
	case "", policyEscalate, policyFail, policyRetry:
		return true
	}
	return false
}

// SourceSkillFunc resolves the source-specific prompt skill (the
// contents of skills/marunage-source-<name>/SKILL.md). Returns "" for
// sources without a dedicated skill — BuildPrompt will then collapse
// the section cleanly.
type SourceSkillFunc func(source string) string

// Dispatcher ties the cmux client + store repo together with the
// lock-key resolver and prompt builder.
type Dispatcher struct {
	store               Store
	cmux                cmux.Client
	now                 func() time.Time
	baseSkill           string
	sourceSkill         SourceSkillFunc
	lockKeys            map[string]string
	claudeCommand       string
	allowedCwdPrefixes  []string
	auditor             config.Auditor
	matcher             PermissionMatcher
	onUnknownPermission string
}

// Option mutates Dispatcher construction.
type Option func(*Dispatcher)

// WithStore injects the tasks-table repository. Required.
func WithStore(s Store) Option { return func(d *Dispatcher) { d.store = s } }

// WithCmux injects the cmux client. Required.
func WithCmux(c cmux.Client) Option { return func(d *Dispatcher) { d.cmux = c } }

// WithClock injects a deterministic clock for started_at writes.
// Defaults to time.Now in production.
func WithClock(now func() time.Time) Option { return func(d *Dispatcher) { d.now = now } }

// WithBaseSkill sets the base execution skill content. Required.
func WithBaseSkill(s string) Option { return func(d *Dispatcher) { d.baseSkill = s } }

// WithSourceSkill installs the source-specific skill resolver. Optional;
// when absent, source-specific guidance is omitted from every prompt.
func WithSourceSkill(f SourceSkillFunc) Option { return func(d *Dispatcher) { d.sourceSkill = f } }

// WithLockKeys installs the [execution.lock_keys] regex map used to
// resolve notes.lock_hint. Optional; when absent, no AcquireLock call
// is ever issued and every row dispatches without contention.
func WithLockKeys(m map[string]string) Option { return func(d *Dispatcher) { d.lockKeys = m } }

// WithClaudeCommand sets the shell command cmux runs inside each new
// workspace. Required.
func WithClaudeCommand(s string) Option { return func(d *Dispatcher) { d.claudeCommand = s } }

// WithAuditor installs the audit-log sink. Every dispatch start and
// per-row failure records one event so requirement.md L29 invariant #2
// "No silent execution" + L745 ("各ディスパッチで誰が何のタスクをいつ
// 何にディスパッチしたか・どの権限モードで起動したかを残す") are
// honoured. Defaults to config.NopAuditor so existing tests / CLI paths
// that have not yet wired audit.log keep building.
func WithAuditor(a config.Auditor) Option {
	return func(d *Dispatcher) { d.auditor = a }
}

// WithPermissionMatcher installs the auto-accept allowlist resolver.
// Optional; when nil, HandlePermissionRequest returns PermissionAsk for
// every prompt (safe default — never silently allow). Production
// callers wire `permission.New(cfg.Execution.AutoAcceptTools)`.
func WithPermissionMatcher(m PermissionMatcher) Option {
	return func(d *Dispatcher) { d.matcher = m }
}

// WithOnUnknownPermission selects the policy applied when the matcher
// denies a request. Accepts the same strings config.toml's
// execution.on_unknown_permission accepts: "escalate", "fail",
// "retry". Empty / unset is treated as PermissionAsk (the dispatcher
// abstains and the caller re-prompts the human). New() returns
// ErrInvalidConfig for any other value.
func WithOnUnknownPermission(p string) Option {
	return func(d *Dispatcher) { d.onUnknownPermission = p }
}

// WithAllowedCwdPrefixes installs the cfg.Execution.AllowedCwdPrefixes
// allowlist. A row whose CWD does not start with any listed prefix is
// failed before NewWorkspace, per requirement.md L687 / L774. An empty
// or nil slice means "no whitelist" (all paths allowed) — matching the
// spec's "空配列の場合はホワイトリストを無効化（全パス許可）".
//
// Prefix matching is byte-literal strings.HasPrefix; the config layer
// is responsible for any path normalisation (e.g. ~ expansion) before
// the slice reaches the dispatcher.
func WithAllowedCwdPrefixes(prefixes []string) Option {
	return func(d *Dispatcher) { d.allowedCwdPrefixes = prefixes }
}

// ErrInvalidConfig signals a missing required Option at construction.
var ErrInvalidConfig = errors.New("dispatch: missing required option")

// dispatchClaimSentinel is the placeholder ws value the dispatcher
// writes during the atomic pre-NewWorkspace claim step. The real ws
// ID overwrites it once NewWorkspace returns; on failure the
// dispatcher clears it. The reaper (PR-44) treats this sentinel as a
// "stuck mid-claim" signal — a row stuck in pending with this value
// and an old updated_at means the dispatcher crashed between
// ClaimWorkspace and SetWorkspace, and the row should be reset.
const dispatchClaimSentinel = "__dispatching__"

// New builds a Dispatcher. Required: WithStore, WithCmux, WithBaseSkill,
// WithClaudeCommand. Returns ErrInvalidConfig naming the missing field
// so a buggy CLI wiring fails loud at startup.
func New(opts ...Option) (*Dispatcher, error) {
	d := &Dispatcher{
		now:         time.Now,
		sourceSkill: func(string) string { return "" },
		auditor:     config.NopAuditor{},
	}
	for _, opt := range opts {
		opt(d)
	}
	if d.store == nil {
		return nil, fmt.Errorf("%w: WithStore", ErrInvalidConfig)
	}
	if d.cmux == nil {
		return nil, fmt.Errorf("%w: WithCmux", ErrInvalidConfig)
	}
	if d.baseSkill == "" {
		return nil, fmt.Errorf("%w: WithBaseSkill", ErrInvalidConfig)
	}
	if d.claudeCommand == "" {
		return nil, fmt.Errorf("%w: WithClaudeCommand", ErrInvalidConfig)
	}
	if !validPermissionPolicy(d.onUnknownPermission) {
		return nil, fmt.Errorf("%w: on_unknown_permission %q (want escalate / fail / retry / empty)",
			ErrInvalidConfig, d.onUnknownPermission)
	}
	return d, nil
}

// HandlePermissionRequest is the dispatcher-side handler for one
// Claude tool-permission request. The caller (a future cmux/MCP shim)
// supplies the (tool, args) pair Claude wants to invoke; this method
// consults the configured PermissionMatcher and on_unknown_permission
// policy, mutates the row when the policy demands it, records an
// audit entry, and returns the verdict.
//
// Decision matrix:
//
//	matcher.Allow(tool, args) == true       -> PermissionAllow (no row mutation)
//	matcher == nil                          -> PermissionAsk   (safe default)
//	matcher denies + policy "escalate"      -> EscalateToHuman + dispatch.escalate audit -> PermissionEscalate
//	matcher denies + policy "fail"          -> MarkFailedWithReason + dispatch.fail audit -> PermissionFail
//	matcher denies + policy "retry" or ""   -> PermissionAsk   (caller re-prompts)
//
// The reason string written to audit / judgment_reason runs through
// logging.Redact so a tool args payload that happens to carry a Bearer
// header / API key does not pin the secret into either sink.
func (d *Dispatcher) HandlePermissionRequest(ctx context.Context, taskID int64, tool, args string) (PermissionDecision, error) {
	if d.matcher == nil {
		return PermissionAsk, nil
	}
	if d.matcher.Allow(tool, args) {
		return PermissionAllow, nil
	}
	reason := logging.Redact(fmt.Sprintf("auto-accept denied: tool=%q args=%q", tool, args))
	switch d.onUnknownPermission {
	case policyEscalate:
		if err := d.store.EscalateToHuman(ctx, taskID, reason); err != nil {
			return PermissionAsk, fmt.Errorf("dispatch: EscalateToHuman id=%d: %w", taskID, err)
		}
		d.auditor.Record(config.AuditEvent{
			Action: "dispatch.escalate",
			Key:    "task:" + strconv.FormatInt(taskID, 10),
			Value:  reason,
		})
		return PermissionEscalate, nil
	case policyFail:
		// markFailed already redacts; pass the un-prefixed reason so
		// the on-disk text reads consistently with the escalate branch.
		d.markFailed(ctx, taskID, reason)
		return PermissionFail, nil
	default:
		// "retry" / "" / any future value validated by New: defer to
		// the caller. Do not mutate the row.
		return PermissionAsk, nil
	}
}

// RunOptions controls one Run invocation.
type RunOptions struct {
	// MaxParallel caps the number of pending rows dispatched in this Run.
	// Must be > 0 for any rows to dispatch — a zero/negative value is the
	// caller's signal of "do nothing". Ignored when ID > 0 (single-row
	// dispatch always processes exactly that row).
	MaxParallel int
	// ID restricts Run to the specified row, mirroring `marunage dispatch
	// <id>`. The row must currently be pending (post-lock skips and
	// failures still surface as errors / status writes the same way). A
	// zero value means "scan the pending queue".
	ID int64
}

// Run picks pending rows in dispatch order and pushes each into a fresh
// cmux workspace. Per-row failures are isolated so one stuck row does
// not poison the whole batch (docs/requirement.md "他のソースの discovery
// / 既存タスクの dispatch は継続（巻き込み故障させない）"). The returned
// error is reserved for catastrophic failures (e.g. Store.List itself
// blew up); per-row failures are written into the row's status /
// judgment_reason and Run returns nil.
func (d *Dispatcher) Run(ctx context.Context, opts RunOptions) error {
	if opts.ID > 0 {
		return d.runOne(ctx, opts.ID)
	}
	if opts.MaxParallel <= 0 {
		return nil
	}

	candidates, err := d.store.List(ctx, store.ListFilter{
		Statuses: []string{store.StatusPending},
		// Pull more than MaxParallel because some rows may be skipped
		// for lock contention — without slack the dispatcher would stall
		// on a single contended row at the head of the queue. The 4x
		// multiplier is arbitrary; it just has to be enough that a
		// realistic config (max_parallel=3, lock_keys with a few hot
		// keys) has room to find an unblocked row in one List call.
		Limit: opts.MaxParallel * 4,
	})
	if err != nil {
		return fmt.Errorf("dispatch: list pending: %w", err)
	}

	dispatched := 0
	for _, task := range candidates {
		if dispatched >= opts.MaxParallel {
			break
		}
		ok, err := d.dispatchOne(ctx, task)
		if err != nil {
			return err
		}
		if ok {
			dispatched++
		}
	}
	return nil
}

// ErrNotPending signals that `marunage dispatch <id>` was asked to
// dispatch a row that is not currently pending. Distinct from
// store.ErrNotFound so the CLI can print a precise diagnostic.
var ErrNotPending = errors.New("dispatch: task is not pending")

func (d *Dispatcher) runOne(ctx context.Context, id int64) error {
	task, err := d.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if task.Status != store.StatusPending {
		return fmt.Errorf("%w: id=%d status=%q", ErrNotPending, id, task.Status)
	}
	_, err = d.dispatchOne(ctx, task)
	return err
}

// recordAuditStart appends one audit.log entry at the moment the row
// transitions out of pending. Action is "dispatch.start"; Key carries
// the task id; Value carries the cmux ws reference. Called AFTER
// SetWorkspace + SetStartedAt + UpdateStatus(running) so a recorded
// dispatch.start is always backed by a row that actually changed
// state — readers can trust the audit trail.
func (d *Dispatcher) recordAuditStart(taskID int64, ws string) {
	d.auditor.Record(config.AuditEvent{
		Action: "dispatch.start",
		Key:    "task:" + strconv.FormatInt(taskID, 10),
		Value:  ws,
	})
}

// recordAuditFail appends one audit.log entry for any per-row dispatch
// failure (lock_key resolve / WaitReady / Send). Action is
// "dispatch.fail"; Key carries the task id; Value carries the failure
// reason verbatim. Reason has already been written to judgment_reason
// by markFailed; the audit entry is the historical, append-only twin
// for forensics.
func (d *Dispatcher) recordAuditFail(taskID int64, reason string) {
	d.auditor.Record(config.AuditEvent{
		Action: "dispatch.fail",
		Key:    "task:" + strconv.FormatInt(taskID, 10),
		Value:  reason,
	})
}

// markFailed records a dispatch-time failure on the row while
// preserving any prior judgment_reason. requirement.md L567 reserves
// judgment_reason writes to triage / EscalateToHuman; PR-42 inherits
// the dispatcher's "fail loud" need without destroying the triage
// trail that `marunage review` reads for post-mortem.
//
// Strategy: read the row first, prepend any non-empty existing reason
// with "; " before the dispatch reason, then write back via
// MarkFailedWithReason. The Get + Update pair is not atomic, but the
// only writers that race here are EscalateToHuman (PR-71 daemon
// concurrent escalation; not yet wired) and a duplicate dispatch run
// (already prevented by the SetWorkspace + SetStartedAt + UpdateStatus
// sequence). Best-effort fallback: if Get itself fails, write the
// dispatch reason alone so we still surface the failure.
//
// Errors from MarkFailedWithReason are intentionally swallowed: we are
// already in a failure path where surfacing a second error would mask
// the original cmux/Send failure the caller is trying to record. A DB
// failure here is observable through the row staying in running until
// the next Run picks up the inconsistency.
func (d *Dispatcher) markFailed(ctx context.Context, id int64, dispatchReason string) {
	// Strip leaked tokens (cmux stderr can echo back Authorization
	// headers, an Anthropic / GitHub API failure can include the offending
	// key, etc.). Redacting before BOTH the DB write and the audit
	// append keeps secrets out of the persistent and append-only sinks.
	dispatchReason = logging.Redact(dispatchReason)
	cur, err := d.store.Get(ctx, id)
	if err != nil {
		_ = d.store.MarkFailedWithReason(ctx, id, dispatchReason)
		d.recordAuditFail(id, dispatchReason)
		return
	}
	reason := dispatchReason
	if cur.JudgmentReason != "" {
		reason = cur.JudgmentReason + "; " + dispatchReason
	}
	_ = d.store.MarkFailedWithReason(ctx, id, reason)
	d.recordAuditFail(id, dispatchReason)
}

// dispatchOne handles a single candidate. Returns (true, nil) when the
// row was successfully claimed and a Send was attempted (regardless of
// whether the Send itself later failed — the row is no longer eligible
// for a re-pick this round either way). Returns (false, nil) when the
// row was skipped (lock contention, NewWorkspace failure) and the
// MaxParallel budget should be preserved for the next candidate.
//
// A non-nil error is reserved for store-level failures the dispatcher
// cannot recover from; per-row failures are recorded onto the row.
func (d *Dispatcher) dispatchOne(ctx context.Context, task store.Task) (bool, error) {
	// Reject CWD outside the configured allowlist before doing any
	// work. requirement.md L687 promises this gate runs "dispatch 前",
	// so we put it ahead of AcquireLock — no point burning a lock_key
	// on a row we are about to fail anyway.
	if !cwdAllowed(task.CWD, d.allowedCwdPrefixes) {
		d.markFailed(ctx, task.ID,
			fmt.Sprintf("dispatch: cwd %q not in allowed_cwd_prefixes", task.CWD))
		return false, nil
	}

	lockKey, err := ResolveLockKey(d.lockKeys, task.Notes)
	if err != nil {
		// Malformed notes is a Discovery-plugin bug; recording the row as
		// failed here keeps the dispatcher moving instead of blocking the
		// whole queue on one bad row. The row stays in failed with the
		// reason so `marunage review` surfaces it.
		d.markFailed(ctx, task.ID,
			fmt.Sprintf("dispatch: lock_key resolve failed: %v", err))
		return false, nil
	}

	if lockKey != "" {
		if err := d.store.AcquireLock(ctx, task.ID, lockKey); err != nil {
			if errors.Is(err, store.ErrLockHeld) {
				// Skip — another running task holds the same lock_key.
				// The row stays pending; the next Run picks it up when
				// the holder finishes.
				return false, nil
			}
			return false, fmt.Errorf("dispatch: AcquireLock id=%d: %w", task.ID, err)
		}
	}

	// Reserve the row with a sentinel BEFORE NewWorkspace so a concurrent
	// dispatcher cannot also burn a cmux workspace on the same row. The
	// claim is atomic at the SQLite level (UPDATE ... WHERE status=pending
	// AND ws IS NULL); the loser observes claimed=false and abandons.
	// The sentinel is replaced by the real ws ID once NewWorkspace
	// returns.
	claimed, err := d.store.ClaimWorkspace(ctx, task.ID, dispatchClaimSentinel)
	if err != nil {
		if lockKey != "" {
			_ = d.store.ReleaseLock(ctx, task.ID)
		}
		return false, fmt.Errorf("dispatch: ClaimWorkspace id=%d: %w", task.ID, err)
	}
	if !claimed {
		// Lost the race to another dispatcher. Release any lock_key we
		// hold so a sibling row sharing the same key is not blocked.
		if lockKey != "" {
			_ = d.store.ReleaseLock(ctx, task.ID)
		}
		return false, nil
	}

	ws, err := d.cmux.NewWorkspace(ctx, cmux.NewWorkspaceOptions{
		CWD:     task.CWD,
		Command: d.claudeCommand,
		Name:    workspaceName(task),
	})
	if err != nil {
		// Clear the sentinel so the row is safely retryable on the next
		// Run. Release any lock_key we acquired so a sibling row
		// sharing the same resolved key is not blocked indefinitely.
		_ = d.store.SetWorkspace(ctx, task.ID, "")
		if lockKey != "" {
			_ = d.store.ReleaseLock(ctx, task.ID)
		}
		return false, nil
	}

	// Replace the sentinel with the real ws ID. Order from here on is
	// critical: SetStartedAt BEFORE UpdateStatus(running) so the
	// invariant "status=running implies started_at stamped" holds. PR-44
	// reaper's 24h-stuck probe matches on running + started_at < now-24h;
	// a row left running with started_at IS NULL would be invisible to
	// the probe and silently leak.
	if err := d.store.SetWorkspace(ctx, task.ID, ws.ID); err != nil {
		return false, fmt.Errorf("dispatch: SetWorkspace id=%d ws=%s: %w", task.ID, ws.ID, err)
	}
	if err := d.store.SetStartedAt(ctx, task.ID, d.now()); err != nil {
		return false, fmt.Errorf("dispatch: SetStartedAt id=%d: %w", task.ID, err)
	}
	if err := d.store.UpdateStatus(ctx, task.ID, store.StatusRunning); err != nil {
		return false, fmt.Errorf("dispatch: UpdateStatus id=%d: %w", task.ID, err)
	}
	d.recordAuditStart(task.ID, ws.ID)

	if err := d.cmux.WaitReady(ctx, ws); err != nil {
		d.markFailed(ctx, task.ID,
			fmt.Sprintf("dispatch: WaitReady failed: %v", err))
		return true, nil
	}

	prompt := BuildPrompt(PromptInputs{
		Base:           d.baseSkill,
		SourceSpecific: d.sourceSkill(task.Source),
		Task:           task,
	})
	if err := d.cmux.Send(ctx, ws, prompt); err != nil {
		d.markFailed(ctx, task.ID,
			fmt.Sprintf("dispatch: Send failed: %v", err))
		return true, nil
	}
	return true, nil
}

// cwdAllowed returns true when cwd starts with any of prefixes, or
// when prefixes is empty (the spec's "all paths allowed" mode).
func cwdAllowed(cwd string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(cwd, p) {
			return true
		}
	}
	return false
}

// workspaceName renders the cmux dashboard label per requirement.md
// step 2.a ("--name '#<id> <title短縮>'"). Long titles are truncated so
// the cmux dashboard stays readable on a typical 80-column terminal.
//
// Trim is rune-based, not byte-based: a Japanese / emoji title sliced at
// `title[:titleMaxLen]` would cut mid-rune and emit invalid UTF-8 to the
// cmux label (and to anything downstream that re-parses the name).
const titleMaxLen = 40

func workspaceName(t store.Task) string {
	runes := []rune(t.Title)
	if len(runes) > titleMaxLen {
		runes = runes[:titleMaxLen]
	}
	title := string(runes)
	// Strip embedded newlines so the cmux dashboard line does not break.
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.ReplaceAll(title, "\r", " ")
	return fmt.Sprintf("#%d %s", t.ID, title)
}
