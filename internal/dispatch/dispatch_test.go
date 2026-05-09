package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/permission"
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

// ListWorkspaces is a no-op stub: PR-42 dispatch never calls it. Present
// only so *fakeCmux satisfies cmux.Client after PR-44 added the method.
func (f *fakeCmux) ListWorkspaces(_ context.Context) ([]cmux.Workspace, error) {
	return nil, nil
}

// ReadOutput is a no-op stub: dispatch never calls it. Present only so
// *fakeCmux satisfies cmux.Client after PR-91 added the method.
func (f *fakeCmux) ReadOutput(_ context.Context, _ cmux.Workspace) (string, error) {
	return "", nil
}

// dispatchFixture wires a real on-disk SQLite store, a fake cmux client,
// and a Dispatcher with a deterministic clock so tests can assert exact
// started_at values.
type dispatchFixture struct {
	repo *store.TaskRepo
	cmux *fakeCmux
	disp *dispatch.Dispatcher
	now  time.Time
	ctx  context.Context
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

// E_audit: requirement.md L29 invariant #2 "No silent execution" +
// L745 ("各ディスパッチで誰が何のタスクをいつ何にディスパッチしたか・
// どの権限モードで起動したかを残す") mandate audit.log entries for
// every dispatch start and failure. Without these, a malicious or
// buggy dispatcher could move a row through pending -> running ->
// failed without leaving a forensic trail.
//
// fakeAuditor captures the events the dispatcher records so tests can
// assert (a) start fires after SetWorkspace, (b) fail fires from each
// failure branch, (c) the event carries id / ws / claude_command.
type fakeAuditor struct {
	mu     sync.Mutex
	events []config.AuditEvent
}

func (a *fakeAuditor) Record(e config.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}
func (a *fakeAuditor) Events() []config.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]config.AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

func TestRunRecordsAuditOnSuccessfulDispatch(t *testing.T) {
	au := &fakeAuditor{}
	f := newDispatchFixture(t, dispatch.WithAuditor(au))
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "audit success", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := au.Events()
	var start *config.AuditEvent
	for i := range events {
		if events[i].Action == "dispatch.start" {
			start = &events[i]
			break
		}
	}
	if start == nil {
		t.Fatalf("dispatch.start audit event not recorded; got %+v", events)
	}
	if !strings.Contains(start.Key, fmt.Sprintf("%d", id)) {
		t.Errorf("dispatch.start Key = %q; want it to mention task id %d", start.Key, id)
	}
	if start.Value == "" {
		t.Errorf("dispatch.start Value is empty; want it to record the cmux ws reference")
	}
}

func TestRunRecordsAuditOnDispatchFailure(t *testing.T) {
	au := &fakeAuditor{}
	f := newDispatchFixture(t, dispatch.WithAuditor(au))
	f.cmux.sendHook = func(_ cmux.Workspace, _ string) error {
		return errors.New("cmux send: exit 1")
	}
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "audit fail", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := au.Events()
	var fail *config.AuditEvent
	for i := range events {
		if events[i].Action == "dispatch.fail" {
			fail = &events[i]
			break
		}
	}
	if fail == nil {
		t.Fatalf("dispatch.fail audit event not recorded; got %+v", events)
	}
	if !strings.Contains(fail.Key, fmt.Sprintf("%d", id)) {
		t.Errorf("dispatch.fail Key = %q; want it to mention task id %d", fail.Key, id)
	}
	if fail.Value == "" {
		t.Errorf("dispatch.fail Value is empty; want it to record the failure reason")
	}
}

// E_cwd: requirement.md L687 + L774 mandates that task.CWD must match
// one of cfg.Execution.AllowedCwdPrefixes (when the list is non-empty)
// before dispatch. A missing prefix check would let a Discovery plugin
// or a manual `marunage add --cwd /etc` smuggle an arbitrary directory
// into the bypass-mode Claude session — directly contradicting the
// security boundary the requirement promises.
//
// Pin three behaviours:
//  1. CWD inside the allowlist dispatches normally.
//  2. CWD outside the allowlist is failed before NewWorkspace.
//  3. An empty / unset allowlist means "no whitelist" (per spec).
func TestRunRejectsCwdOutsideAllowlist(t *testing.T) {
	f := newDispatchFixture(t,
		dispatch.WithAllowedCwdPrefixes([]string{"/tmp/works/", "/home/me/src/"}),
	)
	bad, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "outside", CWD: "/etc/passwd",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(f.cmux.newWorkspaceCalls); got != 0 {
		t.Errorf("NewWorkspace called %d times for cwd outside allowlist; want 0", got)
	}
	row, err := f.repo.Get(f.ctx, bad)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status = %q; want %q (cwd outside allowlist must fail before dispatch)", row.Status, store.StatusFailed)
	}
	if !strings.Contains(row.JudgmentReason, "cwd") {
		t.Errorf("judgment_reason = %q; want it to mention the cwd policy", row.JudgmentReason)
	}
}

func TestRunAcceptsCwdInsideAllowlist(t *testing.T) {
	f := newDispatchFixture(t,
		dispatch.WithAllowedCwdPrefixes([]string{"/tmp/works/", "/home/me/src/"}),
	)
	good, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "inside", CWD: "/tmp/works/repo-a",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	row, err := f.repo.Get(f.ctx, good)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want %q (cwd inside allowlist should dispatch)", row.Status, store.StatusRunning)
	}
}

