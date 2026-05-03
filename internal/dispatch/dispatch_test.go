package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/store"
)

// PR-42 Dispatcher.Run test list (t_wada TDD; ticked off as the matching
// test below goes green):
//
//   C1. Empty pending queue: Run is a no-op, returns nil, never calls
//       cmux.NewWorkspace.
//   C2. Single pending row: Run drives the full happy path —
//       AcquireLock (when lock_hint resolves; here it is empty so we
//       skip that step), NewWorkspace with the documented argv shape,
//       SetWorkspace immediately after NewWorkspace, UpdateStatus to
//       running, SetStartedAt with the dispatcher clock, WaitReady,
//       Send carrying the BuildPrompt result. Order matters because
//       SetWorkspace must precede WaitReady so a parallel iteration
//       cannot re-claim the row mid-flight.
//   C3. Run picks the highest-priority pending first (priority DESC,
//       created_at ASC, id ASC) — the same order store.List exposes.
//   C4. MaxParallel caps the number of dispatched rows. Lower-priority
//       rows past the cap are left as pending (no NewWorkspace call).
//   C5. notes.lock_hint matching a [execution.lock_keys] entry causes
//       the resolver-derived key to be AcquireLock'd before NewWorkspace.
//   C6. AcquireLock returning ErrLockHeld skips the row (no NewWorkspace,
//       no SetWorkspace, no status change) and the dispatcher continues
//       to the next pending row. The skipped row stays pending. The
//       MaxParallel budget is consumed by *successful* dispatches only.

// fakeCmux is the test double for cmux.Client. NewWorkspace returns
// "workspace:N" with N incrementing per call; WaitReady and Send are
// no-ops by default. Tests override the per-method hooks to inject
// failures, delays, etc.
type fakeCmux struct {
	mu sync.Mutex

	newWorkspaceCalls []cmux.NewWorkspaceOptions
	waitReadyCalls    []cmux.Workspace
	sendCalls         []fakeSendCall

	// Per-call hooks. Default behaviour (nil) succeeds.
	newWorkspaceHook func(opts cmux.NewWorkspaceOptions) (cmux.Workspace, error)
	waitReadyHook    func(ws cmux.Workspace) error
	sendHook         func(ws cmux.Workspace, text string) error

	nextID int
}

type fakeSendCall struct {
	WS   cmux.Workspace
	Text string
}

func (f *fakeCmux) NewWorkspace(_ context.Context, opts cmux.NewWorkspaceOptions) (cmux.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newWorkspaceCalls = append(f.newWorkspaceCalls, opts)
	if f.newWorkspaceHook != nil {
		return f.newWorkspaceHook(opts)
	}
	f.nextID++
	return cmux.Workspace{ID: fmt.Sprintf("workspace:%d", f.nextID), Name: opts.Name}, nil
}

func (f *fakeCmux) WaitReady(_ context.Context, ws cmux.Workspace) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitReadyCalls = append(f.waitReadyCalls, ws)
	if f.waitReadyHook != nil {
		return f.waitReadyHook(ws)
	}
	return nil
}

func (f *fakeCmux) Send(_ context.Context, ws cmux.Workspace, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls = append(f.sendCalls, fakeSendCall{WS: ws, Text: text})
	if f.sendHook != nil {
		return f.sendHook(ws, text)
	}
	return nil
}

// dispatchFixture wires a real on-disk SQLite store, a fake cmux client,
// and a Dispatcher with a deterministic clock so tests can assert exact
// started_at values.
type dispatchFixture struct {
	repo  *store.TaskRepo
	cmux  *fakeCmux
	disp  *dispatch.Dispatcher
	now   time.Time
	ctx   context.Context
}

