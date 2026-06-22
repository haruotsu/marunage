package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withOpenViewer swaps the cmux launcher for the duration of t.
func withOpenViewer(t *testing.T, fn func(string) error) {
	t.Helper()
	prev := openViewerHook
	openViewerHook = fn
	t.Cleanup(func() { openViewerHook = prev })
}

// OP1: `marunage open` renders view.md and launches the viewer on that path.
func TestOpen_RendersAndLaunchesViewer(t *testing.T) {
	_, dest := installRenderHarness(t)
	var launched string
	withOpenViewer(t, func(p string) error { launched = p; return nil })

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"open"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("open exit=%d; stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("view.md not written: %v", err)
	}
	if launched != dest {
		t.Errorf("viewer launched on %q; want %q", launched, dest)
	}
}

// OP2: a viewer launch failure (e.g. cmux absent) is soft — open still exits
// 0 and prints the path plus a hint so the user can open the file manually.
func TestOpen_ViewerFailureIsSoft(t *testing.T) {
	_, dest := installRenderHarness(t)
	withOpenViewer(t, func(string) error { return errors.New("cmux not found on PATH") })

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"open"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("open exit=%d; want 0 when viewer cannot launch; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), dest) {
		t.Errorf("stdout=%q; want it to include the view path %q", stdout.String(), dest)
	}
	if !strings.Contains(stdout.String(), "could not open in cmux") {
		t.Errorf("stdout=%q; want a hint that cmux launch failed", stdout.String())
	}
}

// OP3: a repo error fails the command before any viewer launch.
func TestOpen_RepoErrorPropagatesAndDoesNotLaunch(t *testing.T) {
	withViewPath(t, filepath.Join(t.TempDir(), "view.md"))
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})
	launched := false
	withOpenViewer(t, func(string) error { launched = true; return nil })

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"open"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("open exit=0; want non-zero on repo error; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if launched {
		t.Error("viewer launched despite repo error")
	}
}