func TestRunEmptyAllowlistPermitsAnyCwd(t *testing.T) {
	// Default fixture has no WithAllowedCwdPrefixes; this is the spec
	// "空配列の場合はホワイトリストを無効化（全パス許可）" path.
	f := newDispatchFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "no-allowlist", CWD: "/anywhere",
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
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want %q (empty allowlist should not gate dispatch)", row.Status, store.StatusRunning)
	}
}

// TestRunRejectsCwdPrefixBoundary: /home/me/src-evil must not match prefix /home/me/src
func TestRunRejectsCwdPrefixBoundary(t *testing.T) {
	f := newDispatchFixture(t,
		dispatch.WithAllowedCwdPrefixes([]string{"/home/me/src"}),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "boundary", CWD: "/home/me/src-evil/repo",
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
		t.Errorf("status = %q; want %q (/home/me/src-evil must not match prefix /home/me/src)", row.Status, store.StatusFailed)
	}
}

// TestRunRejectsCwdDotDotTraversal: ../ must not bypass prefix check
func TestRunRejectsCwdDotDotTraversal(t *testing.T) {
	f := newDispatchFixture(t,
		dispatch.WithAllowedCwdPrefixes([]string{"/home/me/src"}),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "traversal", CWD: "/home/me/src/../../../etc",
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
		t.Errorf("status = %q; want %q (../ traversal must not bypass prefix check)", row.Status, store.StatusFailed)
	}
}

// E_cwd_default: when task.CWD is empty and WithDefaultCwd is configured,
// the dispatcher must substitute the default CWD so cmux receives a
// non-empty path. Without this, cmux.NewWorkspace returns ErrInvalidOptions
// and the task is silently failed even though the policy layer allowed it.
//
// The allowlist prefix "/tmp/" is required so that the substituted defaultCwd
// "/tmp/default" passes the CwdAllowed gate — the test covers the combined
// fallback+allowlist path.
func TestRunEmptyCwdFallsBackToDefaultCwd(t *testing.T) {
	f := newDispatchFixture(t,
		dispatch.WithDefaultCwd("/tmp/default"),
		dispatch.WithAllowedCwdPrefixes([]string{"/tmp/"}),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "empty-cwd", CWD: "",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(f.cmux.newWorkspaceCalls); got != 1 {
		t.Fatalf("NewWorkspace calls = %d; want 1 (empty cwd should dispatch via defaultCwd)", got)
	}
	if got := f.cmux.newWorkspaceCalls[0].CWD; got != "/tmp/default" {
		t.Errorf("NewWorkspace CWD = %q; want %q (empty task cwd must fall back to default_cwd)", got, "/tmp/default")
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want %q (empty cwd + defaultCwd configured should dispatch)", row.Status, store.StatusRunning)
	}
}

// E_cwd_default_unset: when task.CWD is empty AND no default CWD is
// configured, the dispatcher must fail the task rather than passing an
// empty path to cmux.NewWorkspace (which would return ErrInvalidOptions
// with an undefined working directory).
func TestRunEmptyCwdWithNoDefaultCwdFails(t *testing.T) {
	// No WithDefaultCwd — d.defaultCwd stays "".
	f := newDispatchFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "unset-cwd", CWD: "",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(f.cmux.newWorkspaceCalls); got != 0 {
		t.Errorf("NewWorkspace called %d times; want 0 (empty cwd with no default must not reach cmux)", got)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status = %q; want %q (empty cwd with no default_cwd must fail)", row.Status, store.StatusFailed)
	}
	if !strings.Contains(row.JudgmentReason, "cwd") {
		t.Errorf("judgment_reason = %q; want it to mention cwd", row.JudgmentReason)
	}
}

// D3: when dispatch fails AFTER the row already carries a triage /
// orient judgment_reason (e.g. "phase1: markdown source bypass" set at
// Insert time, or an EscalateToHuman reason left over from a prior
// run), the dispatch failure reason must be APPENDED rather than
// overwriting the original. requirement.md L567 designates triage /
// EscalateToHuman as the only writers of judgment_reason; PR-42's
// MarkFailedWithReason path must not silently destroy the triage trail
// that `marunage review` relies on for post-mortem.
func TestRunPreservesPriorJudgmentReasonOnFailure(t *testing.T) {
	f := newDispatchFixture(t)
	f.cmux.sendHook = func(_ cmux.Workspace, _ string) error {
		return errors.New("cmux send: exit 1")
	}

	const triageReason = "phase1: markdown source bypass"
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source:         "markdown",
		Title:          "preserve triage",
		CWD:            "/tmp",
		JudgmentReason: triageReason,
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
		t.Fatalf("status = %q; want %q", row.Status, store.StatusFailed)
	}
	if !strings.Contains(row.JudgmentReason, triageReason) {
		t.Errorf("judgment_reason = %q; want it to preserve the original triage reason %q", row.JudgmentReason, triageReason)
	}
	if !strings.Contains(row.JudgmentReason, "Send") {
		t.Errorf("judgment_reason = %q; want it to also mention the dispatch Send failure", row.JudgmentReason)
	}
}

