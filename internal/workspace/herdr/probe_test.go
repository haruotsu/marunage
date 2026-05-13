package herdr_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/haruotsu/marunage/internal/workspace"
	"github.com/haruotsu/marunage/internal/workspace/herdr"
)

// ClaudeReadinessProbe should report ready when both Claude's banner
// and the "❯" input prompt appear in the pane content.
func TestClaudeReadinessProbe_ReadyWhenBannerAndPromptVisible(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "Claude Code v1.0\n❯ "})

	probe := herdr.NewClaudeReadinessProbeWithRunner(r)
	ready, err := probe.IsReady(context.Background(), workspace.Workspace{ID: "w1-1"})
	if err != nil {
		t.Fatalf("IsReady: %v", err)
	}
	if !ready {
		t.Errorf("ready = false; want true when banner + prompt visible")
	}
}

// Probe should NOT report ready when only the shell prompt "❯" is
// visible without the Claude banner (e.g. shell prompt before claude
// starts up).
func TestClaudeReadinessProbe_NotReadyWhenOnlyPromptVisible(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "❯ claude --dangerously-skip-permissions"})

	probe := herdr.NewClaudeReadinessProbeWithRunner(r)
	ready, _ := probe.IsReady(context.Background(), workspace.Workspace{ID: "w1-1"})
	if ready {
		t.Errorf("ready = true; want false when banner missing")
	}
}

// Probe should swallow transient read errors (returning false, nil) so
// WaitReady keeps polling.
func TestClaudeReadinessProbe_SuppressesTransientReadErrors(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Err: errors.New("transient")})

	probe := herdr.NewClaudeReadinessProbeWithRunner(r)
	ready, err := probe.IsReady(context.Background(), workspace.Workspace{ID: "w1-1"})
	if err != nil {
		t.Errorf("IsReady err = %v; want nil (transient errors suppressed)", err)
	}
	if ready {
		t.Errorf("ready = true; want false on transient error")
	}
}

// Probe must promote a missing-binary error to ErrHerdrNotFound so the
// surrounding WaitReady fast-fails instead of looping silently.
func TestClaudeReadinessProbe_PromotesMissingBinary(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Err: &exec.Error{Name: "herdr", Err: exec.ErrNotFound}})

	probe := herdr.NewClaudeReadinessProbeWithRunner(r)
	_, err := probe.IsReady(context.Background(), workspace.Workspace{ID: "w1-1"})
	if !errors.Is(err, herdr.ErrHerdrNotFound) {
		t.Errorf("err = %v; want ErrHerdrNotFound", err)
	}
}

// Probe promotes pane-gone / server-stopped style stderr markers to a
// real error so WaitReady stops polling instead of waiting out the
// 60s startup window for a pane that is never coming back.
func TestClaudeReadinessProbe_PropagatesTerminalFailures(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
	}{
		{"pane not found", "Error: pane_not_found: w1-99"},
		{"workspace not found", "Error: workspace_not_found"},
		{"server stopped", "Error: server not running"},
		{"socket refused", "Error: Connection refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{}
			r.queue(runResult{Err: errors.New("exit status 1"), Stderr: tc.stderr})

			probe := herdr.NewClaudeReadinessProbeWithRunner(r)
			ready, err := probe.IsReady(context.Background(), workspace.Workspace{ID: "w1-1"})
			if err == nil {
				t.Fatalf("expected error for stderr %q; got ready=%v err=nil", tc.stderr, ready)
			}
			if ready {
				t.Errorf("ready = true; want false on terminal failure")
			}
		})
	}
}
