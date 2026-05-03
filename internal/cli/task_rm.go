package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskRmCmd builds `marunage rm <id>`. Unlike the transition commands,
// rm deletes the row outright (no status machinery) and uses the
// pre-delete snapshot for the mirror's OnDelete hook so plugins still
// have external_id available to find the upstream record.
//
// Mirror failure is reported but does NOT undo the delete: the local row
// is the source of truth and an out-of-sync upstream is the operator's
// problem to address out of band, just like the transition commands.
func newTaskRmCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "rm <id>",
		Short:        "Remove a task and propagate the deletion to the source mirror.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}

			repo, closer, err := activeTaskRepoFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			// Snapshot before delete so OnDelete still has external_id
			// and source available for the upstream lookup.
			task, err := repo.Get(cmd.Context(), id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					printNotFoundAndExit(cmd, id)
					return errTaskCommandFailed
				}
				return translateRepoError(err)
			}

			if err := repo.Delete(cmd.Context(), id); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					printNotFoundAndExit(cmd, id)
					return errTaskCommandFailed
				}
				return translateRepoError(err)
			}

			mirror, err := activeMirrorFactory()(cmd.Context(), *configPath)
			if err != nil {
				return fmt.Errorf("mirror: %w", err)
			}
			if err := mirror.OnDelete(cmd.Context(), task); err != nil {
				return fmt.Errorf("mirror sync: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Removed task #%d\n", id)
			return nil
		},
	}
}
