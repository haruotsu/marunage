package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/doctor"
)

// newDoctorCmd builds the `marunage doctor` subcommand. The actual probe
// logic lives in internal/doctor; this file is just CLI plumbing so the
// flag layer stays thin and the doctor package can be reused from the Web
// UI without dragging cobra along.
//
// Tests inject fake Runner / SecretsProbe / OSDetector implementations by
// calling withDoctorRuntime; production callers leave doctorRuntimeHook
// nil and get the real os/exec / filesystem-backed implementations.
func newDoctorCmd(configPath *string) *cobra.Command {
	var (
		fix    bool
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "doctor [--fix] [--json]",
		Short: "Check that claude / cmux / sqlite3 / gh / gws / jq are installed and usable.",
		Long: "doctor probes the external tools marunage depends on and reports the\n" +
			"first failure that would block `marunage setup` or a real run.\n\n" +
			"Required tools: claude, cmux, python (>= 3.11), sqlite3, plus at\n" +
			"least one usable secret-storage backend.\n\n" +
			"Optional tools become required once you enable the matching source\n" +
			"in discovery.sources_enabled: gh for GitHub, gws for Gmail/Calendar.\n\n" +
			"--fix prints install hints (brew / apt / dnf) for missing tools but\n" +
			"never executes them; review and run them yourself.",
		// doctor is the entry-point users hit when nothing works yet, so
		// we suppress cobra's "use --help" usage banner on RunE errors:
		// the actionable next step is in the printed report itself, not
		// in the synopsis.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}

			rt := activeDoctorRuntime()
			rep := doctor.Run(cmd.Context(), rt.Inputs(cfg))

			if asJSON {
				data, err := doctor.MarshalJSON(rep)
				if err != nil {
					return fmt.Errorf("marshal report: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
			} else {
				printTextReport(cmd.OutOrStdout(), rep)
			}

			if fix {
				printFixHints(cmd.OutOrStdout(), rep, rt.Family())
			}

			if !rep.OK {
				// Returning an error after we've already printed the
				// human report would cause cobra to emit "Error: ..."
				// on top, which buries the table. Use SilenceErrors and
				// signal failure via a typed sentinel cobra knows to
				// turn into exit code 1.
				cmd.SilenceErrors = true
				return errDoctorFailed
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&fix, "fix", false, "Print install hints for missing tools (does not execute).")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the report as JSON for Web UI / CI consumption.")

	return cmd
}

// errDoctorFailed signals "one or more required checks failed" without
// printing a redundant "Error: ..." banner over the human report.
var errDoctorFailed = fmt.Errorf("doctor: one or more required checks failed")

// doctorRuntimeOverride is the test injection seam. Each non-nil field
// replaces the corresponding production implementation. Production code
// (newDoctorCmd) sees doctorRuntimeHook == nil and falls through to
// productionDoctorRuntime.
type doctorRuntimeOverride struct {
	Inputs func(cfg config.Config) doctor.Inputs
	Family func() doctor.OSFamily
}

// doctorRuntimeHook is the package-private slot tests use via
// withDoctorRuntime. Assigning here directly is intentionally not
// thread-safe; tests run in a single goroutine before Execute and call
// withDoctorRuntime which restores the previous value via t.Cleanup.
var doctorRuntimeHook *doctorRuntimeOverride

// withDoctorRuntime swaps in test fakes and restores the prior hook on
// test completion. The closure-of-Cleanup pattern keeps tests isolated
// even if they fail mid-way.
func withDoctorRuntime(t interface{ Cleanup(func()) }, override doctorRuntimeOverride) {
	prev := doctorRuntimeHook
	doctorRuntimeHook = &override
	t.Cleanup(func() { doctorRuntimeHook = prev })
}

// activeDoctorRuntime returns the current runtime: the test override
// when one is installed, otherwise the production implementation.
func activeDoctorRuntime() doctorRuntime {
	if doctorRuntimeHook != nil {
		return doctorRuntime{
			inputs: doctorRuntimeHook.Inputs,
			family: doctorRuntimeHook.Family,
		}
	}
	return doctorRuntime{}
}

// doctorRuntime bundles the dependencies newDoctorCmd needs. The zero
// value resolves to the production implementations via Inputs / Family;
// tests substitute either or both via withDoctorRuntime.
type doctorRuntime struct {
	inputs func(cfg config.Config) doctor.Inputs
	family func() doctor.OSFamily
}

func (r doctorRuntime) Inputs(cfg config.Config) doctor.Inputs {
	if r.inputs != nil {
		return r.inputs(cfg)
	}
	runner := doctor.ExecRunner{}
	return doctor.Inputs{
		Cfg:     cfg,
		Runner:  runner,
		Secrets: doctor.FileSecretsProbe{Runner: runner},
		MCP:     doctor.ClaudeMCPProbe{},
		OS:      doctor.RealOSDetector{},
	}
}

func (r doctorRuntime) Family() doctor.OSFamily {
	if r.family != nil {
		return r.family()
	}
	return doctor.RealOSDetector{}.Family()
}

// printTextReport renders a one-line-per-check human view. The format is
// stable enough for users to grep but is NOT a programmatic interface;
// machine consumers should use --json.
func printTextReport(w io.Writer, rep Report) {
	for _, c := range rep.Checks {
		marker := "OK"
		if !c.OK {
			marker = "FAIL"
		}
		req := "optional"
		if c.Required {
			req = "required"
		}
		_, _ = fmt.Fprintf(w, "[%s] %-8s (%s)  %s\n", marker, c.Name, req, c.Detail)
		if c.Hint != "" {
			_, _ = fmt.Fprintf(w, "         hint: %s\n", c.Hint)
		}
	}
	if rep.OK {
		_, _ = fmt.Fprintln(w, "\nAll required checks passed.")
	} else {
		_, _ = fmt.Fprintln(w, "\nOne or more required checks failed. Re-run with --fix for install hints.")
	}
}

func printFixHints(w io.Writer, rep Report, family doctor.OSFamily) {
	hints := doctor.FixHints(rep, family)
	if len(hints) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\nInstall hints (%s) — review then run yourself:\n", family)
	for _, h := range hints {
		_, _ = fmt.Fprintf(w, "  %s\n", h)
	}
}

// Report / CheckOutcome aliases so the printer functions can refer to the
// doctor types without forcing every caller of this file to import the
// internal/doctor package. They keep the printer signatures readable.
type (
	Report       = doctor.Report
	CheckOutcome = doctor.CheckOutcome
)