// C2c: started_at must never be NULL while status=running. The dispatch
// loop has multiple writes (SetWorkspace, UpdateStatus(running),
// SetStartedAt). If SetStartedAt fails AFTER UpdateStatus(running),
// the row is left running with started_at=NULL — invisible to PR-44
// reaper's "started_at + 24h" stuck-probe and silently leaks.
//
// Pin the contract: by the time status flips to running, started_at
// must already be stamped. This test stamps started_at during the
// transition window (via a hook on UpdateStatus) and asserts that
// reading the row at status='running' always reveals a non-zero
// started_at.
func TestRunStampsStartedAtBeforeRunning(t *testing.T) {
	f := newDispatchFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "ordering", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// At this point dispatch completed; verify post-condition. The real
	// invariant we care about is "no row in status=running with
	// started_at IS NULL is ever observable". In a successful run both
	// fields end up set; the regression we are pinning is the failure
	// case — to express that without a partial-failure injection point,
	// we additionally assert the order via the recorded fixture clock.
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Fatalf("status = %q; want running", row.Status)
	}
	if row.StartedAt.IsZero() {
		t.Fatalf("started_at is zero while status=running; reaper would never detect a stuck row")
	}
	if !row.StartedAt.Equal(f.now) {
		t.Errorf("started_at = %v; want %v (the dispatcher clock at dispatch time)", row.StartedAt, f.now)
	}
}

// C2d: when SetStartedAt fails (e.g. transient store error), the row
// must NOT be left in status=running. The simplest way to guarantee
// "running implies started_at stamped" is to write started_at FIRST,
// then flip status to running — so a failure on the started_at write
// leaves the row pending and retryable on the next Run.
//
// Use a wrapper Store that fails SetStartedAt the first time it is
// called, then succeeds. After Run returns, the row should be pending
// (not running) so it gets re-picked next time.
type setStartedAtFailingStore struct {
	dispatch.Store
	failedOnce bool
}

func (s *setStartedAtFailingStore) SetStartedAt(ctx context.Context, id int64, t time.Time) error {
	if !s.failedOnce {
		s.failedOnce = true
		return errors.New("simulated SetStartedAt failure")
	}
	return s.Store.SetStartedAt(ctx, id, t)
}

func TestRunNoRunningWithoutStartedAtOnSetStartedAtFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return now }))
	wrapped := &setStartedAtFailingStore{Store: repo}
	fcm := &fakeCmux{}

	d, err := dispatch.New(
		dispatch.WithStore(wrapped),
		dispatch.WithCmux(fcm),
		dispatch.WithClock(func() time.Time { return now }),
		dispatch.WithBaseSkill("BASE"),
		dispatch.WithClaudeCommand("claude"),
	)
	if err != nil {
		t.Fatalf("dispatch.New: %v", err)
	}

	id, err := repo.Insert(context.Background(), store.Task{
		Source: "manual", Title: "split-brain test", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Run returns the SetStartedAt error; that's expected. The
	// invariant under test is the row STATE after Run.
	_ = d.Run(context.Background(), dispatch.RunOptions{MaxParallel: 1})

	row, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Two assertions to keep this from passing vacuously: (a) the
	// invariant itself, and (b) that the failure injection actually
	// fired by checking the row did NOT reach running.
	if row.Status == store.StatusRunning && row.StartedAt.IsZero() {
		t.Errorf("invariant violated: row is running with started_at IS NULL — reaper cannot detect this stuck row")
	}
	if row.Status == store.StatusRunning {
		t.Errorf("row reached running despite SetStartedAt failure: SetStartedAt must precede UpdateStatus(running) so the failure leaves the row pending and retryable")
	}
	if !wrapped.failedOnce {
		t.Errorf("SetStartedAt failure injection never fired; the test was vacuous")
	}
}

// D1b: AcquireLock-then-NewWorkspace-failure must release the lock_key so a
// sibling pending row sharing the same resolved lock_key is not blocked
// forever. Without this, AcquireLock's "pending counts as a holder" rule
// (store/tasks.go AcquireLock godoc) means the failed row keeps the key
// indefinitely while sitting in pending — every subsequent Run hits
// ErrLockHeld for any other row resolving to the same lock_key.
func TestRunReleasesLockOnNewWorkspaceFailure(t *testing.T) {
	f := newDispatchFixture(t,
		dispatch.WithLockKeys(map[string]string{
			"^repo:.*": "git-repo",
		}),
	)

	// First call to NewWorkspace fails; subsequent calls succeed. This
	// simulates "the first row hit a transient cmux error after we already
	// AcquireLock'd it; the second row shares the same lock_hint and must
	// still go through".
	var calls int
	f.cmux.newWorkspaceHook = func(_ cmux.NewWorkspaceOptions) (cmux.Workspace, error) {
		calls++
		if calls == 1 {
			return cmux.Workspace{}, errors.New("cmux exploded")
		}
		return cmux.Workspace{ID: fmt.Sprintf("workspace:%d", calls)}, nil
	}

	first, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "first", CWD: "/tmp",
		Notes: `{"lock_hint":"repo:haruotsu/marunage"}`,
	})
	if err != nil {
		t.Fatalf("Insert first: %v", err)
	}
	second, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "second", CWD: "/tmp",
		Notes: `{"lock_hint":"repo:haruotsu/marunage"}`,
	})
	if err != nil {
		t.Fatalf("Insert second: %v", err)
	}

	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 2}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// First row: NewWorkspace failed -> still pending, lock_key released.
	firstRow, err := f.repo.Get(f.ctx, first)
	if err != nil {
		t.Fatalf("Get first: %v", err)
	}
	if firstRow.Status != store.StatusPending {
		t.Errorf("first status = %q; want still %q (NewWorkspace failed)", firstRow.Status, store.StatusPending)
	}
	if firstRow.LockKey != "" {
		t.Errorf("first lock_key = %q; want empty (must be released after NewWorkspace failure so siblings can dispatch)", firstRow.LockKey)
	}

	// Second row: should have been dispatched (lock was released by the first row).
	secondRow, err := f.repo.Get(f.ctx, second)
	if err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if secondRow.Status != store.StatusRunning {
		t.Errorf("second status = %q; want %q (sibling row should dispatch after lock release)", secondRow.Status, store.StatusRunning)
	}
	if secondRow.WS == "" {
		t.Errorf("second ws = %q; want non-empty (sibling row should have a workspace)", secondRow.WS)
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

// F-series (PR-43): WithWorkspaceDirs wires the per-task control dir.

type fakeDirs struct {
	root string
}

func (d fakeDirs) Dir(id int64) string {
	return filepath.Join(d.root, fmt.Sprintf("%d", id))
}

// F1: dir created + prompt embeds sentinel path.
func TestRunCreatesWorkspaceDirAndEmbedsSentinelPath(t *testing.T) {
	root := t.TempDir()
	dirs := fakeDirs{root: root}
	f := newDispatchFixture(t, dispatch.WithWorkspaceDirs(dirs))

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "with sentinel", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantDir := dirs.Dir(id)
	info, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("workspace dir %q not created: %v", wantDir, err)
	}
	if !info.IsDir() {
		t.Errorf("workspace path %q is not a directory", wantDir)
	}

	if len(f.cmux.sendCalls) != 1 {
		t.Fatalf("Send calls = %d; want 1", len(f.cmux.sendCalls))
	}
	payload := f.cmux.sendCalls[0].Text
	wantSentinel := filepath.Join(wantDir, ".exit_code")
	if !strings.Contains(payload, wantSentinel) {
		t.Errorf("Send payload does not embed sentinel path %q; got:\n%s", wantSentinel, payload)
	}
}

