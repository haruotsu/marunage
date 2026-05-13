package herdr

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/haruotsu/marunage/internal/workspace"
)

// claudeReadinessProbe checks whether Claude's interactive prompt
// ("❯") is visible in the pane. WaitReady polls this until Claude
// finishes its startup sequence (trust prompt, bypass-permissions
// prompt, etc.) and is ready to accept a Send.
//
// This mirrors the cmux backend's identical probe; the only
// backend-specific bit is the read command (`herdr pane read` vs
// `cmux read-screen`).
type claudeReadinessProbe struct {
	runner workspace.Runner
}

// NewClaudeReadinessProbe returns a ReadinessProbe that shells out to
// `herdr pane read <pane_id> --source recent` and returns ready when
// the "❯" input prompt appears alongside Claude's own banner.
// Read errors are suppressed (returning false) so WaitReady keeps
// polling while the pane is still booting.
func NewClaudeReadinessProbe() ReadinessProbe {
	return &claudeReadinessProbe{runner: workspace.ExecRunner{}}
}

// NewClaudeReadinessProbeWithRunner is the test-friendly constructor
// that lets a scripted runner stand in for the real herdr CLI.
func NewClaudeReadinessProbeWithRunner(r workspace.Runner) ReadinessProbe {
	return &claudeReadinessProbe{runner: r}
}

// terminalReadFailures are stderr substrings that mean "the pane (or
// the whole herdr server) is gone, no amount of polling will fix it".
// WaitReady should fail loudly on these instead of looping until the
// 60s startup timeout fires. Anything else (e.g. a one-off "could not
// read scrollback" hiccup during the trust-prompt → claude transition)
// stays a transient false-and-keep-polling result.
var terminalReadFailures = []string{
	"pane_not_found",
	"workspace_not_found",
	"server not running",
	"connection refused",
}

func (p *claudeReadinessProbe) IsReady(ctx context.Context, ws Workspace) (bool, error) {
	stdout, stderr, err := p.runner.Run(ctx, "herdr", "pane", "read", ws.ID,
		"--source", "recent",
		"--lines", "200",
	)
	if err != nil {
		if workspace.IsBinaryNotFound(err) {
			return false, ErrHerdrNotFound
		}
		lower := bytes.ToLower(stderr)
		for _, marker := range terminalReadFailures {
			if bytes.Contains(lower, []byte(marker)) {
				return false, fmt.Errorf("herdr pane read: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
			}
		}
		return false, nil
	}
	out := string(stdout)
	// Claude is ready only when its own banner is visible AND the
	// input prompt is present. Matching "Claude Code v" avoids false
	// positives from the shell prompt ("❯ claude") that appears while
	// Claude is still starting up.
	return strings.Contains(out, "Claude Code v") && strings.Contains(out, "❯"), nil
}
