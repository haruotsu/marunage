package doctor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ClaudeMCPProbe implements MCPProbe by running `claude mcp list` and parsing
// each non-empty line as a server name. The output format `claude mcp list`
// emits is one server name per line (e.g. "slack\ngoogle-drive\n").
//
// If the claude binary is missing or the command fails, ListMCPServers returns
// an error so the caller can surface a "could not probe" message rather than
// a silent empty list.
type ClaudeMCPProbe struct{}

// ListMCPServers runs `claude mcp list` and returns the names it emits, one
// per line. Trailing whitespace and blank lines are stripped. An error is
// returned only when the binary is missing or the command exits non-zero;
// an empty-but-successful list returns (nil, nil).
func (ClaudeMCPProbe) ListMCPServers(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "claude", "mcp", "list").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			names = append(names, parseMCPServerName(trimmed))
		}
	}
	return names, nil
}

// parseMCPServerName extracts the server name from a `claude mcp list` output
// line. The current format is "<name>: <url> - <status>"; older versions emit
// just the name. When ": " is present, everything before it is the name.
func parseMCPServerName(line string) string {
	if idx := strings.Index(line, ": "); idx >= 0 {
		return line[:idx]
	}
	return line
}
