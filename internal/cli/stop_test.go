package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/store"
)

// fakeStopStore is an in-memory StopStore for stop command tests.
type fakeStopStore struct {
	mu          sync.Mutex
	rows        map[int64]store.Task
	failReasons map[int64]string
}

func newFakeStopStore() *fakeStopStore {
	return &fakeStopStore{
		rows:        make(map[int64]store.Task),
		failReasons: make(map[int64]string),
	}
}

func (f *fakeStopStore) List(_ context.Context, filter store.ListFilter) ([]store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Task
	for _, t := range f.rows {
		if len(filter.Statuses) > 0 && !contains(filter.Statuses, t.Status) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeStopStore) Get(_ context.Context, id int64) (store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.rows[id]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeStopStore) MarkFailedWithReason(_ context.Context, id int64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.rows[id]
	if !ok {
		return store.ErrNotFound
	}
	f.failReasons[id] = reason
	t.Status = store.StatusFailed
	t.JudgmentReason = reason
	f.rows[id] = t
	return nil
}

// fakeWorkspaceStopper records Stop calls.
type fakeWorkspaceStopper struct {
	mu      sync.Mutex
	stopped []string
	stopErr error
}

func (f *fakeWorkspaceStopper) Stop(_ context.Context, workspaceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, workspaceID)
	return f.stopErr
}

// fakeStopAuditor records audit events.
type fakeStopAuditor struct {
	mu     sync.Mutex
	events []config.AuditEvent
}

func (a *fakeStopAuditor) Record(e config.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

// installFakeStopDeps wires fake stop dependencies and returns them.
func installFakeStopDeps(t *testing.T) (*fakeStopStore, *fakeWorkspaceStopper, *fakeStopAuditor) {
	t.Helper()
	ss := newFakeStopStore()
	ws := &fakeWorkspaceStopper{}
	au := &fakeStopAuditor{}
	withStopDepsFactory(t, func(_ context.Context, _ string) (StopStore, WorkspaceStopper, config.Auditor, func() error, error) {
		return ss, ws, au, func() error { return nil }, nil
	})
	return ss, ws, au
}

// TestStop_NeitherFlagReturnsError: no --all or --task → error
func TestStop_NeitherFlagReturnsError(t *testing.T) {
	installFakeStopDeps(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit=0; want non-zero when neither --all nor --task given")
	}
}

// TestStop_All_StopsAllRunningTasks: --all stops every running task
func TestStop_All_StopsAllRunningTasks(t *testing.T) {
	ss, ws, _ := installFakeStopDeps(t)
	ss.rows[1] = store.Task{ID: 1, Source: "manual", Title: "a", Status: store.StatusRunning, WS: "workspace:1"}
	ss.rows[2] = store.Task{ID: 2, Source: "manual", Title: "b", Status: store.StatusRunning, WS: "workspace:2"}
	ss.rows[3] = store.Task{ID: 3, Source: "manual", Title: "c", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop", "--all"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d; stderr=%q", code, stderr.String())
	}
	if ss.rows[1].Status != store.StatusFailed {
		t.Errorf("task 1 status = %q; want failed", ss.rows[1].Status)
	}
	if ss.rows[2].Status != store.StatusFailed {
		t.Errorf("task 2 status = %q; want failed", ss.rows[2].Status)
	}
	if ss.rows[3].Status != store.StatusPending {
		t.Errorf("task 3 status = %q; want pending (untouched)", ss.rows[3].Status)
	}
	if !strings.Contains(stdout.String(), "2") {
		t.Errorf("stdout should report count; got %q", stdout.String())
	}
	// cmux Stop was called for the two running tasks
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if len(ws.stopped) != 2 {
		t.Errorf("workspace Stop calls = %d; want 2", len(ws.stopped))
	}
}

