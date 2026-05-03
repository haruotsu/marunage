package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// `marunage promote <id>` flips a skipped row back to pending so the
// dispatcher will pick it up next time. Only skipped rows are eligible;
// every other status is rejected.
func TestTaskPromote_TransitionsSkippedToPending(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[5] = store.Task{
		ID: 5, Source: "manual", Title: "second chance", Status: store.StatusSkipped,
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"promote", "5"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("promote exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[5].Status; got != store.StatusPending {
		t.Errorf("status = %q; want %q", got, store.StatusPending)
	}
	if !strings.Contains(stdout.String(), "pending") {
		t.Errorf("stdout should mention new status; got %q", stdout.String())
	}
}

// promote on a non-skipped row is rejected.
func TestTaskPromote_RejectsPendingRow(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"promote", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit promoting a pending row")
	}
	if repo.rows[1].Status != store.StatusPending {
		t.Errorf("status drifted: got %q", repo.rows[1].Status)
	}
}

func TestTaskPromote_RejectsDoneRow(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusDone}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"promote", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit promoting a done row")
	}
}

func TestTaskPromote_NotFoundExit1(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"promote", "999"}, &stdout, &stderr)
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

// promote fires the OnReopen mirror hook (a "back into the queue"
// notification — same upstream effect as reopen).
func TestTaskPromote_FiresMirrorOnReopen(t *testing.T) {
	repo := installFakeRepo(t)
	mirror := installFakeMirror(t)
	repo.rows[1] = store.Task{ID: 1, Source: "markdown", Title: "x", Status: store.StatusSkipped}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"promote", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("promote exit=%d; stderr=%q", code, stderr.String())
	}
	if len(mirror.reopenCalls) != 1 {
		t.Fatalf("mirror.OnReopen calls = %d; want 1", len(mirror.reopenCalls))
	}
	if mirror.reopenCalls[0].Status != store.StatusPending {
		t.Errorf("mirror.OnReopen arg status = %q; want pending", mirror.reopenCalls[0].Status)
	}
}

func TestTaskPromote_RequiresIDArg(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"promote"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when id missing")
	}
}
