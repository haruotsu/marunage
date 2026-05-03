package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskPromoteCmd builds `marunage promote <id>`. Only skipped rows
// transition (skipped -> pending); every other status is rejected by
// store.TransitionStatus. Fires the OnReopen mirror hook because from the
// upstream's point of view a promoted task re-enters the queue exactly
// like a reopened one.
func newTaskPromoteCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "promote <id>",
		Short:        "Promote a skipped task back to pending.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return transitionRunner(cmd, args, *configPath, store.StatusPending,
				map[string]bool{store.StatusSkipped: true},
				func(ctx context.Context, m Mirror, t store.Task) error {
					return m.OnReopen(ctx, t)
				})
		},
	}
}
