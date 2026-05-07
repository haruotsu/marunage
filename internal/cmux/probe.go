package cmux

import (
	"context"
	"strings"
)

// claudeReadinessProbe checks whether Claude's interactive prompt ("❯") is
// visible in the workspace terminal. WaitReady polls this until Claude
// finishes its startup sequence (trust prompt, bypass-permissions prompt, etc.)
// and is ready to accept a Send.
type claudeReadinessProbe struct {
	runner Runner
}

// NewClaudeReadinessProbe returns a ReadinessProbe that shells out to
// `cmux read-screen --workspace <ws.ID>` and returns ready when the "❯"
// input prompt appears. Read-screen errors are suppressed (returning
// false) so WaitReady keeps polling while the workspace is still booting.
func NewClaudeReadinessProbe() ReadinessProbe {
	return &claudeReadinessProbe{runner: ExecRunner{}}
}

func (p *claudeReadinessProbe) IsReady(ctx context.Context, ws Workspace) (bool, error) {
	stdout, _, err := p.runner.Run(ctx, "cmux", "read-screen", "--workspace", ws.ID)
	if err != nil {
		if isBinaryNotFound(err) {
			return false, ErrCmuxNotFound
		}
		return false, nil
	}
	out := string(stdout)
	// Claude is ready only when its own banner is visible AND the input
	// prompt is present. Matching "Claude Code v" avoids false-positives
	// from the shell prompt ("❯ claude") that appears while Claude is
	// still starting up.
	return strings.Contains(out, "Claude Code v") && strings.Contains(out, "❯"), nil
}
