// Package cli builds the marunage CLI surface using spf13/cobra.
//
// PR-02 wires every Phase 1 subcommand defined in docs/requirement.md as a
// stub returning notImplementedError. The real behavior lands in subsequent
// PRs, but `marunage --help` already renders the complete UX skeleton.
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/version"
)

// Execute runs the marunage CLI with args, writing output to stdout/stderr,
// and returns the process exit code. Returning rather than calling os.Exit
// keeps the function easy to drive from tests.
func Execute(args []string, stdout, stderr io.Writer) int {
	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	if err := cmd.Execute(); err != nil {
		return 1
	}
	return 0
}

// notImplementedError is returned by stub subcommands. cobra will prefix it
// with "Error: " when printing to stderr.
type notImplementedError struct {
	command string
}

func (e notImplementedError) Error() string {
	return fmt.Sprintf("marunage %s: not yet implemented (see docs/pr_split_plan.md)", e.command)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "marunage",
		Short: "Autonomous task-execution OSS that delegates inbound work to Claude sessions.",
		Long: "marunage runs an OODA loop on top of Claude Code sessions managed by cmux:\n" +
			"a Discovery layer polls Slack/Gmail/GitHub/etc., a Queue layer triages and\n" +
			"prioritises tasks in a local SQLite store, and an Execution layer dispatches\n" +
			"each task into its own interactive Claude session — observable and reversible\n" +
			"at every step. See docs/requirement.md for the full design.",
		Version: version.Version(),
		// Suppress the auto-printed usage banner on RunE errors so the
		// "not yet implemented" message is not buried under help text.
		SilenceUsage: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")

	// --config lets every subcommand point at an alternate config.toml.
	// It's a persistent flag so subcommands inherit it without re-declaring.
	configPath := defaultConfigPath()
	root.PersistentFlags().StringVar(&configPath, "config", configPath, "Path to the marunage config.toml")

	for _, c := range buildLeafStubs() {
		root.AddCommand(c)
	}
	root.AddCommand(newDaemonCmd(&configPath))
	root.AddCommand(newLoopCmd(&configPath))
	root.AddCommand(newConfigCmd(&configPath))
	root.AddCommand(newDoctorCmd(&configPath))
	root.AddCommand(newInitCmd(&configPath))
	root.AddCommand(newSetupCmd(&configPath))
	root.AddCommand(newDispatchCmd(&configPath))
	root.AddCommand(newTaskAddCmd(&configPath))
	root.AddCommand(newTaskListCmd(&configPath))
	root.AddCommand(newTaskShowCmd(&configPath))
	root.AddCommand(newTaskDoneCmd(&configPath))
	root.AddCommand(newTaskFailCmd(&configPath))
	root.AddCommand(newTaskPromoteCmd(&configPath))
	root.AddCommand(newTaskReopenCmd(&configPath))
	root.AddCommand(newTaskRmCmd(&configPath))
	root.AddCommand(newTaskRenderCmd(&configPath))
	root.AddCommand(newTaskExportCmd(&configPath))
	root.AddCommand(newTaskCleanCmd(&configPath))
	root.AddCommand(newReaperCmd(&configPath))
	root.AddCommand(newTaskStatusCmd(&configPath))
	root.AddCommand(newDiscoverCmd())
	root.AddCommand(newWebCmd(&configPath))

	return root
}

// stubSpec describes a leaf subcommand that PR-02 ships as a stub.
type stubSpec struct {
	use   string // cobra Use string, may include argument hints
	short string
}

func buildLeafStubs() []*cobra.Command {
	specs := []stubSpec{
		{"run-all", "Dispatch every pending task in priority order."},
		{"open", "Render view.md and open it in cmux's markdown viewer."},
		{"notify", "Send completion / failure / waiting_human notifications."},
		{"review", "Review past skipped tasks for triage feedback."},
	}

	cmds := make([]*cobra.Command, 0, len(specs))
	for _, s := range specs {
		cmds = append(cmds, newStubCmd(s, ""))
	}
	return cmds
}

// newStubCmd builds a leaf subcommand whose RunE returns notImplementedError.
// parentPath, when non-empty, is prepended to the displayed command path
// (e.g. "daemon" so the error reads "marunage daemon start: ...").
func newStubCmd(spec stubSpec, parentPath string) *cobra.Command {
	name := commandNameFromUse(spec.use)
	displayed := name
	if parentPath != "" {
		displayed = parentPath + " " + name
	}
	return &cobra.Command{
		Use:   spec.use,
		Short: spec.short,
		RunE: func(_ *cobra.Command, _ []string) error {
			return notImplementedError{command: displayed}
		},
	}
}

// newDaemonCmd is implemented in daemon.go; the stub it once held now
// lives there alongside start / stop / status which manage the
// pidfile-backed background `marunage loop` process.

// commandNameFromUse extracts the bare command name from a cobra Use string
// such as "add <title>" or "doctor [--fix]".
func commandNameFromUse(use string) string {
	fields := strings.Fields(use)
	if len(fields) == 0 {
		return use
	}
	return fields[0]
}
