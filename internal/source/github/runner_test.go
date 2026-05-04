package github_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/source/github"
)

// TestExecRunnerCapturesStdoutSeparatelyFromStderr exercises the real
// ExecRunner against /bin/sh so the package keeps a smoke test against a
// binary that always exists on darwin / linux. We do not invoke `gh` itself
// — that would require the user's gh to be installed and authenticated,
// which CI cannot guarantee.
func TestExecRunnerCapturesStdoutSeparatelyFromStderr(t *testing.T) {
	r := github.ExecRunner{}
	stdout, stderr, err := r.Run(context.Background(), "sh", "-c",
		`printf 'out-line\n'; printf 'err-line\n' >&2`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(string(stdout)); got != "out-line" {
		t.Errorf("stdout = %q; want %q", got, "out-line")
	}
	if got := strings.TrimSpace(string(stderr)); got != "err-line" {
		t.Errorf("stderr = %q; want %q", got, "err-line")
	}
}

// TestExecRunnerSurfacesMissingBinaryAsExecErrNotFound pins the contract
// the package documents: a missing binary is an *exec.Error wrapping
// exec.ErrNotFound, so callers can map it via errors.Is. The fixed name
// stays unique enough to never collide with a real binary on PATH.
func TestExecRunnerSurfacesMissingBinaryAsExecErrNotFound(t *testing.T) {
	r := github.ExecRunner{}
	_, _, err := r.Run(context.Background(), "marunage-no-such-tool-pr83")
	if err == nil {
		t.Fatalf("Run on missing binary returned nil error; want exec.ErrNotFound")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v; want errors.Is(err, exec.ErrNotFound)", err)
	}
}