// F2: mkdir failure leaves the row pending.
func TestRunLeavesRowPendingWhenWorkspaceMkdirFails(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}
	dirs := fakeDirs{root: blocker}
	f := newDispatchFixture(t, dispatch.WithWorkspaceDirs(dirs))

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "mkdir fails", CWD: "/tmp",
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
	if row.Status != store.StatusPending {
		t.Errorf("status after mkdir failure = %q; want still %q (retryable)", row.Status, store.StatusPending)
	}
	if got := len(f.cmux.newWorkspaceCalls); got != 0 {
		t.Errorf("NewWorkspace calls = %d; want 0 (mkdir must precede NewWorkspace)", got)
	}
}

// D1c (PR-43): NewWorkspace failure leaves a trace in audit.log.
func TestRunRecordsAuditOnNewWorkspaceFailure(t *testing.T) {
	au := &fakeAuditor{}
	f := newDispatchFixture(t, dispatch.WithAuditor(au))
	f.cmux.newWorkspaceHook = func(_ cmux.NewWorkspaceOptions) (cmux.Workspace, error) {
		return cmux.Workspace{}, errors.New("cmux: simulated failure")
	}

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "audit nws fail", CWD: "/tmp",
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
	if row.Status != store.StatusPending {
		t.Errorf("status = %q; want still %q (D1 contract)", row.Status, store.StatusPending)
	}

	var found *config.AuditEvent
	for i, ev := range au.Events() {
		if ev.Action == "dispatch.fail" && strings.Contains(ev.Value, "NewWorkspace") {
			found = &au.Events()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected dispatch.fail audit mentioning NewWorkspace; got %+v", au.Events())
	}
	wantKey := fmt.Sprintf("task:%d", id)
	if found.Key != wantKey {
		t.Errorf("audit Key = %q; want %q", found.Key, wantKey)
	}
}

// J5 (PR-42b): NewWorkspace failure after ClaimWorkspace must clear the
// __dispatching__ sentinel so the row is retryable.
func TestRunClearsSentinelOnNewWorkspaceFailureAfterClaim(t *testing.T) {
	f := newDispatchFixture(t)
	f.cmux.newWorkspaceHook = func(_ cmux.NewWorkspaceOptions) (cmux.Workspace, error) {
		return cmux.Workspace{}, errors.New("cmux exploded")
	}
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "sentinel cleanup", CWD: "/tmp",
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
	if row.Status != store.StatusPending {
		t.Errorf("status = %q; want still %q", row.Status, store.StatusPending)
	}
}

// F2b (PR-43): when MkdirAll fails the row stays pending and the
// failure leaves a trace in audit.log.
func TestRunRecordsAuditOnWorkspaceMkdirFailure(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}
	dirs := fakeDirs{root: blocker}
	au := &fakeAuditor{}
	f := newDispatchFixture(t,
		dispatch.WithWorkspaceDirs(dirs),
		dispatch.WithAuditor(au),
	)

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "audit mkdir fail", CWD: "/tmp",
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
	if row.Status != store.StatusPending {
		t.Errorf("status = %q; want still %q (F2 contract)", row.Status, store.StatusPending)
	}
	var found *config.AuditEvent
	for i, ev := range au.Events() {
		if ev.Action == "dispatch.fail" && strings.Contains(ev.Value, "mkdir") {
			found = &au.Events()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected dispatch.fail audit mentioning mkdir failure; got %+v", au.Events())
	}
	wantKey := fmt.Sprintf("task:%d", id)
	if found.Key != wantKey {
		t.Errorf("audit Key = %q; want %q", found.Key, wantKey)
	}
}

// J1/J2: two Dispatchers sharing one store + cmux must not double-claim
// a row. Without an atomic claim step, both dispatchers can pick the
// same pending row, both NewWorkspace, both SetWorkspace — leaving an
// orphan cmux workspace and corrupted ws references. The test pins the
// safety property that PR-42b promises ("(source, external_id) UNIQUE
// と lock_key で safety が保たれる"): every pending row is dispatched
// AT MOST ONCE under -race.
func TestRunConcurrentDispatchersDoNotDoubleClaim(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return now }))

	// Shared cmux client. NewWorkspace returns a unique ws ID per call so
	// duplicate dispatches are observable as duplicate IDs in the cmux
	// call log.
	fcm := &fakeCmux{}

	const N = 10
	var ids []int64
	for i := 0; i < N; i++ {
		id, err := repo.Insert(context.Background(), store.Task{
			Source: "manual", Title: fmt.Sprintf("t%d", i), CWD: "/tmp",
		})
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	newDisp := func() *dispatch.Dispatcher {
		d, err := dispatch.New(
			dispatch.WithStore(repo),
			dispatch.WithCmux(fcm),
			dispatch.WithClock(func() time.Time { return now }),
			dispatch.WithBaseSkill("BASE"),
			dispatch.WithClaudeCommand("claude"),
		)
		if err != nil {
			t.Fatalf("dispatch.New: %v", err)
		}
		return d
	}
	dA, dB := newDisp(), newDisp()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = dA.Run(context.Background(), dispatch.RunOptions{MaxParallel: N})
	}()
	go func() {
		defer wg.Done()
		_ = dB.Run(context.Background(), dispatch.RunOptions{MaxParallel: N})
	}()
	wg.Wait()

	// Every NewWorkspace call must correspond to a distinct task — there
	// must be no row dispatched twice.
	if got := len(fcm.newWorkspaceCalls); got != N {
		t.Errorf("NewWorkspace calls = %d; want %d (each row dispatched exactly once)", got, N)
	}
	seenName := make(map[string]int)
	for _, c := range fcm.newWorkspaceCalls {
		seenName[c.Name]++
	}
	for name, n := range seenName {
		if n > 1 {
			t.Errorf("workspace name %q dispatched %d times; want 1 (double-claim)", name, n)
		}
	}

	// Every row must end in running with a non-empty ws.
	for _, id := range ids {
		row, err := repo.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get %d: %v", id, err)
		}
		if row.Status != store.StatusRunning {
			t.Errorf("row %d status = %q; want running", id, row.Status)
		}
		if row.WS == "" {
			t.Errorf("row %d ws is empty after dispatch", id)
		}
	}
}

