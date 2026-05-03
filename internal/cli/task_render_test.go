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
// left behind, proving atomic write went all the way through. Also
// pins the 0o600 perm contract: the chmod must happen on the tmp
// before the rename, so a racing reader cannot observe a wider mode.
// (Mirrors internal/source/markdown/writer_test.go TestAtomicWriteFile.)
func TestTaskRender_OverwritesExistingAndLeavesNoTmp(t *testing.T) {
	repo, dest := installRenderHarness(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "fresh row", Status: store.StatusPending,
	}

	// Seed with a deliberately wider mode so the assertion below cannot
	// be satisfied accidentally by reusing the seed file's permissions.
	if err := os.WriteFile(dest, []byte("STALE CONTENT\n"), 0o644); err != nil {
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

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %v; want 0o600 (chmod must run on tmp before rename)", perm)
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

// C4: render must list EVERY task, not the dispatcher's "actionable
// only" subset. The on-disk contract is "single screen of everything",
// so the SUT has to pass an empty ListFilter (no Statuses, no Sources,
// no Limit) into repo.List. A future change that defaults to
// {Statuses: [pending, running]} (matching `marunage list`) would
// silently break the cmux viewer's "see done/failed/skipped too"
// promise without this guard.
func TestTaskRender_PassesEmptyListFilter(t *testing.T) {
	repo, _ := installRenderHarness(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("render exit=%d; stderr=%q", code, stderr.String())
	}

	if len(repo.listFilters) != 1 {
		t.Fatalf("listFilters captured = %d; want 1", len(repo.listFilters))
	}
	got := repo.listFilters[0]
	if len(got.Statuses) != 0 {
		t.Errorf("Statuses = %v; want empty (render shows every status)", got.Statuses)
	}
	if len(got.Sources) != 0 {
		t.Errorf("Sources = %v; want empty", got.Sources)
	}
	if got.Limit != 0 {
		t.Errorf("Limit = %d; want 0 (no truncation in view.md)", got.Limit)
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

// C7: A repo.List failure (after the factory succeeded) must also leave
// no view file behind. C6 only covers the factory-open path; the read
// path is the second window where a partial write could appear if the
// SUT ever pre-truncated the destination.
func TestTaskRender_NoViewFileWrittenOnListError(t *testing.T) {
	repo, dest := installRenderHarness(t)
	repo.listErr = errBoom

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("view file should not exist after List error; stat err=%v", err)
	}
}

// C8b: A pre-existing parent directory with a wider mode (e.g. the
// user ran `mkdir -m 755 ~/.marunage` by hand, or a previous version
// of marunage created it under a relaxed umask) gets retightened to
// 0o700. MkdirAll alone does not narrow an existing dir, so `marunage
// render` must follow the same Chmod-after-Mkdir pattern that
// internal/secrets/file_backend.go uses for ~/.marunage/secrets.
func TestTaskRender_RetightensExistingParentDirectory(t *testing.T) {
	installFakeRepo(t)
	root := t.TempDir()
	parent := filepath.Join(root, "marunage")
	dest := filepath.Join(parent, "view.md")
	withViewPath(t, dest)

	// Pre-create the parent with the deliberately wide mode the bug
	// shape would leave behind. Mkdir is also subject to umask, so
	// chmod separately to be sure we measure the assertion target.
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("seed chmod parent: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("render exit=%d; stderr=%q", code, stderr.String())
	}

	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("parent perm = %v; want 0o700 (render must retighten existing dir)", perm)
	}
}

// C8: When the destination's parent directory does not yet exist (the
// first ever `marunage render` against a fresh ~/.marunage/ tree), the
// SUT creates it with mode 0o700. ~/.marunage/ holds tasks.db plus
// secrets fallbacks, so a world-readable parent dir would defeat the
// 0o600 view file protection.
func TestTaskRender_CreatesParentDirectoryWith0700(t *testing.T) {
	installFakeRepo(t)
	root := t.TempDir()
	dest := filepath.Join(root, "fresh", "marunage", "view.md")
	withViewPath(t, dest)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"render"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("render exit=%d; stderr=%q", code, stderr.String())
	}

	// Both the leaf parent and any intermediate dir must end up 0o700,
	// so a world-readable middle dir does not leak the file name.
	for _, dir := range []string{filepath.Dir(dest), filepath.Dir(filepath.Dir(dest))} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("dir %s perm = %v; want 0o700", dir, perm)
		}
	}
}
