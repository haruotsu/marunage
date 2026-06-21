package doctor

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// OSFamily is the coarse OS bucket that drives the install-hint table.
// We only distinguish what changes the install command (brew vs apt vs
// dnf vs "do it yourself"); finer distinctions belong in the per-tool
// docs we link from OSFamilyOther.
type OSFamily int

const (
	OSFamilyOther OSFamily = iota
	OSFamilyDarwin
	OSFamilyDebian
	OSFamilyFedora
)

// String makes the family print readably in test failures. The lowercase
// form intentionally matches what users would type in a bug report.
func (f OSFamily) String() string {
	switch f {
	case OSFamilyDarwin:
		return "darwin"
	case OSFamilyDebian:
		return "debian-like"
	case OSFamilyFedora:
		return "fedora-like"
	}
	return "other"
}

// OSDetector returns the OSFamily for the host. The interface exists so
// tests can pin a family without depending on runtime.GOOS / /etc/os-release.
type OSDetector interface {
	Family() OSFamily
}

// RealOSDetector is the production OSDetector. It consults runtime.GOOS
// first and only opens /etc/os-release on linux, where the apt/dnf split
// actually matters.
type RealOSDetector struct {
	OSReleasePath string // overridden in tests; defaults to /etc/os-release
	GOOS          string // overridden in tests; defaults to runtime.GOOS
}

func (d RealOSDetector) Family() OSFamily {
	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	switch goos {
	case "darwin":
		return OSFamilyDarwin
	case "linux":
		return linuxFamilyFromOSRelease(d.osReleasePath())
	}
	return OSFamilyOther
}

func (d RealOSDetector) osReleasePath() string {
	if d.OSReleasePath != "" {
		return d.OSReleasePath
	}
	return "/etc/os-release"
}

// linuxFamilyFromOSRelease parses ID / ID_LIKE out of /etc/os-release and
// classifies the distribution. Any read error or unrecognized ID falls
// back to OSFamilyOther (which prints generic hints).
func linuxFamilyFromOSRelease(path string) OSFamily {
	f, err := os.Open(path)
	if err != nil {
		return OSFamilyOther
	}
	defer func() { _ = f.Close() }()

	id := ""
	idLike := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "ID="):
			id = unquote(strings.TrimPrefix(line, "ID="))
		case strings.HasPrefix(line, "ID_LIKE="):
			idLike = unquote(strings.TrimPrefix(line, "ID_LIKE="))
		}
	}
	combined := strings.ToLower(id + " " + idLike)
	switch {
	case containsWord(combined, "debian"), containsWord(combined, "ubuntu"):
		return OSFamilyDebian
	case containsWord(combined, "fedora"), containsWord(combined, "rhel"), containsWord(combined, "centos"):
		return OSFamilyFedora
	}
	return OSFamilyOther
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func containsWord(haystack, word string) bool {
	for _, f := range strings.Fields(haystack) {
		if f == word {
			return true
		}
	}
	return false
}

// installHintRow is one row in the per-tool install table. We index by
// (tool name, family) and fall back to a documented upstream URL for
// OSFamilyOther so users on Alpine / Arch / NixOS still get a pointer.
type installHintRow struct {
	pkgs map[OSFamily]string // family -> package name; missing means "see upstream"
	url  string              // upstream documentation URL for the "other" fallback
}

// installHints is the single source of truth for what `--fix` prints.
// TestInstallHints_CoverEveryRegisteredTool makes sure no registered tool
// is missing a row here.
var installHints = map[string]installHintRow{
	"claude": {
		pkgs: map[OSFamily]string{
			OSFamilyDarwin: "claude",
		},
		url: "https://docs.anthropic.com/en/docs/claude-code/setup",
	},
	"cmux": {
		pkgs: map[OSFamily]string{
			OSFamilyDarwin: "manaflow-ai/cmux/cmux",
		},
		url: "https://github.com/manaflow-ai/cmux",
	},
	"herdr": {
		// herdr ships its own installer (curl | sh) rather than a package; the
		// upstream URL covers every OS family.
		pkgs: map[OSFamily]string{},
		url:  "https://herdr.dev/",
	},
	"python": {
		pkgs: map[OSFamily]string{
			OSFamilyDarwin: "python@3.12",
			OSFamilyDebian: "python3",
			OSFamilyFedora: "python3",
		},
		url: "https://www.python.org/downloads/",
	},
	"sqlite3": {
		pkgs: map[OSFamily]string{
			OSFamilyDarwin: "sqlite",
			OSFamilyDebian: "sqlite3",
			OSFamilyFedora: "sqlite",
		},
		url: "https://www.sqlite.org/download.html",
	},
	"gh": {
		pkgs: map[OSFamily]string{
			OSFamilyDarwin: "gh",
			OSFamilyDebian: "gh",
			OSFamilyFedora: "gh",
		},
		url: "https://cli.github.com/",
	},
	"gws": {
		pkgs: map[OSFamily]string{},
		url:  "https://github.com/google/gws",
	},
	"jq": {
		pkgs: map[OSFamily]string{
			OSFamilyDarwin: "jq",
			OSFamilyDebian: "jq",
			OSFamilyFedora: "jq",
		},
		url: "https://stedolan.github.io/jq/",
	},
	"secrets": {
		// secrets is not a binary; the "fix" is `marunage setup`.
		pkgs: map[OSFamily]string{},
		url:  "run `marunage setup` to configure a secret backend",
	},
	"slack-mcp": {
		// slack-mcp is not a binary; the fix is adding the MCP server to Claude Code.
		pkgs: map[OSFamily]string{},
		url:  "run `claude mcp add slack <transport>` to configure the Slack MCP server",
	},
}

// installHintFor returns the install hint string for tool on family. The
// second return value is false only when tool is not in the install table
// at all (which the test guards against); a tool that has no package on a
// given family falls back to the upstream URL but still returns ok=true.
func installHintFor(tool string, family OSFamily) (string, bool) {
	row, ok := installHints[tool]
	if !ok {
		return "", false
	}
	if pkg, ok := row.pkgs[family]; ok {
		switch family {
		case OSFamilyDarwin:
			return fmt.Sprintf("brew install %s", pkg), true
		case OSFamilyDebian:
			return fmt.Sprintf("sudo apt install %s", pkg), true
		case OSFamilyFedora:
			return fmt.Sprintf("sudo dnf install %s", pkg), true
		}
	}
	if tool == "secrets" {
		return row.url, true
	}
	return fmt.Sprintf("install %s from %s", tool, row.url), true
}

// FixHints returns the ordered list of human-readable install lines for
// every failing required check in rep. Optional checks that happen to be
// failing get a hint too, prefixed with "(optional)" so users can ignore
// them if they don't plan to use that source.
func FixHints(rep Report, family OSFamily) []string {
	var hints []string
	for _, c := range rep.Checks {
		if c.OK {
			continue
		}
		hint, ok := installHintFor(c.Name, family)
		if !ok {
			continue
		}
		prefix := ""
		if !c.Required {
			prefix = "(optional) "
		}
		hints = append(hints, fmt.Sprintf("%s%s: %s", prefix, c.Name, hint))
	}
	return hints
}
