package loop_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/loop"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
)

// PR-71 internal/loop test list — see .test-list-pr71.md for the canonical
// list. Tests are kept in execution order so a Red-failure points at the
// first missing piece of the orchestrator.

// fakePlugin is the source.Plugin double used across loop tests. Tests
// override `listFn` / `sinceFn` per-case to inject success / failure / a
// scripted task slice without spinning up the markdown plugin.
type fakePlugin struct {
	name      string
	listFn    func(ctx context.Context) ([]source.Task, error)
	sinceFn   func(ctx context.Context, checkpoint string) ([]source.Task, error)
	listCalls int
	sinceArgs []string
	mu        sync.Mutex
}

func (f *fakePlugin) Name() string { return f.name }

func (f *fakePlugin) List(ctx context.Context) ([]source.Task, error) {
	f.mu.Lock()
	f.listCalls++
	f.mu.Unlock()
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx)
}

func (f *fakePlugin) Setup(context.Context, source.SetupOptions) error { return nil }

func (f *fakePlugin) AuthStatus(context.Context) (source.AuthStatus, error) {
	return source.AuthAuthenticated, nil
}

// sincerPlugin embeds fakePlugin and additionally implements source.Sincer.
// We split it out so a non-Sincer plugin truly does NOT satisfy the
// interface (Go's structural typing makes adding Since to fakePlugin
// silently change the type assertion in production code).
type sincerPlugin struct{ *fakePlugin }

func (s sincerPlugin) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	s.mu.Lock()
	s.sinceArgs = append(s.sinceArgs, checkpoint)
	s.mu.Unlock()
	if s.sinceFn == nil {
		return nil, nil
	}
	return s.sinceFn(ctx, checkpoint)
}

// fakeDispatcher is the loop.Dispatcher double. Captures every Run call
// so tests can assert "dispatch was invoked exactly once with the
// configured MaxParallel".
type fakeDispatcher struct {
	mu    sync.Mutex
	calls []dispatch.RunOptions
	err   error
}

func (f *fakeDispatcher) Run(_ context.Context, opts dispatch.RunOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, opts)
	return f.err
}

func (f *fakeDispatcher) snapshot() []dispatch.RunOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]dispatch.RunOptions, len(f.calls))
	copy(out, f.calls)
	return out
}

// fakeRender is the loop.Render double. Records each call so tests can
// assert it ran exactly once per RunOnce.
type fakeRender struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeRender) Render(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeRender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// recordingAuditor mirrors the helper used by reaper / dispatch tests.
type recordingAuditor struct {
	mu     sync.Mutex
	events []config.AuditEvent
}

func (a *recordingAuditor) Record(e config.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *recordingAuditor) snapshot() []config.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]config.AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

func (a *recordingAuditor) actions() []string {
	out := []string{}
	for _, e := range a.snapshot() {
		out = append(out, e.Action)
	}
	return out
}

// fixture wires a real on-disk SQLite TaskRepo + KVStateRepo, a fake
// dispatcher, a fake renderer, and a recording auditor with a
// deterministic clock. Tests register one or more fakePlugins as needed.
type fixture struct {
	repo *store.TaskRepo
	kv   *store.KVStateRepo
	reg  *source.Registry
	disp *fakeDispatcher
	rend *fakeRender
	aud  *recordingAuditor
	now  time.Time
	ctx  context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return now }))
	kv := store.NewKVStateRepo(db, store.WithKVClock(func() time.Time { return now }))

	return &fixture{
		repo: repo,
		kv:   kv,
		reg:  source.NewRegistry(),
		disp: &fakeDispatcher{},
		rend: &fakeRender{},
		aud:  &recordingAuditor{},
		now:  now,
		ctx:  context.Background(),
	}
}

func (f *fixture) newLoop(t *testing.T, extra ...loop.Option) *loop.Loop {
	t.Helper()
	opts := []loop.Option{
		loop.WithRegistry(f.reg),
		loop.WithTaskRepo(f.repo),
		loop.WithKVStateRepo(f.kv),
		loop.WithDispatcher(f.disp),
		loop.WithRender(f.rend),
		loop.WithAuditor(f.aud),
		loop.WithClock(func() time.Time { return f.now }),
		loop.WithMaxParallel(2),
	}
	opts = append(opts, extra...)
	l, err := loop.New(opts...)
	if err != nil {
		t.Fatalf("loop.New: %v", err)
	}
	return l
}

