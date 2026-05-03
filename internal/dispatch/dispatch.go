package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/cmux"
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
	SetWorkspace(ctx context.Context, id int64, ws string) error
	UpdateStatus(ctx context.Context, id int64, newStatus string) error
	SetStartedAt(ctx context.Context, id int64, t time.Time) error
	MarkFailedWithReason(ctx context.Context, id int64, reason string) error
}

// SourceSkillFunc resolves the source-specific prompt skill (the
// contents of skills/marunage-source-<name>/SKILL.md). Returns "" for
// sources without a dedicated skill — BuildPrompt will then collapse
// the section cleanly.
type SourceSkillFunc func(source string) string

// Dispatcher ties the cmux client + store repo together with the
// lock-key resolver and prompt builder.
type Dispatcher struct {
	store         Store
	cmux          cmux.Client
	now           func() time.Time
	baseSkill     string
	sourceSkill   SourceSkillFunc
	lockKeys      map[string]string
	claudeCommand string
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

// ErrInvalidConfig signals a missing required Option at construction.
var ErrInvalidConfig = errors.New("dispatch: missing required option")

// New builds a Dispatcher. Required: WithStore, WithCmux, WithBaseSkill,
// WithClaudeCommand. Returns ErrInvalidConfig naming the missing field
// so a buggy CLI wiring fails loud at startup.
func New(opts ...Option) (*Dispatcher, error) {
	d := &Dispatcher{
		now:         time.Now,
		sourceSkill: func(string) string { return "" },
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
	return d, nil
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
	lockKey, err := ResolveLockKey(d.lockKeys, task.Notes)
	if err != nil {
		// Malformed notes is a Discovery-plugin bug; recording the row as
		// failed here keeps the dispatcher moving instead of blocking the
		// whole queue on one bad row. The row stays in failed with the
		// reason so `marunage review` surfaces it.
		_ = d.store.MarkFailedWithReason(ctx, task.ID,
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

	ws, err := d.cmux.NewWorkspace(ctx, cmux.NewWorkspaceOptions{
		CWD:     task.CWD,
		Command: d.claudeCommand,
		Name:    workspaceName(task),
	})
	if err != nil {
		// No claim has been written yet (SetWorkspace happens AFTER
		// NewWorkspace returns), so the row is safely retryable on the
		// next Run. Leave it as pending and move on.
		return false, nil
	}

	// Claim the row immediately so a parallel dispatcher iteration cannot
	// re-pick it. Order is critical: SetWorkspace BEFORE WaitReady so a
	// concurrent List(pending) cannot observe an unclaimed row whose
	// cmux workspace is already alive.
	if err := d.store.SetWorkspace(ctx, task.ID, ws.ID); err != nil {
		return false, fmt.Errorf("dispatch: SetWorkspace id=%d ws=%s: %w", task.ID, ws.ID, err)
	}
	if err := d.store.UpdateStatus(ctx, task.ID, store.StatusRunning); err != nil {
		return false, fmt.Errorf("dispatch: UpdateStatus id=%d: %w", task.ID, err)
	}
	if err := d.store.SetStartedAt(ctx, task.ID, d.now()); err != nil {
		return false, fmt.Errorf("dispatch: SetStartedAt id=%d: %w", task.ID, err)
	}

	if err := d.cmux.WaitReady(ctx, ws); err != nil {
		_ = d.store.MarkFailedWithReason(ctx, task.ID,
			fmt.Sprintf("dispatch: WaitReady failed: %v", err))
		return true, nil
	}

	prompt := BuildPrompt(PromptInputs{
		Base:           d.baseSkill,
		SourceSpecific: d.sourceSkill(task.Source),
		Task:           task,
	})
	if err := d.cmux.Send(ctx, ws, prompt); err != nil {
		_ = d.store.MarkFailedWithReason(ctx, task.ID,
			fmt.Sprintf("dispatch: Send failed: %v", err))
		return true, nil
	}
	return true, nil
}

// workspaceName renders the cmux dashboard label per requirement.md
// step 2.a ("--name '#<id> <title短縮>'"). Long titles are truncated so
// the cmux dashboard stays readable on a typical 80-column terminal.
const titleMaxLen = 40

func workspaceName(t store.Task) string {
	title := t.Title
	if len(title) > titleMaxLen {
		title = title[:titleMaxLen]
	}
	// Strip embedded newlines so the cmux dashboard line does not break.
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.ReplaceAll(title, "\r", " ")
	return fmt.Sprintf("#%d %s", t.ID, title)
}