func newDispatchFixture(t *testing.T, opts ...dispatch.Option) dispatchFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return now }))

	fcm := &fakeCmux{}
	defOpts := []dispatch.Option{
		dispatch.WithStore(repo),
		dispatch.WithCmux(fcm),
		dispatch.WithClock(func() time.Time { return now }),
		dispatch.WithBaseSkill("BASE-SKILL"),
		dispatch.WithClaudeCommand("claude --dangerously-skip-permissions"),
	}
	d, err := dispatch.New(append(defOpts, opts...)...)
	if err != nil {
		t.Fatalf("dispatch.New: %v", err)
	}
	return dispatchFixture{
		repo: repo,
		cmux: fcm,
		disp: d,
		now:  now,
		ctx:  context.Background(),
	}
}

// C1: empty queue is a no-op.
func TestRunEmptyQueueIsNoop(t *testing.T) {
	f := newDispatchFixture(t)

	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 3}); err != nil {
		t.Fatalf("Run on empty queue: %v", err)
	}
	if got := len(f.cmux.newWorkspaceCalls); got != 0 {
		t.Errorf("NewWorkspace called %d times on empty queue; want 0", got)
	}
}

// C2: single pending row drives the full happy path with the documented
// argv shape and ordering.
func TestRunDispatchesSinglePending(t *testing.T) {
	f := newDispatchFixture(t)

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual",
		Title:  "Buy milk",
		Body:   "from the corner store",
		CWD:    "/tmp/work",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// NewWorkspace called once with the documented argv shape.
	if len(f.cmux.newWorkspaceCalls) != 1 {
		t.Fatalf("NewWorkspace calls = %d; want 1", len(f.cmux.newWorkspaceCalls))
	}
	got := f.cmux.newWorkspaceCalls[0]
	if got.CWD != "/tmp/work" {
		t.Errorf("CWD = %q; want %q", got.CWD, "/tmp/work")
	}
	if got.Command != "claude --dangerously-skip-permissions" {
		t.Errorf("Command = %q; want claude command", got.Command)
	}
	wantNamePrefix := fmt.Sprintf("#%d ", id)
	if !strings.HasPrefix(got.Name, wantNamePrefix) || !strings.Contains(got.Name, "Buy milk") {
		t.Errorf("Name = %q; want it to start with %q and contain title", got.Name, wantNamePrefix)
	}

	// WaitReady + Send each called once.
	if len(f.cmux.waitReadyCalls) != 1 {
		t.Errorf("WaitReady calls = %d; want 1", len(f.cmux.waitReadyCalls))
	}
	if len(f.cmux.sendCalls) != 1 {
		t.Fatalf("Send calls = %d; want 1", len(f.cmux.sendCalls))
	}

	// Send payload contains the prompt sections.
	payload := f.cmux.sendCalls[0].Text
	for _, want := range []string{"BASE-SKILL", "Buy milk", "from the corner store"} {
		if !strings.Contains(payload, want) {
			t.Errorf("Send payload missing %q; got:\n%s", want, payload)
		}
	}

	// Row state: ws set, status=running, started_at stamped.
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.WS != f.cmux.sendCalls[0].WS.ID {
		t.Errorf("ws = %q; want %q", row.WS, f.cmux.sendCalls[0].WS.ID)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want %q", row.Status, store.StatusRunning)
	}
	if !row.StartedAt.Equal(f.now) {
		t.Errorf("started_at = %v; want %v", row.StartedAt, f.now)
	}
}

// C2b: SetWorkspace must commit BEFORE WaitReady so a parallel dispatcher
// iteration querying for pending rows cannot re-claim the row.
func TestRunWritesWorkspaceBeforeWaitReady(t *testing.T) {
	f := newDispatchFixture(t)

	// Insert a single pending row.
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "ordering test", Body: "b", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// During WaitReady, snapshot the row state. If SetWorkspace ran first,
	// ws is already populated; if it ran second, ws is still empty.
	var wsAtWaitReady, statusAtWaitReady string
	f.cmux.waitReadyHook = func(_ cmux.Workspace) error {
		row, err := f.repo.Get(f.ctx, id)
		if err != nil {
			return fmt.Errorf("probe inside WaitReady: %w", err)
		}
		wsAtWaitReady = row.WS
		statusAtWaitReady = row.Status
		return nil
	}

	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if wsAtWaitReady == "" {
		t.Errorf("ws was empty at WaitReady time; SetWorkspace must run BEFORE WaitReady to prevent duplicate dispatch")
	}
	if statusAtWaitReady != store.StatusRunning {
		t.Errorf("status at WaitReady = %q; want running already (no concurrent dispatcher should pick this row)", statusAtWaitReady)
	}
}

