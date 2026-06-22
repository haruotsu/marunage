// Package cli builds the marunage CLI surface using spf13/cobra. Every Phase 1
// subcommand defined in docs/requirement.md is wired here; `marunage --help`
// renders the complete UX skeleton and each leaf runs real logic.
package cli

import (
	"io"

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
		// Suppress the auto-printed usage banner on RunE errors so a command's
		// error message is not buried under help text.
		SilenceUsage: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")

	// --config lets every subcommand point at an alternate config.toml.
	// It's a persistent flag so subcommands inherit it without re-declaring.
	configPath := defaultConfigPath()
	root.PersistentFlags().StringVar(&configPath, "config", configPath, "Path to the marunage config.toml")

	root.AddCommand(newRunAllCmd(&configPath))
	root.AddCommand(newOpenCmd(&configPath))
	root.AddCommand(newNotifyCmd(&configPath))
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
	root.AddCommand(newDiscoverCmd(&configPath))
	root.AddCommand(newWebCmd(&configPath))
	root.AddCommand(newSkillsCmd(&configPath))
	root.AddCommand(newTaskReviewCmd(&configPath))
	root.AddCommand(newJournalCmd(&configPath))
	root.AddCommand(newProjectCmd(&configPath))

	return root
}

// newDaemonCmd is implemented in daemon.go; the stub it once held now
// lives there alongside start / stop / status which manage the
// pidfile-backed background `marunage loop` process.
