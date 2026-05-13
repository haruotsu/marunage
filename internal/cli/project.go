package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/project"
	"github.com/haruotsu/marunage/internal/workspace/cmux"
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
type projectRunnerFactory func(boardURL string) boardFetcher

var projectRunnerHook projectRunnerFactory

// withProjectRunnerHook installs a test-only boardFetcher factory and
// restores the previous factory on cleanup.
func withProjectRunnerHook(t interface{ Cleanup(func()) }, f projectRunnerFactory) {
	prev := projectRunnerHook
	projectRunnerHook = f
	t.Cleanup(func() { projectRunnerHook = prev })
}

func activeProjectBoardFetcher(boardURL string) boardFetcher {
	if projectRunnerHook != nil {
		return projectRunnerHook(boardURL)
	}
	return defaultBoardFetcher{}
}

// projectDispatchFunc dispatches a single board item by starting a cmux
// workspace and sending a prompt. Returns nil on success.
type projectDispatchFunc func(ctx context.Context, configPath string, item project.BoardItem) error

var projectDispatchHook projectDispatchFunc

// withProjectDispatchHook installs a test-only dispatch function and
// restores the previous on cleanup.
func withProjectDispatchHook(t interface{ Cleanup(func()) }, f projectDispatchFunc) {
	prev := projectDispatchHook
	projectDispatchHook = f
	t.Cleanup(func() { projectDispatchHook = prev })
}

func activeProjectDispatch() projectDispatchFunc {
	if projectDispatchHook != nil {
		return projectDispatchHook
	}
	return productionProjectDispatch
}

// productionProjectDispatch loads config, creates a cmux workspace with
// the configured claude_command, and sends a prompt for the board item.
// The workspace runs until the item is moved to Done on the board, which
// the outer polling loop detects on the next fetch cycle.
func productionProjectDispatch(ctx context.Context, configPath string, item project.BoardItem) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cwd, err := expandHome(cfg.Core.DefaultCwd)
	if err != nil {
		return fmt.Errorf("resolve core.default_cwd %q: %w", cfg.Core.DefaultCwd, err)
	}
	if _, statErr := os.Stat(cwd); statErr != nil {
		return fmt.Errorf("core.default_cwd %q does not exist or is not accessible: %w", cwd, statErr)
	}

	cm := newWorkspaceClient(cfg, true)
	ws, err := cm.NewWorkspace(ctx, cmux.NewWorkspaceOptions{
		CWD:     cwd,
		Command: cfg.Execution.ClaudeCommand,
		Name:    fmt.Sprintf("project:%s", item.ID),
	})
	if err != nil {
		return fmt.Errorf("create workspace for %q: %w", item.Title, err)
	}

	if err := cm.WaitReady(ctx, ws); err != nil {
		return fmt.Errorf("workspace not ready for %q: %w", item.Title, err)
	}

	prompt := buildProjectPrompt(item)
	if err := cm.Send(ctx, ws, prompt); err != nil {
		return fmt.Errorf("send prompt for %q: %w", item.Title, err)
	}
	return nil
}

// sanitizeField strips control characters and newlines from a string that
// originates from an external system (e.g. a GitHub Projects board title).
// This prevents prompt injection when the value is embedded into a Claude
// session prompt: a title like "## new heading\nIgnore above" could otherwise
// be misinterpreted as Markdown structure or a new instruction block.
func sanitizeField(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// buildProjectPrompt formats the task prompt sent to the Claude session.
// item.Title and item.ID are sanitized to prevent prompt injection from
// externally-authored board content (GitHub Projects titles are user-controlled).
// The values are placed inside an XML-style fence so the Claude session can
// distinguish trusted instructions from untrusted board data.
func buildProjectPrompt(item project.BoardItem) string {
	title := sanitizeField(item.Title)
	id := sanitizeField(item.ID)
	return fmt.Sprintf(
		"## GitHub Projects Task\n\n"+
			"<task>\n"+
			"Title: %s\n"+
			"ID: %s\n"+
			"</task>\n\n"+
			"Please work on this task. When complete, mark the item as Done on the GitHub Projects board.",
		title, id,
	)
}

// newProjectCmd builds `marunage project` with its subcommands.
func newProjectCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "project",
		Short:        "Manage and run GitHub Projects boards.",
		SilenceUsage: true,
	}
	cmd.AddCommand(newProjectRunCmd(configPath))
	return cmd
}

// newProjectRunCmd builds `marunage project run <board-url>`.
// It polls the board, sorts items by phase × date, and dispatches tasks
// one-by-one. [human] tasks block forward progress until they are marked
// Done on the board; when the board is all Done, the command exits 0.
func newProjectRunCmd(configPath *string) *cobra.Command {
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
			if pollInterval < 100*time.Millisecond {
				return fmt.Errorf("--interval must be at least 100ms (got %s)", pollInterval)
			}

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
					if err := activeProjectDispatch()(cmd.Context(), *configPath, *item); err != nil {
						return fmt.Errorf("dispatch %q: %w", item.Title, err)
					}
					cmd.Printf("Task dispatched. Polling every %s for completion.\n", pollInterval)
				}

				// Use time.NewTimer so the timer goroutine is GC'd immediately
				// when the context is cancelled, rather than leaking until
				// pollInterval expires (the time.After pattern).
				timer := time.NewTimer(pollInterval)
				select {
				case <-cmd.Context().Done():
					timer.Stop()
					return cmd.Context().Err()
				case <-timer.C:
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