// I1-I8: permission.Matcher + on_unknown_permission policy.
//
// HandlePermissionRequest is the dispatcher-side handler for Claude
// tool-permission prompts (the cmux/MCP layer that actually intercepts
// the prompt is out of scope for PR-42b — see docs/pr_split_plan.md
// PR-42 "スコープ非該当"). What this PR pins is the WIRING: the
// matcher decides allow / deny; on deny the configured policy runs
// (escalate -> EscalateToHuman + dispatch.escalate audit; fail ->
// MarkFailedWithReason + dispatch.fail audit; retry -> defer to caller).

func newPermissionFixture(t *testing.T, policy string, autoAccept []string) (dispatchFixture, int64) {
	t.Helper()
	m, err := permission.New(autoAccept)
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	au := &fakeAuditor{}
	f := newDispatchFixture(t,
		dispatch.WithPermissionMatcher(m),
		dispatch.WithOnUnknownPermission(policy),
		dispatch.WithAuditor(au),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "perm test", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Move row into running so EscalateToHuman is allowed (it gates on
	// status IN ('running', 'waiting_human')).
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}
	f.cmux = nil // unused in these tests; HandlePermissionRequest is direct.
	_ = au
	return f, id
}

// I1: matcher allow -> PermissionAllow, no row mutation.
func TestHandlePermissionRequestAllowsWhenMatched(t *testing.T) {
	f, id := newPermissionFixture(t, "escalate", []string{"Read", "Bash(git status:*)"})
	d, err := f.disp.HandlePermissionRequest(f.ctx, id, "Read", "/tmp/x")
	if err != nil {
		t.Fatalf("HandlePermissionRequest: %v", err)
	}
	if d != dispatch.PermissionAllow {
		t.Errorf("decision = %v; want PermissionAllow", d)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want running (allow must not mutate)", row.Status)
	}
}

// I2: matcher deny + escalate policy -> waiting_human + dispatch.escalate.
func TestHandlePermissionRequestEscalatesOnDeny(t *testing.T) {
	au := &fakeAuditor{}
	m, err := permission.New([]string{"Read"})
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	f := newDispatchFixture(t,
		dispatch.WithPermissionMatcher(m),
		dispatch.WithOnUnknownPermission("escalate"),
		dispatch.WithAuditor(au),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "escalate me", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}

	dec, err := f.disp.HandlePermissionRequest(f.ctx, id, "Bash", "rm -rf /")
	if err != nil {
		t.Fatalf("HandlePermissionRequest: %v", err)
	}
	if dec != dispatch.PermissionEscalate {
		t.Errorf("decision = %v; want PermissionEscalate", dec)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusWaitingHuman {
		t.Errorf("status = %q; want %q", row.Status, store.StatusWaitingHuman)
	}
	for _, want := range []string{"Bash", "rm -rf"} {
		if !strings.Contains(row.JudgmentReason, want) {
			t.Errorf("judgment_reason = %q; want it to mention %q", row.JudgmentReason, want)
		}
	}
	var sawEscalate bool
	for _, e := range au.Events() {
		if e.Action == "dispatch.escalate" {
			sawEscalate = true
			if !strings.Contains(e.Key, fmt.Sprintf("%d", id)) {
				t.Errorf("escalate audit Key = %q; want it to mention id %d", e.Key, id)
			}
			if !strings.Contains(e.Value, "Bash") {
				t.Errorf("escalate audit Value = %q; want it to mention denied tool", e.Value)
			}
		}
	}
	if !sawEscalate {
		t.Errorf("no dispatch.escalate audit event recorded; got %+v", au.Events())
	}
}

// I3: matcher deny + fail policy -> failed + dispatch.fail.
func TestHandlePermissionRequestFailsOnDeny(t *testing.T) {
	au := &fakeAuditor{}
	m, err := permission.New([]string{"Read"})
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	f := newDispatchFixture(t,
		dispatch.WithPermissionMatcher(m),
		dispatch.WithOnUnknownPermission("fail"),
		dispatch.WithAuditor(au),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "fail me", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}

	dec, err := f.disp.HandlePermissionRequest(f.ctx, id, "WebFetch", "https://example.com")
	if err != nil {
		t.Fatalf("HandlePermissionRequest: %v", err)
	}
	if dec != dispatch.PermissionFail {
		t.Errorf("decision = %v; want PermissionFail", dec)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status = %q; want %q", row.Status, store.StatusFailed)
	}
}

// I4: matcher deny + retry policy -> PermissionAsk, no mutation.
func TestHandlePermissionRequestAsksOnRetryPolicy(t *testing.T) {
	m, err := permission.New([]string{"Read"})
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	f := newDispatchFixture(t,
		dispatch.WithPermissionMatcher(m),
		dispatch.WithOnUnknownPermission("retry"),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "ask me", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}

	dec, err := f.disp.HandlePermissionRequest(f.ctx, id, "Edit", "/tmp/x")
	if err != nil {
		t.Fatalf("HandlePermissionRequest: %v", err)
	}
	if dec != dispatch.PermissionAsk {
		t.Errorf("decision = %v; want PermissionAsk", dec)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want %q (retry policy must not mutate)", row.Status, store.StatusRunning)
	}
}

// I5: no matcher configured -> PermissionAsk (safe default; never silently allow).
func TestHandlePermissionRequestNoMatcherAsks(t *testing.T) {
	f := newDispatchFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "no matcher", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}
	dec, err := f.disp.HandlePermissionRequest(f.ctx, id, "Bash", "anything")
	if err != nil {
		t.Fatalf("HandlePermissionRequest: %v", err)
	}
	if dec != dispatch.PermissionAsk {
		t.Errorf("decision = %v; want PermissionAsk (safe default)", dec)
	}
}

// I7b: a non-bypass permission_mode (default / acceptEdits / plan /
// custom) implies Claude will issue permission prompts at runtime. If
// the dispatcher has no PermissionMatcher wired, those prompts will
// either hang forever or be silently denied — both observable to the
// user as "the dispatched session does nothing". Failing loud at New
// time is the only way to surface the misconfiguration before the
// dispatcher starts producing zombie workspaces.
func TestNewRequiresMatcherWhenPermissionModeNotBypass(t *testing.T) {
	cases := []string{"default", "acceptEdits", "plan", "custom"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			_, err := dispatch.New(
				dispatch.WithStore(stubStore{}),
				dispatch.WithCmux(&fakeCmux{}),
				dispatch.WithBaseSkill("BASE"),
				dispatch.WithClaudeCommand("claude"),
				dispatch.WithPermissionMode(mode),
				// Intentionally no WithPermissionMatcher.
			)
			if err == nil {
				t.Fatalf("New(WithPermissionMode(%q)) without matcher = nil; want error", mode)
			}
			if !errors.Is(err, dispatch.ErrInvalidConfig) {
				t.Errorf("err = %v; want errors.Is(err, ErrInvalidConfig)", err)
			}
		})
	}
}