// N1 — N4: required options.

func TestNew_RequiresRegistry(t *testing.T) {
	t.Parallel()
	_, err := loop.New(
		loop.WithTaskRepo(stubRepo{}),
		loop.WithDispatcher(&fakeDispatcher{}),
		loop.WithRender(&fakeRender{}),
	)
	if !errors.Is(err, loop.ErrInvalidConfig) {
		t.Fatalf("missing registry should return ErrInvalidConfig; got %v", err)
	}
}

func TestNew_RequiresTaskRepo(t *testing.T) {
	t.Parallel()
	_, err := loop.New(
		loop.WithRegistry(source.NewRegistry()),
		loop.WithDispatcher(&fakeDispatcher{}),
		loop.WithRender(&fakeRender{}),
	)
	if !errors.Is(err, loop.ErrInvalidConfig) {
		t.Fatalf("missing task repo should return ErrInvalidConfig; got %v", err)
	}
}

func TestNew_RequiresDispatcher(t *testing.T) {
	t.Parallel()
	_, err := loop.New(
		loop.WithRegistry(source.NewRegistry()),
		loop.WithTaskRepo(stubRepo{}),
		loop.WithRender(&fakeRender{}),
	)
	if !errors.Is(err, loop.ErrInvalidConfig) {
		t.Fatalf("missing dispatcher should return ErrInvalidConfig; got %v", err)
	}
}

func TestNew_RequiresRender(t *testing.T) {
	t.Parallel()
	_, err := loop.New(
		loop.WithRegistry(source.NewRegistry()),
		loop.WithTaskRepo(stubRepo{}),
		loop.WithDispatcher(&fakeDispatcher{}),
	)
	if !errors.Is(err, loop.ErrInvalidConfig) {
		t.Fatalf("missing render should return ErrInvalidConfig; got %v", err)
	}
}

// O1: empty registry still calls dispatcher + render exactly once.
func TestRunOnce_EmptyRegistry_StillDispatchesAndRenders(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	calls := f.disp.snapshot()
	if len(calls) != 1 {
		t.Fatalf("dispatcher Run called %d times; want 1", len(calls))
	}
	if calls[0].MaxParallel != 2 {
		t.Errorf("MaxParallel = %d; want 2", calls[0].MaxParallel)
	}
	if got := f.rend.count(); got != 1 {
		t.Errorf("Render called %d times; want 1", got)
	}
}

// O2: discover walks every plugin and inserts the returned tasks.
func TestRunOnce_DiscoversAndInsertsTasks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plug := &fakePlugin{
		name: "manual",
		listFn: func(context.Context) ([]source.Task, error) {
			return []source.Task{
				{Source: "manual", ExternalID: "ext-1", Title: "First", Body: "body-1"},
				{Source: "manual", ExternalID: "ext-2", Title: "Second"},
			}, nil
		},
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d; want 2", len(rows))
	}
	got := map[string]store.Task{}
	for _, r := range rows {
		got[r.ExternalID] = r
	}
	if got["ext-1"].Title != "First" {
		t.Errorf("ext-1 title = %q; want First", got["ext-1"].Title)
	}
	if got["ext-1"].Body != "body-1" {
		t.Errorf("ext-1 body = %q; want body-1", got["ext-1"].Body)
	}
	if got["ext-1"].Source != "manual" {
		t.Errorf("ext-1 source = %q; want manual", got["ext-1"].Source)
	}
	if got["ext-1"].Status != store.StatusPending {
		t.Errorf("ext-1 status = %q; want pending", got["ext-1"].Status)
	}
}

