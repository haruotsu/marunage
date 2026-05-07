package cmux

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

// P1
func TestClaudeReadinessProbe_ReadyWhenPromptPresent(t *testing.T) {
	probe := &claudeReadinessProbe{runner: scriptedRunner{
		stdout: []byte("Claude Code v2.1\n❯ Try \"edit file.go\""),
		err:    nil,
	}}
	ready, err := probe.IsReady(context.Background(), Workspace{ID: "workspace:9"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("want ready=true when Claude banner + ❯ are present")
	}
}

// P1b: shell prompt "❯ claude" alone (Claude still starting) must not be ready.
func TestClaudeReadinessProbe_NotReadyOnShellPromptOnly(t *testing.T) {
	probe := &claudeReadinessProbe{runner: scriptedRunner{
		stdout: []byte("marunage [main]\n❯ claude"),
		err:    nil,
	}}
	ready, err := probe.IsReady(context.Background(), Workspace{ID: "workspace:9"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ready {
		t.Fatal("want ready=false when only shell prompt is visible (Claude still starting)")
	}
}

// P2
func TestClaudeReadinessProbe_NotReadyWhenPromptAbsent(t *testing.T) {
	probe := &claudeReadinessProbe{runner: scriptedRunner{
		stdout: []byte("Starting Claude..."),
		err:    nil,
	}}
	ready, err := probe.IsReady(context.Background(), Workspace{ID: "workspace:9"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ready {
		t.Fatal("want ready=false when ❯ is absent")
	}
}

// P3
func TestClaudeReadinessProbe_NotReadyOnError(t *testing.T) {
	probe := &claudeReadinessProbe{runner: scriptedRunner{
		stdout: nil,
		err:    errors.New("cmux: workspace not found"),
	}}
	ready, err := probe.IsReady(context.Background(), Workspace{ID: "workspace:9"})
	if err != nil {
		t.Fatalf("unexpected error: %v (should suppress and return false)", err)
	}
	if ready {
		t.Fatal("want ready=false on read-screen error")
	}
}

// NewClaudeReadinessProbeWithRunner wires up a custom runner so tests can
// use the public constructor without reaching into the unexported struct.
func TestNewClaudeReadinessProbeWithRunner(t *testing.T) {
	probe := NewClaudeReadinessProbeWithRunner(scriptedRunner{
		stdout: []byte("Claude Code v1.0\n❯ hello"),
	})
	ready, err := probe.IsReady(context.Background(), Workspace{ID: "workspace:9"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("want ready=true when Claude banner + ❯ are visible")
	}
}

// Fatal errors (binary not found) must be propagated so WaitReady stops
// polling immediately instead of burning until timeout.
func TestClaudeReadinessProbe_PropagatesBinaryNotFoundError(t *testing.T) {
	probe := &claudeReadinessProbe{runner: scriptedRunner{
		stdout: nil,
		err:    &exec.Error{Name: "cmux", Err: exec.ErrNotFound},
	}}
	ready, err := probe.IsReady(context.Background(), Workspace{ID: "workspace:9"})
	if err == nil {
		t.Fatal("want error when cmux binary is not found, not (false, nil)")
	}
	if ready {
		t.Fatal("want ready=false when binary is not found")
	}
}

// scriptedRunner returns fixed stdout/err for all Run calls.
type scriptedRunner struct {
	stdout []byte
	err    error
}

func (s scriptedRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
	return s.stdout, nil, s.err
}
