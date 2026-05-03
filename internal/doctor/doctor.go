// Package doctor implements `marunage doctor`: it probes the small set of
// external tools marunage depends on (claude, cmux, sqlite3, python, plus
// the source-conditional gh / gws / jq) and reports a single boolean
// "ready or not" answer along with per-tool detail.
//
// Architecture:
//
//   - The CLI layer (cmd/marunage/doctor.go) is intentionally thin. It
//     instantiates real implementations of Runner, SecretsProbe, and
//     OSDetector and hands them to Run; everything testable lives here so
//     tests can inject fakes and never touch the real $PATH.
//   - The list of checks is a registry slice (see checks.go). Adding a new
//     tool is one entry plus one row in the install-hint table; the
//     TestInstallHints_CoverEveryRegisteredTool test pins that contract.
//   - Source-conditional checks (gh / gws) inspect cfg.Discovery.SourcesEnabled
//     to decide whether a missing binary is a required failure or merely a
//     recorded optional. This mirrors the table in docs/requirement.md
//     "marunage doctor — 周辺ツール導入チェック".
//
// PR-32 deliberately does NOT depend on internal/secrets — that package is
// being authored in parallel (PR-30). Backend availability is detected via
// file probes (~/.marunage/secrets.age, ~/.marunage/secrets/, $PATH for
// `pass`) so the two PRs can land in either order without a merge dance.
package doctor

import (
	"context"
	"errors"

	"github.com/haruotsu/marunage/internal/config"
)

// errMissingBinary is returned by a Runner.Version implementation when the
// requested binary is not on PATH. It's exported via doctor_test.go's
// fakeRunner so tests can mirror the real behavior.
var errMissingBinary = errors.New("binary not found on PATH")

// Inputs bundles the dependencies Run needs. Grouping them in a struct
// (rather than ten positional arguments) keeps the CLI wiring readable and
// gives tests a single place to set up scenarios.
type Inputs struct {
	Cfg     config.Config
	Runner  Runner
	Secrets SecretsProbe
	OS      OSDetector
}

// CheckOutcome is the per-tool record stored in the Report. The fields are
// chosen so that both the human-readable text output and the --json shape
// can be derived from this single struct.
type CheckOutcome struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
	Version  string `json:"version"`
	Hint     string `json:"hint"`
}

// Report is the aggregate result returned by Run. OK is true iff every
// check whose Required flag came back true also has OK == true; an
// optional check that fails leaves Report.OK alone.
type Report struct {
	OK     bool           `json:"ok"`
	Checks []CheckOutcome `json:"checks"`
}

// Run executes every registered check against in.Runner / in.Secrets and
// returns the aggregate Report. The caller is responsible for printing the
// human or JSON view; Run itself never writes to stdout/stderr.
func Run(ctx context.Context, in Inputs) Report {
	checks := registeredChecks(in.Cfg)

	rep := Report{OK: true}
	for _, c := range checks {
		out := c.Eval(ctx, in)
		out.Name = c.Name
		out.Required = c.RequiredFor(in.Cfg)
		rep.Checks = append(rep.Checks, out)
		if out.Required && !out.OK {
			rep.OK = false
		}
	}
	return rep
}