// C3: dispatch order follows priority DESC, created_at ASC.
func TestRunFollowsDispatchOrder(t *testing.T) {
	f := newDispatchFixture(t)

	insert := func(title string, prio int, ageMinutes int) int64 {
		id, err := f.repo.Insert(f.ctx, store.Task{
			Source:    "manual",
			Title:     title,
			CWD:       "/tmp",
			Priority:  prio,
			CreatedAt: f.now.Add(time.Duration(ageMinutes) * time.Minute),
		})
		if err != nil {
			t.Fatalf("Insert %s: %v", title, err)
		}
		return id
	}

	// Order should be: high (prio=5, oldest) first, then medium (prio=5, newer),
	// then low (prio=1).
	highID := insert("high", 5, 0)
	medID := insert("med", 5, 1)
	lowID := insert("low", 1, 0)

	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 3}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.cmux.newWorkspaceCalls) != 3 {
		t.Fatalf("NewWorkspace calls = %d; want 3", len(f.cmux.newWorkspaceCalls))
	}

	wantOrder := []int64{highID, medID, lowID}
	for i, want := range wantOrder {
		got := f.cmux.newWorkspaceCalls[i].Name
		wantPrefix := fmt.Sprintf("#%d ", want)
		if !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("call[%d] Name = %q; want prefix %q", i, got, wantPrefix)
		}
	}
}

// C4: MaxParallel caps dispatch count.
func TestRunHonoursMaxParallel(t *testing.T) {
	f := newDispatchFixture(t)
	for i := 0; i < 5; i++ {
		if _, err := f.repo.Insert(f.ctx, store.Task{
			Source: "manual", Title: fmt.Sprintf("t%d", i), CWD: "/tmp",
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 2}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(f.cmux.newWorkspaceCalls); got != 2 {
		t.Errorf("NewWorkspace calls = %d; want 2 (MaxParallel)", got)
	}
}

// C5: notes.lock_hint resolves via the configured map and AcquireLock is
// called with the resolved key before NewWorkspace.
func TestRunAcquiresLockWhenLockHintResolves(t *testing.T) {
	f := newDispatchFixture(t,
		dispatch.WithLockKeys(map[string]string{
			"^repo:.*": "git-repo",
		}),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual",
		Title:  "needs lock",
		CWD:    "/tmp",
		Notes:  `{"lock_hint":"repo:haruotsu/marunage"}`,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.cmux.newWorkspaceCalls) != 1 {
		t.Fatalf("NewWorkspace calls = %d; want 1", len(f.cmux.newWorkspaceCalls))
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.LockKey != "git-repo" {
		t.Errorf("lock_key after dispatch = %q; want %q (resolved from lock_hint)", row.LockKey, "git-repo")
	}
}

// C6: AcquireLock returning ErrLockHeld skips the row but does not
// consume the MaxParallel budget; the next pending row dispatches and
// the skipped row remains pending.
func TestRunSkipsLockedRowAndContinues(t *testing.T) {
	f := newDispatchFixture(t,
		dispatch.WithLockKeys(map[string]string{
			"^repo:.*": "git-repo",
		}),
	)

	// holder is already running with the same lock_key — blocks the next AcquireLock.
	holderID, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "holder", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert holder: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, holderID, "git-repo"); err != nil {
		t.Fatalf("holder AcquireLock: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, holderID, store.StatusRunning); err != nil {
		t.Fatalf("holder UpdateStatus: %v", err)
	}

	// blockedID: same lock_hint as the holder; should be skipped.
	blockedID, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "blocked", CWD: "/tmp",
		Notes: `{"lock_hint":"repo:haruotsu/marunage"}`,
	})
	if err != nil {
		t.Fatalf("Insert blocked: %v", err)
	}

	// freeID: no lock; should dispatch despite the blocked row sitting first.
	freeID, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "free", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert free: %v", err)
	}

	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// blocked row stays pending, no ws set.
	blocked, err := f.repo.Get(f.ctx, blockedID)
	if err != nil {
		t.Fatalf("Get blocked: %v", err)
	}
	if blocked.Status != store.StatusPending {
		t.Errorf("blocked status = %q; want still %q", blocked.Status, store.StatusPending)
	}
	if blocked.WS != "" {
		t.Errorf("blocked ws = %q; want empty (skip due to lock)", blocked.WS)
	}

	// free row dispatched; ws and status updated.
	free, err := f.repo.Get(f.ctx, freeID)
	if err != nil {
		t.Fatalf("Get free: %v", err)
	}
	if free.Status != store.StatusRunning {
		t.Errorf("free status = %q; want %q", free.Status, store.StatusRunning)
	}
	if free.WS == "" {
		t.Error("free ws is empty; expected dispatched")
	}
	// MaxParallel=1 was consumed by the free row, not the skipped one.
	if got := len(f.cmux.newWorkspaceCalls); got != 1 {
		t.Errorf("NewWorkspace calls = %d; want 1 (locked row must not consume budget)", got)
	}
}