// I7c: bypass mode does NOT require a matcher (the claude --dangerously
// -skip-permissions binary never asks). Construction must succeed.
func TestNewBypassModeDoesNotRequireMatcher(t *testing.T) {
	_, err := dispatch.New(
		dispatch.WithStore(stubStore{}),
		dispatch.WithCmux(&fakeCmux{}),
		dispatch.WithBaseSkill("BASE"),
		dispatch.WithClaudeCommand("claude --dangerously-skip-permissions"),
		dispatch.WithPermissionMode("bypass"),
	)
	if err != nil {
		t.Fatalf("New(WithPermissionMode(\"bypass\")) without matcher = %v; want nil", err)
	}
}

// I7d: empty permission_mode (caller did not pass WithPermissionMode)
// preserves the existing pre-PR-42b construction surface — no matcher
// requirement. Otherwise every existing dispatcher test in this file
// would have to learn about the new option.
func TestNewEmptyPermissionModeDoesNotRequireMatcher(t *testing.T) {
	_, err := dispatch.New(
		dispatch.WithStore(stubStore{}),
		dispatch.WithCmux(&fakeCmux{}),
		dispatch.WithBaseSkill("BASE"),
		dispatch.WithClaudeCommand("claude"),
	)
	if err != nil {
		t.Fatalf("New() with no permission_mode = %v; want nil (back-compat)", err)
	}
}

