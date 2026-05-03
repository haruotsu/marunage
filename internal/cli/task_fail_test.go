package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// `marunage fail <id>` flips an active row (pending / running /
// waiting_human) to `failed` and prints a confirmation line.
func TestTaskFail_TransitionsPendingToFailed(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[3] = store.Task{
		ID: 3, Source: "manual", Title: "abandon", Status: store.StatusPending,
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"fail", "3"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("fail exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[3].Status; got != store.StatusFailed {
		t.Errorf("status = %q; want %q", got, store.StatusFailed)
	}
	if !strings.Contains(stdout.String(), "failed") {
		t.Errorf("stdout should mention new status; got %q", stdout.String())
	}
}

func TestTaskFail_TransitionsRunningToFailed(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusRunning}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"fail", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("fail exit=%d; stderr=%q", code, stderr.String())
	}
	if repo.rows[1].Status != store.StatusFailed {
		t.Errorf("status = %q; want failed", repo.rows[1].Status)
	}
}

// failed -> failed is rejected (same reasoning as TestTaskDone_RejectsDoneToDone).
func TestTaskFail_RejectsFailedToFailed(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusFailed}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"fail", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for failed->failed")
	}
}

func TestTaskFail_NotFoundExit1(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"fail", "999"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("not-found exit=%d; want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "999") {
		t.Errorf("stderr should mention id; got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("stderr should say 'not found'; got %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "Error:") {
		t.Errorf("stderr should not contain cobra's 'Error:' prefix; got %q", stderr.String())
	}
}

// fail also fires OnDone — both completion-style transitions trigger the
// same upstream-marking mirror hook (the markdown source plugin renders
// both as a checked checkbox; future plugins differentiate).
func TestTaskFail_FiresMirrorOnDone(t *testing.T) {
	repo := installFakeRepo(t)
	mirror := installFakeMirror(t)
	repo.rows[1] = store.Task{ID: 1, Source: "markdown", Title: "x", Status: store.StatusRunning}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"fail", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("fail exit=%d; stderr=%q", code, stderr.String())
	}
	if len(mirror.doneCalls) != 1 {
		t.Fatalf("mirror.OnDone calls = %d; want 1", len(mirror.doneCalls))
	}
	if mirror.doneCalls[0].Status != store.StatusFailed {
		t.Errorf("mirror.OnDone arg status = %q; want failed", mirror.doneCalls[0].Status)
	}
}

func TestTaskFail_RequiresIDArg(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"fail"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when id missing")
	}
}
