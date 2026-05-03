package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskAddCmd builds `marunage add <title>` per docs/requirement.md
// "コマンド `marunage`" row "add". The flag plumbing intentionally keeps
// no business logic of its own beyond:
//
//   - body source resolution: --body / --body-stdin / --body-edit are
//     mutually exclusive and feed a single string into store.Task.Body.
//   - --notes is validated as JSON here (and not in the repo) so users
//     see "invalid JSON" against the flag name instead of a wrapped
//     SQLite CHECK violation. The repo's CHECK is the second line of
//     defense for callers that bypass the CLI.
//   - exit codes follow the doctor.go convention: typed sentinels
//     translate to a clear stderr message; everything else flows through
//     cobra's default RunE error path (SilenceUsage stays on so the
//     usage banner does not bury the message).
func newTaskAddCmd(configPath *string) *cobra.Command {
	var (
		body       string
		bodyStdin  bool
		bodyEdit   bool
		source     string
		cwd        string
		priority   int
		notes      string
		bodyWasSet bool
	)

	cmd := &cobra.Command{
		Use:          "add <title>",
		Short:        "Add a task manually to the queue.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			title := args[0]

			// Body sourcing: at most one of --body / --body-stdin /
			// --body-edit. Tracking bodyWasSet via Changed() lets us
			// distinguish "user passed --body=''" (legal: empty body)
			// from "user did not pass --body".
			bodyWasSet = cmd.Flags().Changed("body")
			selected := 0
			if bodyWasSet {
				selected++
			}
			if bodyStdin {
				selected++
			}
			if bodyEdit {
				selected++
			}
			if selected > 1 {
				return errors.New("--body, --body-stdin, and --body-edit are mutually exclusive")
			}

			resolvedBody := body
			switch {
			case bodyStdin:
				data, err := io.ReadAll(activeStdinReader())
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				resolvedBody = string(data)
			case bodyEdit:
				edited, err := readBodyFromEditor()
				if err != nil {
					return fmt.Errorf("editor: %w", err)
				}
				resolvedBody = edited
			}

			// Validate --notes as JSON before opening the DB; the repo
			// CHECK is the second line of defense.
			if notes != "" && !json.Valid([]byte(notes)) {
				return fmt.Errorf("--notes: invalid JSON")
			}

			repo, closer, err := activeTaskRepoFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			id, err := repo.Insert(cmd.Context(), store.Task{
				Source:   source,
				Title:    title,
				Body:     resolvedBody,
				Notes:    notes,
				Priority: priority,
				CWD:      cwd,
			})
			if err != nil {
				return translateRepoError(err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Created task #%d: %s\n", id, title)
			return nil
		},
	}

	cmd.Flags().StringVar(&body, "body", "", "Task body text (mutually exclusive with --body-stdin / --body-edit).")
	cmd.Flags().BoolVar(&bodyStdin, "body-stdin", false, "Read the task body from standard input.")
	cmd.Flags().BoolVar(&bodyEdit, "body-edit", false, "Open $EDITOR (or vi) to compose the task body.")
	cmd.Flags().StringVar(&source, "source", "manual", "Source label for this task (e.g. manual, gmail, slack).")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory the dispatcher should run this task in.")
	cmd.Flags().IntVar(&priority, "priority", 0, "Task priority (higher runs first).")
	cmd.Flags().StringVar(&notes, "notes", "", "Free-form structured notes as a JSON string.")

	return cmd
}

// readBodyFromEditor invokes $EDITOR (falling back to vi) on a temp file,
// returns its contents, and removes the temp. The temp is created in the
// OS tempdir with mode 0600 so a body containing PII is not world-readable
// during the brief editing window.
func readBodyFromEditor() (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	dir, err := os.MkdirTemp("", "marunage-edit-")
	if err != nil {
		return "", fmt.Errorf("mkdir temp: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "TASK_BODY")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}

	c := exec.Command(editor, path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("run %s: %w", editor, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read edited body: %w", err)
	}
	return string(data), nil
}