// O2b: source.Task.Done = true must NOT be silently demoted to pending.
// Per the source.Plugin contract, Done flags upstream completion at
// observation time; if the loop materialises every task as pending, an
// already-finished upstream item would be re-dispatched on every tick
// until a triage step intervened. Insert with status = done so the
// queue and view.md reflect the upstream truth and the dispatcher's
// "pending" filter naturally skips it.
func TestRunOnce_DonePreservesUpstreamStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plug := &fakePlugin{
		name: "manual",
		listFn: func(context.Context) ([]source.Task, error) {
			return []source.Task{
				{Source: "manual", ExternalID: "ext-open", Title: "Still open"},
				{Source: "manual", ExternalID: "ext-done", Title: "Already done", Done: true},
			}, nil
		},
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]store.Task{}
	for _, r := range rows {
		got[r.ExternalID] = r
	}
	if got["ext-open"].Status != store.StatusPending {
		t.Errorf("ext-open status = %q; want pending", got["ext-open"].Status)
	}
	if got["ext-done"].Status != store.StatusDone {
		t.Errorf("ext-done status = %q; want done (upstream Done=true must propagate)", got["ext-done"].Status)
	}
}

// O2c: source.Task.SourcePath rides into store.Task.ExternalURL so a
// `marunage show` row can link back to the file / URL the task came
// from. Plugins that have no notion of a path leave SourcePath empty
// and the column stays NULL.
func TestRunOnce_SourcePathBecomesExternalURL(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plug := &fakePlugin{
		name: "manual",
		listFn: func(context.Context) ([]source.Task, error) {
			return []source.Task{
				{Source: "manual", ExternalID: "ext-1", Title: "First", SourcePath: "/tmp/todo.md"},
				{Source: "manual", ExternalID: "ext-2", Title: "Second"},
			}, nil
		},
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]store.Task{}
	for _, r := range rows {
		got[r.ExternalID] = r
	}
	if got["ext-1"].ExternalURL != "/tmp/todo.md" {
		t.Errorf("ext-1 ExternalURL = %q; want /tmp/todo.md (SourcePath should map)", got["ext-1"].ExternalURL)
	}
	if got["ext-2"].ExternalURL != "" {
		t.Errorf("ext-2 ExternalURL = %q; want empty (no SourcePath)", got["ext-2"].ExternalURL)
	}
}

// O3: a duplicate external_id is silently skipped (idempotent re-discovery).
func TestRunOnce_SkipsDuplicateExternalID(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", ExternalID: "ext-1", Title: "Pre-existing",
	}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	plug := &fakePlugin{
		name: "manual",
		listFn: func(context.Context) ([]source.Task, error) {
			return []source.Task{
				{Source: "manual", ExternalID: "ext-1", Title: "Re-discovered"},
				{Source: "manual", ExternalID: "ext-2", Title: "New"},
			}, nil
		},
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d; want 2 (one pre-existing + one new)", len(rows))
	}
	for _, r := range rows {
		if r.ExternalID == "ext-1" && r.Title == "Re-discovered" {
			t.Errorf("duplicate insert should NOT overwrite existing title; got %q", r.Title)
		}
	}
}

// O4: Sincer plugin gets the kv_state checkpoint as the argument.
func TestRunOnce_SincerPluginReceivesCheckpoint(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.kv.Set(f.ctx, "loop.checkpoint.gmail", "saved-checkpoint-v1"); err != nil {
		t.Fatalf("seed kv: %v", err)
	}
	inner := &fakePlugin{
		name: "gmail",
		sinceFn: func(_ context.Context, _ string) ([]source.Task, error) {
			return nil, nil
		},
	}
	plug := sincerPlugin{fakePlugin: inner}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	inner.mu.Lock()
	args := append([]string(nil), inner.sinceArgs...)
	listCalls := inner.listCalls
	inner.mu.Unlock()
	if listCalls != 0 {
		t.Errorf("Sincer plugin's List should NOT be called; got %d calls", listCalls)
	}
	if len(args) != 1 || args[0] != "saved-checkpoint-v1" {
		t.Errorf("Sincer received args %v; want [\"saved-checkpoint-v1\"]", args)
	}
}

// O5: checkpoint is written after a successful run.
func TestRunOnce_WritesCheckpointAfterSuccess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plug := &fakePlugin{name: "manual"}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got, err := f.kv.Get(f.ctx, "loop.checkpoint.manual")
	if err != nil {
		t.Fatalf("kv.Get checkpoint: %v", err)
	}
	want := f.now.UTC().Format(time.RFC3339Nano)
	if got != want {
		t.Errorf("checkpoint = %q; want %q", got, want)
	}
}

