package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// `marunage rm <id>` removes the row regardless of status. The pre-delete
// snapshot is what fires the mirror hook so the source plugin still has
// external_id available.
func TestTaskRm_RemovesPendingRow(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"rm", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rm exit=%d; stderr=%q", code, stderr.String())
	}
	if _, ok := repo.rows[1]; ok {
		t.Errorf("row 1 should have been deleted")
	}
	if !strings.Contains(stdout.String(), "1") {
		t.Errorf("stdout should mention id 1; got %q", stdout.String())
	}
}

func TestTaskRm_RemovesDoneRow(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusDone}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"rm", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rm exit=%d; stderr=%q", code, stderr.String())
	}
	if _, ok := repo.rows[1]; ok {
		t.Errorf("row 1 should have been deleted regardless of status")
	}
}

// rm fires the OnDelete mirror hook with the pre-delete snapshot — the
// hook needs external_id and source to find the upstream row.
func TestTaskRm_FiresMirrorOnDeleteWithPreDeleteSnapshot(t *testing.T) {
	repo := installFakeRepo(t)
	mirror := installFakeMirror(t)
	repo.rows[7] = store.Task{
		ID: 7, Source: "markdown", Title: "x", ExternalID: "ext-7",
		Status: store.StatusPending,
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"rm", "7"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rm exit=%d; stderr=%q", code, stderr.String())
	}
	if len(mirror.deleteCalls) != 1 {
		t.Fatalf("mirror.OnDelete calls = %d; want 1", len(mirror.deleteCalls))
	}
	got := mirror.deleteCalls[0]
	if got.ID != 7 || got.ExternalID != "ext-7" {
		t.Errorf("OnDelete arg = %+v; want id=7 external_id=ext-7", got)
	}
}

// Mirror error after a successful delete still surfaces non-zero exit but
// the local row is already gone — same best-effort contract as `done`.
func TestTaskRm_MirrorErrorPropagatesButDeletePersists(t *testing.T) {
	repo := installFakeRepo(t)
	mirror := installFakeMirror(t)
	mirror.errOn["delete"] = errors.New("mirror failed")
	repo.rows[1] = store.Task{ID: 1, Source: "markdown", Title: "x", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"rm", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when mirror fails")
	}
	if _, ok := repo.rows[1]; ok {
		t.Errorf("row 1 should still be deleted even when mirror fails")
	}
}

func TestTaskRm_NotFoundExit1(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"rm", "999"}, &stdout, &stderr)
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

func TestTaskRm_NonNumericIDFails(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"rm", "abc"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q", stdout.String())
	}
}

func TestTaskRm_RequiresIDArg(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"rm"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when id missing")
	}
}

func TestTaskRm_PropagatesRepoErrors(t *testing.T) {
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"rm", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
