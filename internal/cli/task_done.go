package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskDoneCmd builds `marunage done <id>`. The transition matrix is
// pending / running / waiting_human -> done; everything else is rejected
// by store.TransitionStatus.
func newTaskDoneCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "done <id>",
		Short:        "Mark a task as done manually.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return transitionRunner(cmd, args, *configPath, store.StatusDone, nil,
				func(ctx context.Context, m Mirror, t store.Task) error {
					return m.OnDone(ctx, t)
				})
		},
	}
}
