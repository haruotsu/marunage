package cli

import (
	"github.com/spf13/cobra"
)

func newTaskListCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks (defaults to pending and running).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplementedError{command: "list"}
		},
	}
	_ = configPath
	return cmd
}
