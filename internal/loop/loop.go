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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/dispatch"
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
	Get(ctx context.Context, id int64) (store.Task, error)
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
}

// KVStateRepo is the narrow surface the loop needs against the kv_state
// table. *store.KVStateRepo satisfies it.
type KVStateRepo interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	CompareAndSwap(ctx context.Context, key, expected, newValue string) error
	Delete(ctx context.Context, key string) error
}

// Dispatcher is the dispatch surface the loop needs. *dispatch.Dispatcher
// satisfies it.
type Dispatcher interface {
	Run(ctx context.Context, opts dispatch.RunOptions) error
}

// Render is the render surface the loop needs. The CLI wires a closure
// that calls internal/render.Render and writes ~/.marunage/view.md
// atomically; tests pass a fake to assert "render ran exactly once".
type Render interface {
	Render(ctx context.Context) error
}

// Loop is the orchestrator. One instance per process; concurrency
// guarantees rest on WithLockKey.
type Loop struct {
	registry    *source.Registry
	repo        TaskRepo
	kv          KVStateRepo
	dispatcher  Dispatcher
	render      Render
	auditor     config.Auditor
	now         func() time.Time
	maxParallel int
	lockKey     string
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
	return l, nil
}

// checkpointKeyPrefix namespaces the per-plugin discover checkpoints
// inside the shared kv_state table. The prefix keeps loop-owned keys
// from colliding with plugin-internal ones (e.g. markdown's per-file
// mtime checkpoints).
const checkpointKeyPrefix = "loop.checkpoint."

// lockSentinel is the kv_state value the loop writes to claim its lock.
// The exact value does not matter beyond "non-empty"; the
// CompareAndSwap-based release just needs a reproducible token.
const lockSentinel = "held"

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

	l.discoverAll(ctx)

	if err := l.dispatcher.Run(ctx, dispatch.RunOptions{MaxParallel: l.maxParallel}); err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.dispatch.fail",
			Value:  err.Error(),
		})
		return fmt.Errorf("loop: dispatch: %w", err)
	}

	if err := l.render.Render(ctx); err != nil {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.render.fail",
			Value:  err.Error(),
		})
		return fmt.Errorf("loop: render: %w", err)
	}
	return nil
}

// Run drives RunOnce immediately and then on every interval tick until
// ctx is cancelled. RunOnce errors are audited but do not stop the
// loop — the next tick still runs. This mirrors the dispatch contract:
// per-plugin failures do not poison the queue.
func (l *Loop) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("%w: got %v", ErrInvalidInterval, interval)
	}
	if err := l.runTick(ctx); err != nil {
		return err
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := l.runTick(ctx); err != nil {
				return err
			}
		}
	}
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
			Value:  err.Error(),
		})
	}
	return nil
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
				Value:  err.Error(),
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
			Value:  err.Error(),
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
				Value:  fmt.Sprintf("insert %q: %v", t.ExternalID, insErr),
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
			Value:  err.Error(),
		})
	}
}

// taskFromSource maps a source.Task into the store.Task shape with the
// loop's invariants applied: status defaults to pending (Insert sets it
// when zero), Source is forced to plugin.Name() so a misbehaving plugin
// cannot smuggle rows under another source's name, and RawMetadata is
// dropped — the queue schema does not have a column for it and the
// markdown adapter only uses it for line-number debugging.
func taskFromSource(t source.Task, sourceName string) store.Task {
	return store.Task{
		Source:     sourceName,
		ExternalID: t.ExternalID,
		Title:      strings.TrimSpace(t.Title),
		Body:       t.Body,
		Notes:      t.Notes,
	}
}

// acquireLock attempts a kv_state CompareAndSwap from "" -> sentinel,
// or an Insert when the key is absent. Returns (true, nil) on
// successful claim, (false, nil) when another holder still owns the
// lock, and (_, err) on a store-level failure. The lockKey field
// receives the configured prefix so a future caller using the same key
// for an unrelated kv_state need does not collide.
func (l *Loop) acquireLock(ctx context.Context) (bool, error) {
	key := "loop.lock." + l.lockKey
	cur, err := l.kv.Get(ctx, key)
	if err != nil && !errors.Is(err, store.ErrKVNotFound) {
		return false, fmt.Errorf("loop: acquire lock read: %w", err)
	}
	if errors.Is(err, store.ErrKVNotFound) {
		if setErr := l.kv.Set(ctx, key, lockSentinel); setErr != nil {
			return false, fmt.Errorf("loop: acquire lock set: %w", setErr)
		}
		return true, nil
	}
	// Existing row. If somebody else holds the sentinel, refuse.
	if cur == lockSentinel {
		return false, nil
	}
	// Recover from a stale value (different sentinel). CAS so we do not
	// stomp on a holder racing to write a new sentinel.
	if casErr := l.kv.CompareAndSwap(ctx, key, cur, lockSentinel); casErr != nil {
		if errors.Is(casErr, store.ErrKVStaleValue) {
			return false, nil
		}
		return false, fmt.Errorf("loop: acquire lock cas: %w", casErr)
	}
	return true, nil
}

// releaseLock drops the kv_state row so the next RunOnce can claim it.
// Best-effort: a failure here is audited but does not bubble — the
// caller has already returned from RunOnce. Delete + an audit entry is
// the chosen sequence so a future operator can inspect a stuck row.
func (l *Loop) releaseLock(ctx context.Context) {
	key := "loop.lock." + l.lockKey
	if err := l.kv.Delete(ctx, key); err != nil && !errors.Is(err, store.ErrKVNotFound) {
		l.auditor.Record(config.AuditEvent{
			Action: "loop.lock.release.fail",
			Key:    key,
			Value:  err.Error(),
		})
	}
}

// durationOrError returns "" on success and the error string otherwise,
// so the loop.end audit entry's Value column carries enough context for
// a post-mortem without forcing the auditor schema to grow new fields.
func durationOrError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
