package doctor

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/haruotsu/marunage/internal/config"
)

// Check is the declarative description of one probe. The registry below is
// a slice of these; adding a new tool is a one-entry change here plus a
// matching install-hint row (install.go).
type Check struct {
	Name string

	// RequiredFor reports whether this check is a hard requirement under
	// the given config. For unconditional tools (claude, cmux, sqlite3,
	// python) it returns true; for source-conditional tools (gh, gws) it
	// inspects cfg.Discovery.SourcesEnabled.
	RequiredFor func(cfg config.Config) bool

	// Eval performs the probe. The Name / Required fields it returns are
	// overwritten by Run from the surrounding Check spec, so eval bodies
	// only need to populate OK / Detail / Version / Hint.
	Eval func(ctx context.Context, in Inputs) CheckOutcome
}

// registeredChecks returns the ordered list of checks Run should execute.
// The order is the order users see in the printed report, so we put the
// always-required tools first and the source-conditional ones after.
func registeredChecks(_ config.Config) []Check {
	return []Check{
		{Name: "claude", RequiredFor: alwaysRequired, Eval: probeBinary("claude", noVersionFloor)},
		{Name: "cmux", RequiredFor: alwaysRequired, Eval: probeBinary("cmux", noVersionFloor)},
		{Name: "herdr", RequiredFor: herdrExecutorSelected, Eval: probeBinary("herdr", noVersionFloor)},
		{Name: "python", RequiredFor: alwaysRequired, Eval: probeBinary("python", pythonVersionFloor)},
		{Name: "sqlite3", RequiredFor: alwaysRequired, Eval: probeBinary("sqlite3", noVersionFloor)},
		{Name: "gh", RequiredFor: githubSourceEnabled, Eval: probeBinary("gh", noVersionFloor)},
		{Name: "gws", RequiredFor: googleSourceEnabled, Eval: probeGWS},
		{Name: "jq", RequiredFor: neverRequired, Eval: probeBinary("jq", noVersionFloor)},
		{Name: "secrets", RequiredFor: alwaysRequired, Eval: probeSecrets},
		{Name: "slack-mcp", RequiredFor: slackSourceEnabled, Eval: probeSlackMCP},
	}
}

// registeredToolNames is the set of names that the install-hint table must
// cover. Used by the test that pins the "no orphan tool" contract.
func registeredToolNames() []string {
	checks := registeredChecks(config.Default())
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.Name)
	}
	return out
}

// alwaysRequired / neverRequired are tiny helpers that keep the registry
// table aligned and self-documenting.
func alwaysRequired(_ config.Config) bool { return true }
func neverRequired(_ config.Config) bool  { return false }

// herdrExecutorSelected reports whether the herdr execution backend is in
// use. herdr is an optional dependency — only required when the operator
// actually selected it via [execution].executor = "herdr"; otherwise its
// absence is fine (cmux/tmux/local run without it).
func herdrExecutorSelected(cfg config.Config) bool {
	return cfg.Execution.Executor == "herdr"
}

// githubSourceEnabled reports whether any github-flavored source is on in
// cfg.Discovery.SourcesEnabled. Anything starting with "github" counts so
// future variants (github_issue, github_pr, ...) automatically promote gh
// to required without a code change.
func githubSourceEnabled(cfg config.Config) bool {
	for _, s := range cfg.Discovery.SourcesEnabled {
		if strings.HasPrefix(s, "github") {
			return true
		}
	}
	return false
}

// googleSourceEnabled reports whether any google-suite source is enabled.
// gmail / gcal / gdrive / google_* / gws all flip gws to required.
func googleSourceEnabled(cfg config.Config) bool {
	for _, s := range cfg.Discovery.SourcesEnabled {
		if isGoogleSourceName(s) {
			return true
		}
	}
	return false
}

func isGoogleSourceName(s string) bool {
	switch {
	case s == "gmail",
		s == "gcal",
		s == "gdrive",
		s == "calendar",
		s == "gws":
		return true
	case strings.HasPrefix(s, "google"),
		strings.HasPrefix(s, "g_"):
		return true
	}
	return false
}

// slackSourceEnabled reports whether the slack or slack:reaction source is
// enabled. Both rely on Slack MCP access via `claude -p`, so either name
// promotes the slack-mcp check to required.
func slackSourceEnabled(cfg config.Config) bool {
	for _, s := range cfg.Discovery.SourcesEnabled {
		if s == "slack" || s == "slack:reaction" {
			return true
		}
	}
	return false
}

