package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// `marunage done <id>` flips an active row (pending / running /
// waiting_human) to `done` and prints a confirmation line referencing the
// id and the new status.
func TestTaskDone_TransitionsPendingToDone(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[7] = store.Task{
		ID: 7, Source: "manual", Title: "ship it",
		Status: store.StatusPending,
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done", "7"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("done exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[7].Status; got != store.StatusDone {
		t.Errorf("status = %q; want %q", got, store.StatusDone)
	}
	if !strings.Contains(stdout.String(), "7") {
		t.Errorf("stdout should mention id 7; got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "done") {
		t.Errorf("stdout should mention new status; got %q", stdout.String())
	}
}

func TestTaskDone_TransitionsRunningToDone(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "x", Status: store.StatusRunning,
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("done exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[1].Status; got != store.StatusDone {
		t.Errorf("status = %q; want %q", got, store.StatusDone)
	}
}

// Refusing to flip a done row to done again preserves the audit trail —
// repeated done commands do not silently succeed and cause spurious
// notifications.
func TestTaskDone_RejectsDoneToDone(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "x", Status: store.StatusDone,
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for done->done")
	}
	if got := repo.rows[1].Status; got != store.StatusDone {
		t.Errorf("status drifted: got %q", got)
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "transition") &&
		!strings.Contains(strings.ToLower(stderr.String()), "cannot") {
		t.Errorf("stderr should explain the rejection; got %q", stderr.String())
	}
}

// Missing id surfaces the friendly "Task #<id> not found." line that the
// `show` subcommand established as the standard.
func TestTaskDone_NotFoundExit1(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done", "999"}, &stdout, &stderr)
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

func TestTaskDone_NonNumericIDFails(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done", "abc"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q", stdout.String())
	}
}

func TestTaskDone_RequiresIDArg(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when id missing")
	}
}

// Done fires the mirror's OnDone hook with the post-transition row so the
// markdown-source plugin can flip the upstream checkbox.
func TestTaskDone_FiresMirrorOnDone(t *testing.T) {
	repo := installFakeRepo(t)
	mirror := installFakeMirror(t)
	repo.rows[1] = store.Task{ID: 1, Source: "markdown", Title: "x", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("done exit=%d; stderr=%q", code, stderr.String())
	}
	if len(mirror.doneCalls) != 1 {
		t.Fatalf("mirror.OnDone calls = %d; want 1", len(mirror.doneCalls))
	}
	if mirror.doneCalls[0].ID != 1 || mirror.doneCalls[0].Status != store.StatusDone {
		t.Errorf("mirror.OnDone arg = %+v; want id=1 status=done", mirror.doneCalls[0])
	}
}

// Mirror failure does not roll back the SQLite transition. The local store
// is the source of truth; mirror sync is best-effort and surfaced as a
// non-zero exit so the operator knows the upstream is stale.
func TestTaskDone_MirrorErrorPropagatesButTransitionPersists(t *testing.T) {
	repo := installFakeRepo(t)
	mirror := installFakeMirror(t)
	mirror.errOn["done"] = errors.New("mirror failed")
	repo.rows[1] = store.Task{ID: 1, Source: "markdown", Title: "x", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when mirror fails; stdout=%q", stdout.String())
	}
	if got := repo.rows[1].Status; got != store.StatusDone {
		t.Errorf("local status should still be done; got %q", got)
	}
}

// Repo open errors propagate.
func TestTaskDone_PropagatesRepoErrors(t *testing.T) {
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"done", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