// O6: a failing plugin does not abort the rest of discovery.
func TestRunOnce_PluginErrorContinues(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	bad := &fakePlugin{
		name: "broken",
		listFn: func(context.Context) ([]source.Task, error) {
			return nil, errors.New("boom")
		},
	}
	good := &fakePlugin{
		name: "good",
		listFn: func(context.Context) ([]source.Task, error) {
			return []source.Task{
				{Source: "good", ExternalID: "g-1", Title: "good task"},
			}, nil
		},
	}
	if err := f.reg.Register(bad); err != nil {
		t.Fatalf("register bad: %v", err)
	}
	if err := f.reg.Register(good); err != nil {
		t.Fatalf("register good: %v", err)
	}
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce should not bubble per-plugin errors; got %v", err)
	}
	// Good plugin's task landed.
	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Source != "good" {
		t.Fatalf("rows = %+v; want exactly the good task", rows)
	}
	// Audit captured the failure.
	var sawFail bool
	for _, e := range f.aud.snapshot() {
		if e.Action == "loop.discover.fail" && e.Key == "source:broken" {
			sawFail = true
			if e.Value == "" {
				t.Errorf("audit value should carry the error message")
			}
		}
	}
	if !sawFail {
		t.Errorf("audit missing loop.discover.fail; got %v", f.aud.actions())
	}
	// Dispatch + render still ran.
	if len(f.disp.snapshot()) != 1 {
		t.Errorf("dispatcher should still run once; got %d", len(f.disp.snapshot()))
	}
	if f.rend.count() != 1 {
		t.Errorf("render should still run once; got %d", f.rend.count())
	}
}

// O7: loop.start and loop.end audit events bracket the iteration.
func TestRunOnce_RecordsStartAndEndAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	l := f.newLoop(t)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	actions := f.aud.actions()
	if len(actions) < 2 {
		t.Fatalf("audit actions = %v; want at least loop.start and loop.end", actions)
	}
	if actions[0] != "loop.start" {
		t.Errorf("first audit = %q; want loop.start", actions[0])
	}
	if actions[len(actions)-1] != "loop.end" {
		t.Errorf("last audit = %q; want loop.end", actions[len(actions)-1])
	}
}

// O8: ctx cancellation honoured before any plugin runs.
func TestRunOnce_CancelBeforeStart(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plug := &fakePlugin{
		name: "manual",
		listFn: func(context.Context) ([]source.Task, error) {
			t.Errorf("plugin should not be called when ctx is cancelled")
			return nil, nil
		},
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if err := l.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce on cancelled ctx = %v; want context.Canceled", err)
	}
}

// R1: Run executes RunOnce immediately and again every interval.
func TestRun_LoopsUntilCancelled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	l := f.newLoop(t)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, 20*time.Millisecond) }()

	// Wait until at least 3 dispatches have happened, then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for len(f.disp.snapshot()) < 3 {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("dispatcher only ran %d times within 2s; want >=3", len(f.disp.snapshot()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v; want nil on cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run did not return within 1s of cancel")
	}
}

// R3: a RunOnce error does not stop the loop.
func TestRun_TickErrorDoesNotStopLoop(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var ticks atomic.Int32
	plug := &fakePlugin{
		name: "flaky",
		listFn: func(context.Context) ([]source.Task, error) {
			ticks.Add(1)
			return nil, nil
		},
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	f.disp.err = errors.New("dispatch boom")

	l := f.newLoop(t)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, 10*time.Millisecond) }()

	deadline := time.Now().Add(2 * time.Second)
	for ticks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Run did not return within 1s of cancel")
	}
	if ticks.Load() < 3 {
		t.Fatalf("plugin ran %d times; want >=3 (loop kept ticking through errors)", ticks.Load())
	}
}

// R4: invalid interval rejected.
func TestRun_InvalidIntervalReturnsError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	l := f.newLoop(t)
	if err := l.Run(f.ctx, 0); !errors.Is(err, loop.ErrInvalidInterval) {
		t.Fatalf("Run with zero interval = %v; want ErrInvalidInterval", err)
	}
	if err := l.Run(f.ctx, -time.Second); !errors.Is(err, loop.ErrInvalidInterval) {
		t.Fatalf("Run with negative interval = %v; want ErrInvalidInterval", err)
	}
}

