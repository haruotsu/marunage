package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// PR-44 CLI test list (t_wada TDD; ticked off as the matching test below
// goes green):
//
//   G1. `marunage reaper` builds a Reaper from config and invokes Run
//       once. The factory seam (reaperFactoryHook) lets the test capture
//       the Run call without spinning up cmux / sqlite.
//   G2. Run errors propagate as a non-zero exit (cobra prints the error
//       to stderr).
//   G3. The factory hook (`withReaperFactory`) lets tests inject a fake.
//   G4. `marunage --help` includes the reaper subcommand entry.

// fakeReaper captures Run invocations and returns the configured error.
// Tests inject it via withReaperFactory.
type fakeReaper struct {
	mu       sync.Mutex
	runCalls int
	runErr   error
}

func (f *fakeReaper) Run(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCalls++
	return f.runErr
}

func installFakeReaper(t *testing.T) *fakeReaper {
	t.Helper()
	fr := &fakeReaper{}
	withReaperFactory(t, func(_ context.Context, _ string) (reaperRunner, func() error, error) {
		return fr, func() error { return nil }, nil
	})
	return fr
}

// G1: bare `marunage reaper` calls Run once.
func TestReaper_RunsOnce(t *testing.T) {
	fr := installFakeReaper(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reaper"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("reaper exit=%d; stderr=%q", code, stderr.String())
	}
	if fr.runCalls != 1 {
		t.Errorf("Run calls = %d; want 1", fr.runCalls)
	}
}

// G2: Run errors propagate as a non-zero exit + the message lands on
// stderr so the operator sees what blocked the sweep.
func TestReaper_RunErrorPropagates(t *testing.T) {
	fr := installFakeReaper(t)
	fr.runErr = errors.New("cmux unreachable")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"reaper"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("reaper with Run error exit=0; want non-zero; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "cmux unreachable") {
		t.Errorf("stderr = %q; want it to mention the underlying error", stderr.String())
	}
}

// G4: `marunage --help` mentions the reaper subcommand so users can
// discover it without reading the source.
func TestReaper_RegisteredOnRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("--help exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "reaper") {
		t.Errorf("--help output missing 'reaper' line:\n%s", stdout.String())
	}
}
