package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/render"
	"github.com/haruotsu/marunage/internal/store"
)

// installRenderHarness wires both the fake task repo and a test-controlled
// view path into one helper so each render test only states the parts it
// cares about. Returns the fake repo plus the absolute path the SUT will
// write to.
func installRenderHarness(t *testing.T) (*fakeTaskRepo, string) {
	t.Helper()
	repo := installFakeRepo(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "view.md")
	withViewPath(t, dest)
	return repo, dest
}

// C1: `marunage render` writes the cmux view file at the configured
// destination. We use the viewPath hook to redirect the write into a
// t.TempDir so production code can keep the ~/.marunage/view.md
// constant without the test touching the real home directory.
func TestTaskRender_WritesViewFile(t *testing.T) {
	_, dest := installRenderHarness(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("render exit=%d; stderr=%q", code, stderr.String())
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read view file: %v", err)
	}
	if !strings.Contains(string(got), "# marunage view") {
		t.Errorf("view file does not look rendered\n%s", got)
	}
}

// C2: A previous view.md is overwritten — and no .tmp.* sibling is
// left behind, proving atomic write went all the way through.
func TestTaskRender_OverwritesExistingAndLeavesNoTmp(t *testing.T) {
	repo, dest := installRenderHarness(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "fresh row", Status: store.StatusPending,
	}

	if err := os.WriteFile(dest, []byte("STALE CONTENT\n"), 0o600); err != nil {
		t.Fatalf("seed stale view: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("render exit=%d; stderr=%q", code, stderr.String())
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(got), "STALE CONTENT") {
		t.Errorf("stale content still present:\n%s", got)
	}
	if !strings.Contains(string(got), "fresh row") {
		t.Errorf("new content missing:\n%s", got)
	}

	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("leftover tmp sibling found: %q", e.Name())
		}
	}
}

// C3: The CLI is a thin wrapper — its file content matches what
// render.Render() returns for the same task list and the injected
// clock. If the two diverge a future change to one without the other
// goes silently uncaught.
func TestTaskRender_FileContentMatchesPureRender(t *testing.T) {
	repo, dest := installRenderHarness(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "p", Status: store.StatusPending, Priority: 5,
	}
	repo.rows[2] = store.Task{
		ID: 2, Source: "gmail", Title: "r", Status: store.StatusRunning,
	}

	fixed := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	withRenderClock(t, func() time.Time { return fixed })

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("render exit=%d; stderr=%q", code, stderr.String())
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := render.Render([]store.Task{repo.rows[1], repo.rows[2]}, fixed)
	if string(got) != want {
		t.Errorf("file content diverges from pure Render()\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// C5: stdout echoes the absolute resolved path so the operator (or a
// shell pipeline like `marunage render | xargs bat`) can chain on the
// path.
func TestTaskRender_EchoesPathOnStdout(t *testing.T) {
	_, dest := installRenderHarness(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("render exit=%d; stderr=%q", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), dest) {
		t.Errorf("stdout missing destination path %q\nstdout=%q", dest, stdout.String())
	}
}

// C6: A repo open failure surfaces as a non-zero exit and does not leave
// a half-written view file behind (atomic write only fires after the
// task list is in hand).
func TestTaskRender_PropagatesRepoErrors(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "view.md")
	withViewPath(t, dest)
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("view file should not exist after repo error; stat err=%v", err)
	}
}