// probeSlackMCP checks whether the Slack MCP server is configured in Claude
// Code. When the MCP probe is nil (no probe wired) and slack is required, we
// report a failure so the user knows the check couldn't run rather than
// silently passing.
func probeSlackMCP(ctx context.Context, in Inputs) CheckOutcome {
	if in.MCP == nil {
		return CheckOutcome{
			OK:     false,
			Detail: "MCP probe not available; cannot verify Slack MCP configuration",
			Hint:   "run `claude mcp add slack ...` to configure the Slack MCP server",
		}
	}
	servers, err := in.MCP.ListMCPServers(ctx)
	if err != nil {
		return CheckOutcome{
			OK:     false,
			Detail: fmt.Sprintf("claude mcp list failed: %v", err),
			Hint:   "ensure the claude binary is on PATH and working",
		}
	}
	for _, s := range servers {
		lo := strings.ToLower(s)
		// Match bare "slack" (old format) or "<provider> slack" (e.g. "claude.ai Slack").
		if lo == "slack" || strings.HasSuffix(lo, " slack") {
			return CheckOutcome{
				OK:     true,
				Detail: "Slack MCP server configured in Claude Code",
			}
		}
	}
	return CheckOutcome{
		OK:     false,
		Detail: "Slack MCP server not found in `claude mcp list` output",
		Hint:   "run `claude mcp add slack <transport>` to configure the Slack MCP server",
	}
}

// versionFloor is the minimum acceptable version for a tool. nil means
// "any version is acceptable as long as the binary exists".
type versionFloor *struct {
	major int
	minor int
}

var (
	noVersionFloor     versionFloor = nil
	pythonVersionFloor versionFloor = &struct {
		major int
		minor int
	}{3, 11}
)

// probeBinary returns an Eval body that performs a generic "binary on PATH
// + optional version floor" check. The version floor is checked only when
// floor != nil; otherwise we report success as soon as the binary is found.
// probeGWS extends the plain binary check with an auth-state probe. gws being
// on PATH is necessary but not sufficient: the gmail/calendar sources shell
// out to it and fail at runtime when no login has happened. So when the binary
// is present and a GWS probe is wired, we also verify a credential exists and
// otherwise fail with the exact (scope-narrowed) login command — surfacing the
// gap at `doctor` time instead of mid-discovery.
func probeGWS(ctx context.Context, in Inputs) CheckOutcome {
	base := probeBinary("gws", noVersionFloor)(ctx, in)
	if !base.OK || in.GWS == nil {
		return base
	}
	authed, account, err := in.GWS.Authenticated(ctx)
	if err != nil {
		base.Detail = fmt.Sprintf("%s; auth state could not be verified: %v", base.Detail, err)
		return base
	}
	if !authed {
		return CheckOutcome{
			OK:      false,
			Version: base.Version,
			Detail:  base.Detail + " but not authenticated",
			Hint:    "run `gws auth login --services gmail,calendar,tasks` (narrow scopes avoid the invalid_scope error from gws's full set)",
		}
	}
	detail := base.Detail + "; authenticated"
	if account != "" {
		detail = fmt.Sprintf("%s as %s", detail, account)
	}
	return CheckOutcome{OK: true, Version: base.Version, Detail: detail}
}

func probeBinary(name string, floor versionFloor) func(context.Context, Inputs) CheckOutcome {
	return func(ctx context.Context, in Inputs) CheckOutcome {
		path, ok := in.Runner.LookPath(name)
		if !ok {
			return CheckOutcome{
				OK:     false,
				Detail: fmt.Sprintf("%s not found on PATH", name),
			}
		}
		raw, err := in.Runner.Version(ctx, name)
		if err != nil {
			return CheckOutcome{
				OK:      false,
				Detail:  fmt.Sprintf("%s present at %s but --version failed: %v", name, path, err),
				Version: "",
			}
		}
		ver := extractSemver(raw)
		if floor != nil {
			major, minor, ok := parseMajorMinor(ver)
			if !ok {
				return CheckOutcome{
					OK:      false,
					Detail:  fmt.Sprintf("%s present at %s but version string %q not recognized", name, path, raw),
					Version: ver,
				}
			}
			if major < floor.major || (major == floor.major && minor < floor.minor) {
				return CheckOutcome{
					OK:      false,
					Detail:  fmt.Sprintf("%s %d.%d found; need >= %d.%d", name, major, minor, floor.major, floor.minor),
					Version: fmt.Sprintf("%d.%d", major, minor),
				}
			}
		}
		return CheckOutcome{
			OK:      true,
			Detail:  fmt.Sprintf("%s found at %s", name, path),
			Version: ver,
		}
	}
}

// semverPattern matches the first X.Y or X.Y.Z run inside an arbitrary
// version banner. We use FindString to pick out e.g. "3.12.1" from
// "Python 3.12.1\n" or "2.55.0" from "gh version 2.55.0 (2024-01-01)".
var semverPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

func extractSemver(raw string) string {
	return semverPattern.FindString(strings.TrimSpace(raw))
}

func parseMajorMinor(s string) (int, int, bool) {
	if s == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}
