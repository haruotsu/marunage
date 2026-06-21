package loop_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/exec"
	"github.com/haruotsu/marunage/internal/loop"
	"github.com/haruotsu/marunage/internal/manage"
	"github.com/haruotsu/marunage/internal/source/markdown"
	"github.com/haruotsu/marunage/internal/store"
)

// recordingExecutor is a minimal exec.Executor that records the Start specs and
// Send prompts it receives, so the e2e test can assert the dispatcher actually
// launched and fed a session without depending on a real cmux binary.
type recordingExecutor struct {
	mu      sync.Mutex
	started []exec.SessionSpec
	sent    []string
}

func (e *recordingExecutor) Start(_ context.Context, spec exec.SessionSpec) (exec.Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.started = append(e.started, spec)
	return exec.NewSession("ws:1", nil), nil
}

func (e *recordingExecutor) Send(_ context.Context, _ exec.Session, prompt string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sent = append(e.sent, prompt)
	return nil
}

func (e *recordingExecutor) AwaitExit(context.Context, exec.Session) (int, error) { return 0, nil }

func (e *recordingExecutor) counts() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.started), len(e.sent)
}

// TestE2E_MarkdownChecklistThroughManageToExec is the PR-R05 acceptance test:
// a Markdown checklist file flows through collect → manage (ready) → exec.
// The open item is ranked ready and dispatched into the (recording) executor;
// the already-checked item is preserved as done and never dispatched.
func TestE2E_MarkdownChecklistThroughManageToExec(t *testing.T) {
	f := newFixture(t)

	// A real Markdown TODO file on disk: one open item, one already done.
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "todo.md")
	if err := os.WriteFile(mdPath, []byte("- [ ] Ship the release\n- [x] Old finished task\n"), 0o600); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	plugin := markdown.NewAdapter(markdown.New(markdown.WithFiles(mdPath)))
	if err := f.reg.Register(plugin); err != nil {
		t.Fatalf("register markdown: %v", err)
	}

	// Real dispatcher wired to a recording executor (no cmux needed). The cwd
	// gate is open (empty allowlist) with dir as the default cwd so a markdown
	// row, which carries no cwd, still passes.
	rec := &recordingExecutor{}
	disp, err := dispatch.New(
		dispatch.WithStore(f.repo),
		dispatch.WithExecutor(rec),
		dispatch.WithBaseSkill("be a good worker"),
		dispatch.WithClaudeCommand("claude"),
		dispatch.WithDefaultCwd(dir),
	)
	if err != nil {
		t.Fatalf("dispatch.New: %v", err)
	}

	l, err := loop.New(
		loop.WithRegistry(f.reg),
		loop.WithTaskRepo(f.repo),
		loop.WithKVStateRepo(f.kv),
		loop.WithDispatcher(disp),
		loop.WithRender(f.rend),
		loop.WithAuditor(f.aud),
		loop.WithClock(func() time.Time { return f.now }),
		loop.WithMaxParallel(2),
		loop.WithManageStore(f.repo),
		loop.WithManageOptions(manage.WithDefaultCwd(dir)),
	)
	if err != nil {
		t.Fatalf("loop.New: %v", err)
	}

	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var open, done *store.Task
	for i := range rows {
		switch rows[i].Title {
		case "Ship the release":
			open = &rows[i]
		case "Old finished task":
			done = &rows[i]
		}
	}
	if open == nil || done == nil {
		t.Fatalf("expected both markdown rows; got %+v", rows)
	}

	// The open item was classified ready and dispatched: it is now running
	// (the dispatcher claimed it) with plan_label=ready.
	if open.PlanLabel != "ready" {
		t.Errorf("open PlanLabel = %q; want ready", open.PlanLabel)
	}
	if open.Status != store.StatusRunning {
		t.Errorf("open Status = %q; want running (dispatched)", open.Status)
	}
	// The checked item stays done and is never dispatched.
	if done.Status != store.StatusDone {
		t.Errorf("done Status = %q; want done", done.Status)
	}

	startedN, sentN := rec.counts()
	if startedN != 1 || sentN != 1 {
		t.Fatalf("executor Start=%d Send=%d; want exactly 1/1 (only the ready item executes)", startedN, sentN)
	}
	if rec.started[0].Cwd != dir {
		t.Errorf("Start cwd = %q; want %q", rec.started[0].Cwd, dir)
	}
}
