package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskExportCmd builds `marunage export`. Unlike `marunage list`, export
// is the archival path: the default filter is "everything" so a single run
// captures the full history. Output goes to stdout in JSON (default) or
// Markdown form so the operator can pipe to a file or attach to a ticket.
//
// The JSON shape reuses taskJSON from task_repo.go so the wire contract
// stays identical to `list --json` / `show --json`.
func newTaskExportCmd(configPath *string) *cobra.Command {
	var (
		format   string
		statuses []string
		sources  []string
	)

	cmd := &cobra.Command{
		Use:          "export",
		Short:        "Export every task in JSON or Markdown.",
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
			})
			if err != nil {
				return translateRepoError(err)
			}

			switch format {
			case "json":
				return writeExportJSON(cmd.OutOrStdout(), rows)
			case "markdown":
				return writeExportMarkdown(cmd.OutOrStdout(), rows)
			default:
				return fmt.Errorf("--format %q: must be one of json, markdown", format)
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or markdown.")
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "Status filter (repeatable). Defaults to all.")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "Source filter (repeatable).")

	return cmd
}

// writeExportJSON serialises rows via the canonical taskJSON shape. An
// empty result still serialises as "[]" so consumers can rely on len(arr)
// without a nil check.
func writeExportJSON(w io.Writer, rows []store.Task) error {
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

// writeExportMarkdown renders one H2 section per task with a short
// metadata table followed by the body. The shape is a "retrospective
// document" rather than a 1:1 mirror of taskJSON: archival readers want
// to skim titles, not parse keys.
//
// Empty result emits "No tasks." so the operator can tell a working
// command from one that exited silently — same convention as `list`.
//
// taskFromStore is the bridge to the same RFC3339 / null treatment the
// JSON path uses, so timestamps cannot drift between the two formats.
func writeExportMarkdown(w io.Writer, rows []store.Task) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No tasks.")
		return err
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for i, r := range rows {
		view := taskFromStore(r)
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "## #%d %s\n\n", r.ID, r.Title); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "- source: %s\n", r.Source); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "- status: %s\n", r.Status); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "- priority: %d\n", r.Priority); err != nil {
			return err
		}
		if r.ExternalID != "" {
			if _, err := fmt.Fprintf(w, "- external_id: %s\n", r.ExternalID); err != nil {
				return err
			}
		}
		if r.ExternalURL != "" {
			if _, err := fmt.Fprintf(w, "- external_url: %s\n", r.ExternalURL); err != nil {
				return err
			}
		}
		if s := deref(view.CreatedAt); s != "" {
			if _, err := fmt.Fprintf(w, "- created_at: %s\n", s); err != nil {
				return err
			}
		}
		if s := deref(view.CompletedAt); s != "" {
			if _, err := fmt.Fprintf(w, "- completed_at: %s\n", s); err != nil {
				return err
			}
		}
		if r.Body != "" {
			if _, err := fmt.Fprintf(w, "\n%s\n", r.Body); err != nil {
				return err
			}
		}
	}
	return nil
}
