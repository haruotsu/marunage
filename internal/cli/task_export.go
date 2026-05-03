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

// writeExportMarkdown renders one H2 section per task — title +
// metadata list + body — for the "paste into a retrospective doc" use
// case. Empty result emits "No tasks." so the operator can tell a
// working command from one that exited silently.
//
// taskFromStore bridges to the same RFC3339 / null treatment the JSON
// path uses so timestamps cannot drift between the two formats.
func writeExportMarkdown(w io.Writer, rows []store.Task) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No tasks.")
		return err
	}
	for i, r := range rows {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := renderMarkdownTask(w, r); err != nil {
			return err
		}
	}
	return nil
}

// renderMarkdownTask is the per-row writer extracted so the if/err
// chain only appears once. Optional fields are skipped when empty so
// a minimal task does not leave bare "- key: " stubs.
func renderMarkdownTask(w io.Writer, r store.Task) error {
	view := taskFromStore(r)
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	kv := func(format string, args ...any) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}
	if err := kv("## #%d %s\n\n", r.ID, r.Title); err != nil {
		return err
	}
	if err := kv("- source: %s\n", r.Source); err != nil {
		return err
	}
	if err := kv("- status: %s\n", r.Status); err != nil {
		return err
	}
	if err := kv("- priority: %d\n", r.Priority); err != nil {
		return err
	}
	for _, opt := range []struct{ key, value string }{
		{"external_id", r.ExternalID},
		{"external_url", r.ExternalURL},
		{"created_at", deref(view.CreatedAt)},
		{"completed_at", deref(view.CompletedAt)},
	} {
		if opt.value == "" {
			continue
		}
		if err := kv("- %s: %s\n", opt.key, opt.value); err != nil {
			return err
		}
	}
	if r.Body != "" {
		if err := kv("\n%s\n", r.Body); err != nil {
			return err
		}
	}
	return nil
}
