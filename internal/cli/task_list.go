package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskListCmd builds `marunage list`. The default filter is pending +
// running so a user running the command in the middle of an OODA loop sees
// only actionable rows; passing --status overrides the default entirely
// (e.g. `--status done` shows only completed rows).
//
// --json is included from day one so the Web UI / `gh`-style tooling can
// consume `marunage list --json` without screen-scraping the tabwriter
// output. The text format remains stable enough to grep but is NOT a
// programmatic interface.
func newTaskListCmd(configPath *string) *cobra.Command {
	var (
		statuses []string
		sources  []string
		limit    int
		asJSON   bool
	)

	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List tasks (defaults to pending and running).",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, closer, err := activeTaskRepoFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			rows, err := repo.List(cmd.Context(), store.ListFilter{
				Statuses: statuses,
				Sources:  sources,
				Limit:    limit,
			})
			if err != nil {
				return translateRepoError(err)
			}

			if asJSON {
				return writeListJSON(cmd.OutOrStdout(), rows)
			}
			return writeListText(cmd.OutOrStdout(), rows)
		},
	}

	cmd.Flags().StringSliceVar(&statuses, "status", []string{store.StatusPending, store.StatusRunning},
		"Status filter (repeatable). Defaults to pending,running.")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "Source filter (repeatable).")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum rows to return (0 = no limit).")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the result as JSON for scripting.")

	return cmd
}

// writeListText prints rows in a tabwriter-aligned table. The empty case
// produces a single "No tasks." line so users can tell a working command
// from one that exited silently.
func writeListText(w io.Writer, rows []store.Task) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No tasks.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tSource\tStatus\tPriority\tTitle"); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n",
			r.ID, r.Source, r.Status, r.Priority, r.Title); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// writeListJSON marshals rows via the canonical taskJSON shape. An empty
// result still serialises as "[]" rather than "null" so consumers can rely
// on `len(arr)` without a nil check.
func writeListJSON(w io.Writer, rows []store.Task) error {
	out := make([]taskJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, taskFromStore(r))
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
