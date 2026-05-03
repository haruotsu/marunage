package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskReopenCmd builds `marunage reopen <id>`. Restricts the lattice to
// done -> pending and failed -> pending; the skipped case lives in
// `promote` so the operator picks the verb that matches their intent.
// Fires OnReopen so the markdown plugin (PR-50) can flip the upstream
// checkbox back to unchecked.
func newTaskReopenCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "reopen <id>",
		Short:        "Reopen a done or failed task back to pending.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return transitionRunner(cmd, args, *configPath, store.StatusPending,
				map[string]bool{
					store.StatusDone:   true,
					store.StatusFailed: true,
				},
				func(ctx context.Context, m Mirror, t store.Task) error {
					return m.OnReopen(ctx, t)
				})
		},
	}
}