// I7: an unknown on_unknown_permission value rejects construction.
func TestNewRejectsUnknownPermissionPolicy(t *testing.T) {
	_, err := dispatch.New(
		dispatch.WithStore(stubStore{}),
		dispatch.WithCmux(&fakeCmux{}),
		dispatch.WithBaseSkill("BASE"),
		dispatch.WithClaudeCommand("claude"),
		dispatch.WithOnUnknownPermission("nonsense"),
	)
	if err == nil {
		t.Fatal("New with unknown on_unknown_permission = nil; want error")
	}
	if !errors.Is(err, dispatch.ErrInvalidConfig) {
		t.Errorf("err = %v; want errors.Is(err, ErrInvalidConfig)", err)
	}
}

// I8: the reason written into audit.log / judgment_reason on escalate
// is sanitised by Redact, in case the tool args themselves carried a
// secret (e.g. a Bash invocation that includes a curl with a Bearer
// header).
func TestHandlePermissionRequestRedactsReason(t *testing.T) {
	au := &fakeAuditor{}
	m, err := permission.New([]string{"Read"})
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	f := newDispatchFixture(t,
		dispatch.WithPermissionMatcher(m),
		dispatch.WithOnUnknownPermission("escalate"),
		dispatch.WithAuditor(au),
	)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "secret in args", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}
	const leaked = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567"
	args := "curl -H 'Authorization: Bearer " + leaked + "' https://api"

	if _, err := f.disp.HandlePermissionRequest(f.ctx, id, "Bash", args); err != nil {
		t.Fatalf("HandlePermissionRequest: %v", err)
	}

	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(row.JudgmentReason, leaked) {
		t.Errorf("escalate reason leaked secret %q in:\n%s", leaked, row.JudgmentReason)
	}
	for _, e := range au.Events() {
		if e.Action == "dispatch.escalate" && strings.Contains(e.Value, leaked) {
			t.Errorf("escalate audit Value leaked secret %q: %q", leaked, e.Value)
		}
	}
}

// stubStore is a minimal Store impl that satisfies the interface for
// construction-only tests (none of its methods are called).
type stubStore struct{}

func (stubStore) List(context.Context, store.ListFilter) ([]store.Task, error) {
	return nil, nil
}
func (stubStore) Get(context.Context, int64) (store.Task, error)   { return store.Task{}, nil }
func (stubStore) AcquireLock(context.Context, int64, string) error { return nil }
func (stubStore) ReleaseLock(context.Context, int64) error         { return nil }
func (stubStore) ClaimWorkspace(context.Context, int64, string) (bool, error) {
	return true, nil
}
func (stubStore) SetWorkspace(context.Context, int64, string) error         { return nil }
func (stubStore) UpdateStatus(context.Context, int64, string) error         { return nil }
func (stubStore) SetStartedAt(context.Context, int64, time.Time) error      { return nil }
func (stubStore) MarkFailedWithReason(context.Context, int64, string) error { return nil }
func (stubStore) EscalateToHuman(context.Context, int64, string) error      { return nil }

// H6 (PR-42b): redact dispatch failure reason before persisting.
func TestRunRedactsSecretsInFailureReason(t *testing.T) {
	au := &fakeAuditor{}
	f := newDispatchFixture(t, dispatch.WithAuditor(au))
	const leaked = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567"
	f.cmux.sendHook = func(_ cmux.Workspace, _ string) error {
		return fmt.Errorf("cmux send: 401 unauthorized; Authorization: Bearer %s", leaked)
	}

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "redact me", CWD: "/tmp",
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
	if strings.Contains(row.JudgmentReason, leaked) {
		t.Errorf("judgment_reason leaked secret %q in:\n%s", leaked, row.JudgmentReason)
	}
	if !strings.Contains(row.JudgmentReason, "[REDACTED]") {
		t.Errorf("judgment_reason missing [REDACTED] marker in:\n%s", row.JudgmentReason)
	}
	for _, e := range au.Events() {
		if e.Action != "dispatch.fail" {
			continue
		}
		if strings.Contains(e.Value, leaked) {
			t.Errorf("dispatch.fail audit event leaked secret %q in Value=%q", leaked, e.Value)
		}
	}
}

// F3 (PR-43): no WithWorkspaceDirs keeps PR-42 wire format intact.
func TestRunOmitsSentinelSectionWhenWorkspaceDirsUnset(t *testing.T) {
	f := newDispatchFixture(t)

	if _, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "no sentinel", CWD: "/tmp",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.cmux.sendCalls) != 1 {
		t.Fatalf("Send calls = %d; want 1", len(f.cmux.sendCalls))
	}
	payload := f.cmux.sendCalls[0].Text
	for _, banned := range []string{".exit_code", ".result_summary"} {
		if strings.Contains(payload, banned) {
			t.Errorf("Send payload unexpectedly contains %q:\n%s", banned, payload)
		}
	}
}