// L1 + L2: kv_state lock prevents concurrent RunOnce; lock is released on
// success so a follow-up RunOnce succeeds.
func TestRunOnce_LockKeyPreventsConcurrentRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gate := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	plug := &fakePlugin{
		name: "slow",
		listFn: func(ctx context.Context) ([]source.Task, error) {
			select {
			case gate <- struct{}{}:
			default:
			}
			<-release
			return nil, nil
		},
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l1 := f.newLoop(t, loop.WithLockKey("loop"))
	l2 := f.newLoop(t, loop.WithLockKey("loop"))
	first := make(chan error, 1)
	go func() { first <- l1.RunOnce(f.ctx) }()
	<-gate
	if err := l2.RunOnce(f.ctx); !errors.Is(err, loop.ErrLockBusy) {
		releaseOnce.Do(func() { close(release) })
		<-first
		t.Fatalf("concurrent RunOnce = %v; want ErrLockBusy", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-first; err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	// L2 (re-acquire after release). Plugin will see release already
	// closed so it returns immediately without blocking.
	if err := l2.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce after release: %v", err)
	}
}

// L3: panic in dispatch still releases the lock.
func TestRunOnce_LockReleasedOnPanic(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.disp = &fakeDispatcher{} // baseline
	pdisp := &panicDispatcher{}
	l := f.newLoop(t, loop.WithLockKey("loop"), loop.WithDispatcher(pdisp))

	func() {
		defer func() { _ = recover() }()
		_ = l.RunOnce(f.ctx)
	}()

	// Lock should be cleared. Verify by attempting another RunOnce that
	// uses the default dispatcher; if the lock leaked, this would fail
	// with ErrLockBusy.
	l2 := f.newLoop(t, loop.WithLockKey("loop"))
	if err := l2.RunOnce(f.ctx); err != nil {
		t.Fatalf("post-panic RunOnce: %v", err)
	}
}

// L4: without WithLockKey, two RunOnce calls overlap freely.
func TestRunOnce_NoLockKey_AllowsConcurrent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gate := make(chan struct{}, 2)
	release := make(chan struct{})
	plug := &fakePlugin{
		name: "slow",
		listFn: func(ctx context.Context) ([]source.Task, error) {
			gate <- struct{}{}
			<-release
			return nil, nil
		},
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l1 := f.newLoop(t)
	l2 := f.newLoop(t)
	out := make(chan error, 2)
	go func() { out <- l1.RunOnce(f.ctx) }()
	go func() { out <- l2.RunOnce(f.ctx) }()
	// Both must have entered the plugin to fill the gate.
	for i := 0; i < 2; i++ {
		select {
		case <-gate:
		case <-time.After(time.Second):
			t.Fatalf("only %d RunOnce reached the plugin within 1s; both should without lock", i)
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-out; err != nil {
			t.Errorf("RunOnce[%d] = %v", i, err)
		}
	}
}

// stubRepo is a minimal TaskRepo stand-in used only for the
// New-rejects-missing-X tests where the field is set but never read.
type stubRepo struct{}

func (stubRepo) Insert(context.Context, store.Task) (int64, error) { return 0, nil }
func (stubRepo) List(context.Context, store.ListFilter) ([]store.Task, error) {
	return nil, nil
}

// panicDispatcher is the L3 helper: Dispatch panics so we can assert the
// lock release path lives behind a defer.
type panicDispatcher struct{}

func (panicDispatcher) Run(context.Context, dispatch.RunOptions) error {
	panic(fmt.Errorf("dispatch panicked"))
}

// fakeReaper is the loop.Reaper double. Records each call and can
// optionally inject an error so tests can assert reaper errors are
// audited without stopping the loop tick.
type fakeReaper struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (r *fakeReaper) Run(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.err
}

func (r *fakeReaper) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// RP1: RunOnce calls reaper.Run exactly once when WithReaper is set.
func TestRunOnce_CallsReaperOnceWhenConfigured(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reap := &fakeReaper{}
	l := f.newLoop(t, loop.WithReaper(reap))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := reap.count(); got != 1 {
		t.Errorf("reaper.Run called %d times; want 1", got)
	}
}

// RP2: RunOnce does not require a reaper (optional); existing loops
// without WithReaper must still pass.
func TestRunOnce_NoReaper_StillSucceeds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	l := f.newLoop(t) // no WithReaper
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce without reaper: %v", err)
	}
}

