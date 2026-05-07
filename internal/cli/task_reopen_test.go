package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// `marunage reopen <id>` flips a done or failed row back to pending. It is
// the manual escape hatch for "I closed this too soon" — `promote`
// covers the skipped case separately.
func TestTaskReopen_TransitionsDoneToPending(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[2] = store.Task{ID: 2, Source: "manual", Title: "redo", Status: store.StatusDone}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen", "2"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("reopen exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[2].Status; got != store.StatusPending {
		t.Errorf("status = %q; want pending", got)
	}
	if !strings.Contains(stdout.String(), "pending") {
		t.Errorf("stdout should mention new status; got %q", stdout.String())
	}
}

func TestTaskReopen_TransitionsFailedToPending(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusFailed}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("reopen exit=%d; stderr=%q", code, stderr.String())
	}
	if repo.rows[1].Status != store.StatusPending {
		t.Errorf("status = %q; want pending", repo.rows[1].Status)
	}
}

// reopen on a skipped row is rejected — that path is owned by `promote`
// so the operator picks the verb that matches their intent.
func TestTaskReopen_RejectsSkippedRow(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusSkipped}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit reopening a skipped row")
	}
	if repo.rows[1].Status != store.StatusSkipped {
		t.Errorf("status drifted: got %q", repo.rows[1].Status)
	}
}

func TestTaskReopen_RejectsPendingRow(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit reopening a pending row")
	}
}

func TestTaskReopen_NotFoundExit1(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen", "999"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("not-found exit=%d; want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("stderr should say 'not found'; got %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "Error:") {
		t.Errorf("stderr should not contain cobra's 'Error:' prefix; got %q", stderr.String())
	}
}

// reopen fires the OnReopen mirror hook with the post-transition row so
// the markdown plugin can flip the upstream checkbox back to unchecked.
func TestTaskReopen_FiresMirrorOnReopen(t *testing.T) {
	repo := installFakeRepo(t)
	mirror := installFakeMirror(t)
	repo.rows[1] = store.Task{ID: 1, Source: "markdown", Title: "x", Status: store.StatusDone}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("reopen exit=%d; stderr=%q", code, stderr.String())
	}
	if len(mirror.reopenCalls) != 1 {
		t.Fatalf("mirror.OnReopen calls = %d; want 1", len(mirror.reopenCalls))
	}
	if mirror.reopenCalls[0].Status != store.StatusPending {
		t.Errorf("mirror.OnReopen arg status = %q; want pending", mirror.reopenCalls[0].Status)
	}
}

func TestTaskReopen_RequiresIDArg(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when id missing")
	}
}

// reopen must fail (non-zero exit) when SetWorkspace returns an error so the
// DB does not sit in a "pending + stale ws" state that permanently blocks dispatch.
func TestTaskReopen_PropagatesSetWorkspaceError(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusFailed, WS: "workspace:9"}
	repo.setWSErr = errors.New("db locked")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("reopen returned exit=0; want non-zero when SetWorkspace fails")
	}
}

// reopen clears the ws field so the next dispatch can call ClaimWorkspace.
func TestTaskReopen_ClearsWorkspaceReference(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusFailed, WS: "workspace:9"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reopen", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("reopen exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[1].WS; got != "" {
		t.Errorf("WS = %q; want empty after reopen", got)
	}
}
