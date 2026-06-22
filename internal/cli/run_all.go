package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/store"
)

// newRunAllCmd builds `marunage run-all`, which drains the whole pending queue
// rather than the single bounded pass `dispatch` performs.
func newRunAllCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "run-all",
		Short:        "Dispatch every pending task in priority order.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAll(cmd.Context(), *configPath, cmd.OutOrStdout())
		},
	}
}

// runAll repeatedly invokes the dispatcher until the pending queue stops
// shrinking. Each pass dispatches up to core.max_parallel rows (the store
// caps a single List, so one pass cannot always drain a long queue); the loop
// stops when no pending rows remain or a pass makes no progress — the latter
// meaning every survivor is blocked (lock contention / cwd gate), which a
// further pass would not change.
func runAll(ctx context.Context, configPath string, out io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", configPath, err)
	}
	maxParallel := cfg.Core.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 1
	}

	repo, repoCloser, err := activeTaskRepoFactory()(ctx, configPath)
	if err != nil {
		return err
	}
	defer func() { _ = repoCloser() }()

	d, dispCloser, err := activeDispatcherFactory()(ctx, configPath)
	if err != nil {
		return err
	}
	defer func() { _ = dispCloser() }()

	total := 0
	for {
		before, err := pendingCount(ctx, repo)
		if err != nil {
			return err
		}
		if before == 0 {
			break
		}
		if err := d.Run(ctx, dispatch.RunOptions{MaxParallel: maxParallel}); err != nil {
			return err
		}
		after, err := pendingCount(ctx, repo)
		if err != nil {
			return err
		}
		progressed := before - after
		if progressed <= 0 {
			break
		}
		total += progressed
	}
	_, _ = fmt.Fprintf(out, "Dispatched %d task(s).\n", total)
	return nil
}

func pendingCount(ctx context.Context, repo taskRepo) (int, error) {
	rows, err := repo.List(ctx, store.ListFilter{Statuses: []string{store.StatusPending}})
	if err != nil {
		return 0, translateRepoError(err)
	}
	return len(rows), nil
}
