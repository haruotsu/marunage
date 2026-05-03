package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskFailCmd builds `marunage fail <id>`. Same transition lattice as
// `done` (pending / running / waiting_human -> failed); fires the same
// OnDone mirror hook because both are completion-style transitions from
// the upstream's point of view (the markdown plugin checks the box; a
// future GitHub plugin closes the issue and labels it).
func newTaskFailCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "fail <id>",
		Short:        "Mark a task as failed manually.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return transitionRunner(cmd, args, *configPath, store.StatusFailed,
				func(ctx context.Context, m Mirror, t store.Task) error {
					return m.OnDone(ctx, t)
				})
		},
	}
}