// RP3: a reaper error is audited but does NOT propagate — the tick
// succeeds and the loop keeps running.
func TestRunOnce_ReaperError_AuditedButNotPropagated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reap := &fakeReaper{err: errors.New("reaper boom")}
	l := f.newLoop(t, loop.WithReaper(reap))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce should succeed even when reaper fails; got %v", err)
	}
	var sawFail bool
	for _, e := range f.aud.snapshot() {
		if e.Action == "loop.reaper.fail" {
			sawFail = true
			if e.Value == "" {
				t.Errorf("audit value should carry reaper error message")
			}
		}
	}
	if !sawFail {
		t.Errorf("audit missing loop.reaper.fail; got %v", f.aud.actions())
	}
}

// RP4: reaper runs AFTER render so it can reclaim slots for pending
// tasks already reflected in view.md.
func TestRunOnce_ReaperRunsAfterRender(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var order []string
	var mu sync.Mutex
	trackRender := &trackingRender{fn: func() { mu.Lock(); order = append(order, "render"); mu.Unlock() }}
	trackReap := &trackingReaper{fn: func() { mu.Lock(); order = append(order, "reaper"); mu.Unlock() }}
	l, err := loop.New(
		loop.WithRegistry(f.reg),
		loop.WithTaskRepo(f.repo),
		loop.WithKVStateRepo(f.kv),
		loop.WithDispatcher(f.disp),
		loop.WithRender(trackRender),
		loop.WithReaper(trackReap),
		loop.WithAuditor(f.aud),
		loop.WithClock(func() time.Time { return f.now }),
		loop.WithMaxParallel(1),
	)
	if err != nil {
		t.Fatalf("loop.New: %v", err)
	}
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	renderIdx, reaperIdx := -1, -1
	for i, s := range got {
		switch s {
		case "render":
			renderIdx = i
		case "reaper":
			reaperIdx = i
		}
	}
	if renderIdx < 0 || reaperIdx < 0 || renderIdx >= reaperIdx {
		t.Errorf("call order = %v; want render before reaper", got)
	}
}

// trackingRender and trackingReaper are RP4 helpers that record the
// call sequence for ordering assertions.
type trackingRender struct{ fn func() }

func (r *trackingRender) Render(_ context.Context) error { r.fn(); return nil }

type trackingReaper struct{ fn func() }

func (r *trackingReaper) Run(_ context.Context) error { r.fn(); return nil }

// D1: with a short dispatchInterval, dispatch fires more often than discovery.
func TestRun_DispatchIntervalFiresMoreOftenThanDiscovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plug := &fakePlugin{
		name:   "test",
		listFn: func(context.Context) ([]source.Task, error) { return nil, nil },
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t, loop.WithDispatchInterval(10*time.Millisecond))
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, 100*time.Millisecond) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		disp := len(f.disp.snapshot())
		plug.mu.Lock()
		disc := plug.listCalls
		plug.mu.Unlock()
		if disp > disc+2 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("dispatch calls (%d) did not outpace discover calls (%d) within 2s", disp, disc)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

// D2: dispatch-only ticks skip the discover phase entirely.
func TestRun_DispatchOnlyTickSkipsDiscover(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plug := &fakePlugin{
		name:   "test",
		listFn: func(context.Context) ([]source.Task, error) { return nil, nil },
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
	l := f.newLoop(t, loop.WithDispatchInterval(5*time.Millisecond))
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, time.Hour) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(f.disp.snapshot()) >= 5 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("dispatch only ran %d times within 2s; want >= 5", len(f.disp.snapshot()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	plug.mu.Lock()
	listCalls := plug.listCalls
	plug.mu.Unlock()
	if listCalls != 1 {
		t.Errorf("discover called %d times; want exactly 1 (initial tick only)", listCalls)
	}
	dispCalls := len(f.disp.snapshot())
	if dispCalls < 5 {
		t.Errorf("dispatch ran %d times; want >= 5", dispCalls)
	}
}