// TestStop_Task_StopsSpecificRunningTask: --task <id> stops a single task
func TestStop_Task_StopsSpecificRunningTask(t *testing.T) {
	ss, ws, _ := installFakeStopDeps(t)
	ss.rows[5] = store.Task{ID: 5, Source: "manual", Title: "x", Status: store.StatusRunning, WS: "workspace:5"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop", "--task", "5"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d; stderr=%q", code, stderr.String())
	}
	if ss.rows[5].Status != store.StatusFailed {
		t.Errorf("task 5 status = %q; want failed", ss.rows[5].Status)
	}
	if ss.failReasons[5] != stopReason {
		t.Errorf("judgment_reason = %q; want %q", ss.failReasons[5], stopReason)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if len(ws.stopped) != 1 || ws.stopped[0] != "workspace:5" {
		t.Errorf("stopped workspaces = %v; want [workspace:5]", ws.stopped)
	}
}

// TestStop_Task_NotFound: --task <id> for missing task → exit 1
func TestStop_Task_NotFound(t *testing.T) {
	installFakeStopDeps(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop", "--task", "999"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit=0; want non-zero for not-found task")
	}
	if !strings.Contains(stderr.String(), "999") {
		t.Errorf("stderr = %q; want mention of id 999", stderr.String())
	}
}

// TestStop_Task_NotRunning: --task <id> for non-running task → error
func TestStop_Task_NotRunning(t *testing.T) {
	ss, _, _ := installFakeStopDeps(t)
	ss.rows[7] = store.Task{ID: 7, Source: "manual", Title: "done task", Status: store.StatusDone}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop", "--task", "7"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit=0; want non-zero for non-running task")
	}
	// task must remain unchanged
	if ss.rows[7].Status != store.StatusDone {
		t.Errorf("task status changed; got %q; want done", ss.rows[7].Status)
	}
}

// TestStop_All_NoRunningTasks: --all with no running tasks → 0 stopped
func TestStop_All_NoRunningTasks(t *testing.T) {
	ss, _, _ := installFakeStopDeps(t)
	ss.rows[1] = store.Task{ID: 1, Source: "manual", Title: "done", Status: store.StatusDone}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop", "--all"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0") {
		t.Errorf("stdout should mention 0 stopped; got %q", stdout.String())
	}
}

// TestStop_RecordsAuditEvent: stop records action="task.stop" in audit log
func TestStop_RecordsAuditEvent(t *testing.T) {
	ss, _, au := installFakeStopDeps(t)
	ss.rows[3] = store.Task{ID: 3, Source: "manual", Title: "audit-test", Status: store.StatusRunning, WS: "workspace:3"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop", "--task", "3"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d; stderr=%q", code, stderr.String())
	}
	au.mu.Lock()
	defer au.mu.Unlock()
	var found bool
	for _, e := range au.events {
		if e.Action == "task.stop" {
			found = true
			if e.Value != stopReason {
				t.Errorf("audit event Value = %q; want %q", e.Value, stopReason)
			}
		}
	}
	if !found {
		t.Errorf("no task.stop audit event recorded; events: %v", au.events)
	}
}

// TestStop_All_AllFail: --all where every stopOneTask fails → exit non-zero
func TestStop_All_AllFail(t *testing.T) {
	ss, ws, _ := installFakeStopDeps(t)
	ss.rows[1] = store.Task{ID: 1, Source: "manual", Title: "run", Status: store.StatusRunning, WS: "workspace:1"}
	ws.stopErr = fmt.Errorf("cmux gone")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop", "--all"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit=0; want non-zero when all stops failed")
	}
}

// TestStop_Task_NoWS: running task without workspace still gets marked failed
func TestStop_Task_NoWS(t *testing.T) {
	ss, ws, _ := installFakeStopDeps(t)
	ss.rows[9] = store.Task{ID: 9, Source: "manual", Title: "no-ws", Status: store.StatusRunning, WS: ""}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"stop", "--task", "9"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d; stderr=%q", code, stderr.String())
	}
	if ss.rows[9].Status != store.StatusFailed {
		t.Errorf("task 9 status = %q; want failed", ss.rows[9].Status)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	// Stop should not be called when WS is empty
	if len(ws.stopped) != 0 {
		t.Errorf("stopped workspaces = %v; want empty (no WS)", ws.stopped)
	}
}
