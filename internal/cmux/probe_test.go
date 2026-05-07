package cmux

import (
	"context"
	"errors"
	"testing"
)

// Test list for ClaudeReadinessProbe:
//   P1. Returns true when read-screen output contains the "❯" prompt.
//   P2. Returns false when read-screen output lacks the "❯" prompt.
//   P3. Returns false (not error) when read-screen fails so WaitReady keeps polling.

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

// scriptedRunner returns fixed stdout/err for all Run calls.
type scriptedRunner struct {
	stdout []byte
	err    error
}

func (s scriptedRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
	return s.stdout, nil, s.err
}
