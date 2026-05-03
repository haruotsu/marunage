package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner abstracts the two operations doctor needs to perform against an
// external binary: locate it on PATH, and capture its `--version` stdout.
// Splitting these out lets tests inject canned answers without ever
// invoking a real binary (TestRunner_NotInvokedAgainstRealBinary).
type Runner interface {
	// LookPath returns the absolute path to name on PATH together with a
	// "found" bool. A nil error path is intentional; the bool form keeps
	// callers from confusing "missing binary" with "broken probe".
	LookPath(name string) (path string, ok bool)

	// Version captures the stdout of `name --version`. Implementations
	// should also accept the few binaries that print their version to a
	// non-standard flag (sqlite3 wants `-version`, for instance); the
	// real implementation handles those quirks below.
	Version(ctx context.Context, name string) (string, error)
}

// ExecRunner is the production Runner. It defers PATH lookup to
// os/exec.LookPath and runs each binary's documented version flag with
// the parent context's deadline so a hung tool cannot wedge `marunage
// doctor` indefinitely.
type ExecRunner struct{}

func (ExecRunner) LookPath(name string) (string, bool) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	return p, true
}

func (ExecRunner) Version(ctx context.Context, name string) (string, error) {
	flag := versionFlagFor(name)
	cmd := exec.CommandContext(ctx, name, flag)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Some binaries (notably python on certain distros) print to
		// stderr and exit non-zero on `--version` aliases; surface the
		// captured output anyway so the caller can still parse a
		// version string out of it.
		if len(out) == 0 {
			return "", fmt.Errorf("%s %s: %w", name, flag, err)
		}
	}
	return strings.TrimSpace(string(out)), nil
}

// versionFlagFor returns the per-binary version flag. Most tools accept
// `--version`; sqlite3 famously wants `-version` (single dash) and would
// otherwise drop into its REPL.
func versionFlagFor(name string) string {
	if name == "sqlite3" {
		return "-version"
	}
	return "--version"
}