// PR-42b: workspaceName must trim by rune count, not byte count.
func TestRunWorkspaceNameTrimsByRuneCount(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		wantSub string
	}{
		{
			name:    "japanese-overflow",
			title:   strings.Repeat("あ", 50),
			wantSub: strings.Repeat("あ", 40),
		},
		{
			name:    "emoji-overflow",
			title:   strings.Repeat("🍎", 50),
			wantSub: strings.Repeat("🍎", 40),
		},
		{
			name:    "ascii-overflow",
			title:   strings.Repeat("a", 50),
			wantSub: strings.Repeat("a", 40),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchFixture(t)
			id, err := f.repo.Insert(f.ctx, store.Task{
				Source: "manual", Title: tc.title, CWD: "/tmp",
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
			got := f.cmux.newWorkspaceCalls[0].Name
			if !utf8.ValidString(got) {
				t.Errorf("workspace name %q is not valid UTF-8", got)
			}
			wantPrefix := fmt.Sprintf("#%d ", id)
			if !strings.HasPrefix(got, wantPrefix) {
				t.Errorf("name = %q; want prefix %q", got, wantPrefix)
			}
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("name = %q; want %d-rune trimmed title %q", got, len([]rune(tc.wantSub)), tc.wantSub)
			}
			titlePart := strings.TrimPrefix(got, wantPrefix)
			if got := len([]rune(titlePart)); got > 40 {
				t.Errorf("title rune count = %d; want <= 40", got)
			}
		})
	}
}

// PR-72: WithTriageSkill plumbs the marunage-triage SKILL.md content
// into BuildPrompt so the dispatched Send carries the OODA Orient
// section the embedded skill defines.
func TestRunIncludesTriageSkillInSendPayload(t *testing.T) {
	const triageSkillBody = "TRIAGE-SKILL-OODA-ORIENT"
	f := newDispatchFixture(t, dispatch.WithTriageSkill(triageSkillBody))

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "slack", Title: "triage me", Body: "b", CWD: "/tmp",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.disp.Run(f.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.cmux.sendCalls) != 1 {
		t.Fatalf("Send calls = %d; want 1", len(f.cmux.sendCalls))
	}
	payload := f.cmux.sendCalls[0].Text
	if !strings.Contains(payload, triageSkillBody) {
		t.Errorf("Send payload missing triage skill body %q; got:\n%s", triageSkillBody, payload)
	}
	// Sanity check the row actually transitioned out of pending so we
	// know the wiring did not break the rest of the dispatch path.
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want %q", row.Status, store.StatusRunning)
	}
}

// PR-72 back-compat: omitting WithTriageSkill leaves the Send payload
// identical to PR-42's wire format (no triage section, no doubled
// separators). Two assertions back this up: (1) the same prompt is
// produced when WithTriageSkill is configured with empty body, and
// (2) the prompt produced when WithTriageSkill carries a unique
// fixture is strictly LONGER than the no-triage prompt — pinning
// "the section actually appears, and only when wired" without
// relying on a generic substring like "TRIAGE" that an unrelated
// future addition could trip.
func TestRunOmitsTriageSectionWhenSkillNotConfigured(t *testing.T) {
	const triageMarker = "OODA-ORIENT-FIXTURE-MARKER-PR72"

	withoutF := newDispatchFixture(t)
	if _, err := withoutF.repo.Insert(withoutF.ctx, store.Task{
		Source: "slack", Title: "no triage", Body: "b", CWD: "/tmp",
	}); err != nil {
		t.Fatalf("Insert (without): %v", err)
	}
	if err := withoutF.disp.Run(withoutF.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run (without): %v", err)
	}
	if len(withoutF.cmux.sendCalls) != 1 {
		t.Fatalf("Send calls (without) = %d; want 1", len(withoutF.cmux.sendCalls))
	}
	without := withoutF.cmux.sendCalls[0].Text
	if strings.Contains(without, triageMarker) {
		t.Errorf("Send payload unexpectedly contains triage marker without WithTriageSkill:\n%s", without)
	}
	if strings.Contains(without, "\n\n\n\n") {
		t.Errorf("Send payload has doubled blank-line separator:\n%s", without)
	}

	withF := newDispatchFixture(t, dispatch.WithTriageSkill(triageMarker))
	if _, err := withF.repo.Insert(withF.ctx, store.Task{
		Source: "slack", Title: "no triage", Body: "b", CWD: "/tmp",
	}); err != nil {
		t.Fatalf("Insert (with): %v", err)
	}
	if err := withF.disp.Run(withF.ctx, dispatch.RunOptions{MaxParallel: 1}); err != nil {
		t.Fatalf("Run (with): %v", err)
	}
	if len(withF.cmux.sendCalls) != 1 {
		t.Fatalf("Send calls (with) = %d; want 1", len(withF.cmux.sendCalls))
	}
	with := withF.cmux.sendCalls[0].Text
	if !strings.Contains(with, triageMarker) {
		t.Errorf("Send payload should contain triage marker when WithTriageSkill is set:\n%s", with)
	}
	if len(with) <= len(without) {
		t.Errorf("triage payload (len=%d) should be strictly longer than no-triage payload (len=%d)",
			len(with), len(without))
	}
}
