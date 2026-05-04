package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/project"
)

// boardFetcher is the narrow surface newProjectRunCmd needs to read a
// GitHub Projects board. Production wires defaultBoardFetcher; tests
// inject a fake via withProjectRunnerHook so no real gh invocation runs.
type boardFetcher interface {
	Fetch(ctx context.Context, parsed project.ParsedURL) ([]project.BoardItem, error)
}

// defaultBoardFetcher delegates to project.FetchItems with ExecRunner.
type defaultBoardFetcher struct{}

func (defaultBoardFetcher) Fetch(ctx context.Context, parsed project.ParsedURL) ([]project.BoardItem, error) {
	return project.FetchItems(ctx, project.ExecRunner{}, parsed)
}

// projectRunnerFactory builds a boardFetcher for a given board URL.
// Production returns nil (signals "use default"); tests return a fake.
type projectRunnerFactory func(boardURL string) boardFetcher

var projectRunnerHook projectRunnerFactory

// withProjectRunnerHook installs a test-only boardFetcher factory and
// restores the previous factory when t.Cleanup runs.
func withProjectRunnerHook(t interface{ Cleanup(func()) }, f projectRunnerFactory) {
	prev := projectRunnerHook
	projectRunnerHook = f
	t.Cleanup(func() { projectRunnerHook = prev })
}

// activeProjectBoardFetcher returns the configured fetcher, preferring the
// test hook when it is set.
func activeProjectBoardFetcher(boardURL string) boardFetcher {
	if projectRunnerHook != nil {
		return projectRunnerHook(boardURL)
	}
	return defaultBoardFetcher{}
}

// newProjectCmd builds `marunage project` with its subcommands.
func newProjectCmd(_ *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "project",
		Short:        "Manage and run GitHub Projects boards.",
		SilenceUsage: true,
	}
	cmd.AddCommand(newProjectRunCmd())
	return cmd
}

// newProjectRunCmd builds `marunage project run <board-url>`.
// It polls the board, sorts items by phase × date, and dispatches tasks
// one-by-one. [human] tasks block forward progress until they are marked
// Done on the board; when the board is all Done, the command exits 0.
func newProjectRunCmd() *cobra.Command {
	var (
		pollInterval time.Duration
		dryRun       bool
	)
	cmd := &cobra.Command{
		Use:   "run <board-url>",
		Short: "Run tasks from a GitHub Projects board in phase × date order.",
		Long: "project run polls a GitHub Projects board and dispatches tasks one at a\n" +
			"time in phase × date order.\n" +
			"\n" +
			"[human] items are treated as human-gated milestones: the loop pauses\n" +
			"until they are marked Done on the board, then resumes automatically.\n" +
			"\n" +
			"Use --dry-run to print the next action without dispatching or waiting.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			boardURL := args[0]
			parsed, err := project.ParseBoardURL(boardURL)
			if err != nil {
				return fmt.Errorf("invalid board URL %q: %w", boardURL, err)
			}

			fetcher := activeProjectBoardFetcher(boardURL)
			cmd.Printf("Project: %s/%d (kind=%s)\n", parsed.Owner, parsed.Number, parsed.OwnerKind)

			for {
				items, err := fetcher.Fetch(cmd.Context(), parsed)
				if err != nil {
					return fmt.Errorf("fetch board items: %w", err)
				}

				sorted := project.SortByPhaseDate(items)
				item, action := project.NextTask(sorted)

				switch action {
				case project.ActionAllDone:
					cmd.Println("All tasks complete. Project is done.")
					return nil

				case project.ActionWaitHuman:
					cmd.Printf("Waiting for human task: %q (status=%s). Polling every %s.\n",
						item.Title, item.Status, pollInterval)
					if dryRun {
						return nil
					}

				case project.ActionWaitRunning:
					cmd.Printf("Task in progress: %q (status=%s). Polling every %s.\n",
						item.Title, item.Status, pollInterval)
					if dryRun {
						return nil
					}

				case project.ActionDispatch:
					cmd.Printf("Dispatching task: %q (id=%s)\n", item.Title, item.ID)
					if dryRun {
						return nil
					}
					// TODO(PR-101): wire into internal/dispatch to start a cmux
					// workspace for this board item. For now the command polls until
					// the board item moves to Done, which a human or an external
					// process must do.
					cmd.Println("Waiting for task to move to Done on the board...")
				}

				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(pollInterval):
				}
			}
		},
	}
	cmd.Flags().DurationVar(&pollInterval, "interval", 30*time.Second,
		"Polling interval when waiting for task completion or human action.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print the next action without dispatching or polling.")
	return cmd
}