// D1: NewWorkspace failure leaves the row pending so it retries next round.
func TestRunRequeueOnNewWorkspaceFailure(t *testing.T) {
	f := newDispatchFixture(t)
	f.cmux.newWorkspaceHook = func(_ cmux.NewWorkspaceOptions) (cmux.Workspace, error) {
		return cmux.Workspace{}, errors.New("cmux exploded")
	}

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "boom", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run with NewWorkspace failure: %v (Run itself should not propagate per-row errors)", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusPending {
		t.Errorf("status after NewWorkspace failure = %q; want still %q (retryable)", row.Status, store.StatusPending)
	}
	if row.WS != "" {
		t.Errorf("ws after NewWorkspace failure = %q; want empty (no claim was made)", row.WS)
	}
}

// D2: WaitReady failure after SetWorkspace marks the row failed with a
// judgment_reason explaining what went wrong, so the reaper does not
// have to chase a phantom running row.
func TestRunMarksFailedOnWaitReadyError(t *testing.T) {
	f := newDispatchFixture(t)
	f.cmux.waitReadyHook = func(_ cmux.Workspace) error {
		return cmux.ErrTimeout
	}

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "slow start", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status after WaitReady failure = %q; want %q", row.Status, store.StatusFailed)
	}
	if !strings.Contains(row.JudgmentReason, "WaitReady") && !strings.Contains(row.JudgmentReason, "wait") {
		t.Errorf("judgment_reason = %q; want to mention WaitReady", row.JudgmentReason)
	}
	if row.WS == "" {
		t.Errorf("ws cleared on WaitReady failure; want preserved (workspace exists, just unresponsive — reaper visibility)")
	}
}

// D2b: Send failure after a successful WaitReady marks the row failed.
func TestRunMarksFailedOnSendError(t *testing.T) {
	f := newDispatchFixture(t)
	f.cmux.sendHook = func(_ cmux.Workspace, _ string) error {
		return errors.New("cmux send: exit 1")
	}

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "send fail", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status after Send failure = %q; want %q", row.Status, store.StatusFailed)
	}
	if !strings.Contains(row.JudgmentReason, "send") && !strings.Contains(row.JudgmentReason, "Send") {
		t.Errorf("judgment_reason = %q; want to mention Send", row.JudgmentReason)
	}
}
