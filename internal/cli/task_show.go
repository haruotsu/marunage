package cli

import (
	"github.com/spf13/cobra"
)

func newTaskShowCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show full details of a task.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplementedError{command: "show"}
		},
	}
	_ = configPath
	return cmd
}
