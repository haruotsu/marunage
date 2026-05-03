package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskShowCmd builds `marunage show <id>`. id is parsed as a positive
// int64; an unparseable value is rejected before the DB is opened so the
// user sees a flag-name-aware diagnostic instead of "task -1 not found".
//
// Missing rows surface as exit 1 with "Task #<id> not found." on stderr.
// We handle this by returning a sentinel and printing the message
// ourselves with cobra.SilenceErrors so we do not double-print "Error: ".
//
// --json mirrors the list subcommand's wire shape so consumers can pipe
// `marunage show 42 --json | jq '.title'` without a special case.
func newTaskShowCmd(configPath *string) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:          "show <id>",
		Short:        "Show full details of a task.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("id %q: must be an integer", args[0])
			}
			if id <= 0 {
				return fmt.Errorf("id %q: must be a positive integer", args[0])
			}

			repo, closer, err := activeTaskRepoFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			task, err := repo.Get(cmd.Context(), id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					// Print the human message ourselves and silence the
					// default "Error: ..." banner so the stderr line is
					// the friendly one promised by docs/requirement.md.
					cmd.SilenceErrors = true
					fmt.Fprintf(cmd.ErrOrStderr(), "Task #%d not found.\n", id)
					return errTaskNotFound
				}
				return translateRepoError(err)
			}

			if asJSON {
				return writeShowJSON(cmd.OutOrStdout(), task)
			}
			return writeShowText(cmd.OutOrStdout(), task)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the result as JSON.")

	return cmd
}

// errTaskNotFound is the sentinel show returns after printing the friendly
// "Task #<id> not found." message. cobra still treats a non-nil RunE error
// as exit 1, which is what we want; SilenceErrors keeps it from prefixing
// "Error: ".
var errTaskNotFound = errors.New("show: task not found")

// writeShowText renders a key: value pair per line, replacing empty
// strings with "(empty)" so the user can see omitted fields at a glance.
// Times are formatted RFC3339 UTC; zero times become "(empty)" too so the
// formatting is uniform.
func writeShowText(w io.Writer, t store.Task) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// row prints "<key>: <value>", normalising the empty string to
	// "(empty)" so the reader can tell an absent field from a literal
	// blank one.
	row := func(key, value string) {
		if value == "" {
			value = "(empty)"
		}
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", key, value)
	}

	// jsonView gives us the same RFC3339 / null treatment for times that
	// --json uses, so the two outputs cannot drift apart.
	jsonView := taskFromStore(t)
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}

	row("id", fmt.Sprintf("%d", t.ID))
	row("source", t.Source)
	row("external_id", t.ExternalID)
	row("external_url", t.ExternalURL)
	row("title", t.Title)
	row("body", t.Body)
	row("notes", t.Notes)
	row("status", t.Status)
	row("judgment_reason", t.JudgmentReason)
	row("priority", fmt.Sprintf("%d", t.Priority))
	row("lock_key", t.LockKey)
	row("cwd", t.CWD)
	row("ws", t.WS)
	row("result_summary", t.ResultSummary)
	row("reflection", t.Reflection)
	row("created_at", deref(jsonView.CreatedAt))
	row("updated_at", deref(jsonView.UpdatedAt))
	row("started_at", deref(jsonView.StartedAt))
	row("completed_at", deref(jsonView.CompletedAt))

	return tw.Flush()
}

func writeShowJSON(w io.Writer, t store.Task) error {
	data, err := json.MarshalIndent(taskFromStore(t), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
