package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// reviewNowHook lets tests inject a deterministic "now" for --since parsing.
var reviewNowHook func() time.Time

// withReviewNow installs a fixed clock for the duration of t.
func withReviewNow(t interface{ Cleanup(func()) }, now time.Time) {
	prev := reviewNowHook
	reviewNowHook = func() time.Time { return now }
	t.Cleanup(func() { reviewNowHook = prev })
}

func reviewNow() time.Time {
	if reviewNowHook != nil {
		return reviewNowHook()
	}
	return time.Now()
}

// newTaskReviewCmd replaces the PR-02 stub for `marunage review`.
// It lists skipped tasks (optionally bounded by --since) and can emit
// a frequency report of recurring skip reasons via --report.
func newTaskReviewCmd(configPath *string) *cobra.Command {
	var (
		since  string
		asJSON bool
		report bool
	)

	cmd := &cobra.Command{
		Use:          "review [--since Xd]",
		Short:        "Review past skipped tasks for triage feedback.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, closer, err := activeTaskRepoFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			filter := store.ListFilter{
				Statuses: []string{store.StatusSkipped},
			}
			if since != "" {
				threshold, parseErr := parseSinceDuration(since)
				if parseErr != nil {
					return fmt.Errorf("--since %q: %w", since, parseErr)
				}
				filter.CreatedAfter = threshold
			}

			rows, err := repo.List(cmd.Context(), filter)
			if err != nil {
				return translateRepoError(err)
			}

			if asJSON {
				return writeReviewJSON(cmd.OutOrStdout(), rows)
			}
			if report {
				return writeReviewReport(cmd.OutOrStdout(), rows)
			}
			return writeReviewText(cmd.OutOrStdout(), rows)
		},
	}

	cmd.Flags().StringVar(&since, "since", "",
		`Only show tasks skipped within this window (e.g. "7d", "30d", "24h"). Empty means all time.`)
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit result as JSON.")
	cmd.Flags().BoolVar(&report, "report", false,
		"Show a frequency report of recurring skip reasons.")

	return cmd
}

// parseSinceDuration parses a human-friendly duration string (e.g. "7d",
// "30d", "24h") and returns the earliest time that falls within the window
// relative to reviewNow().
//
// Supported suffixes: "d" (days) and anything time.ParseDuration accepts.
func parseSinceDuration(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n := 0
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &n); err != nil {
			return time.Time{}, fmt.Errorf("invalid day count %q", s)
		}
		if n <= 0 {
			return time.Time{}, fmt.Errorf("day count must be positive, got %q", s)
		}
		return reviewNow().Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, err
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("duration must be positive, got %q", s)
	}
	return reviewNow().Add(-d), nil
}

// writeReviewText prints skipped tasks in a tabwriter-aligned table.
func writeReviewText(w io.Writer, rows []store.Task) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No skipped tasks found.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tSource\tTitle\tReason"); err != nil {
		return err
	}
	for _, r := range rows {
		reason := r.JudgmentReason
		if len(reason) > 60 {
			reason = reason[:57] + "..."
		}
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n",
			r.ID, r.Source, r.Title, reason); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\nUse `marunage promote <id>` to re-queue a skipped task.\n")
	return err
}

// writeReviewJSON serialises rows as a JSON array.
func writeReviewJSON(w io.Writer, rows []store.Task) error {
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

// reasonCount tallies occurrences of each unique judgment_reason.
type reasonCount struct {
	reason string
	count  int
}

// writeReviewReport prints a frequency table of recurring skip reasons and
// then the full task list so the reviewer has both the summary and the detail
// in one pass.
func writeReviewReport(w io.Writer, rows []store.Task) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No skipped tasks found.")
		return err
	}

	freq := make(map[string]int)
	for _, r := range rows {
		if r.JudgmentReason != "" {
			freq[r.JudgmentReason]++
		}
	}

	counts := make([]reasonCount, 0, len(freq))
	for reason, n := range freq {
		counts = append(counts, reasonCount{reason: reason, count: n})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].count != counts[j].count {
			return counts[i].count > counts[j].count
		}
		return counts[i].reason < counts[j].reason
	})

	if _, err := fmt.Fprintln(w, "=== Skip Reason Frequency Report ==="); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "Count\tReason"); err != nil {
		return err
	}
	for _, rc := range counts {
		if _, err := fmt.Fprintf(tw, "%d\t%s\n", rc.count, rc.reason); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	return writeReviewText(w, rows)
}
