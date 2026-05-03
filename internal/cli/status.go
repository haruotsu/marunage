package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// statusFilterStatuses is the actionable monitoring set: a row in any
// other state belongs to `marunage list` (pending) or to a post-mortem
// flow (done / failed / skipped). Showing them on the watch screen
// dilutes the "what is the daemon doing right now" signal that the
// status command exists for (docs/requirement.md, PR-61).
var statusFilterStatuses = []string{store.StatusRunning, store.StatusWaitingHuman}

// clearScreen is the standard ANSI clear-and-home sequence: ESC [ 2 J
// clears the entire screen, ESC [ H moves the cursor to (1,1). Together
// they let the watch loop redraw at row 0 without the terminal scrolling
// previous frames off-screen.
const clearScreen = "\x1b[2J\x1b[H"

// defaultStatusInterval is the per-tick refresh cadence when --interval
// is not passed. One second balances "obviously live" against "does not
// peg the SQLite handle" — `marunage list` is a single SELECT, so the
// per-tick cost is negligible, but going faster than a human can read
// is wasteful.
const defaultStatusInterval = time.Second

// statusTickerFactory returns the channel a tick lands on plus a stop
// function the loop must call on exit. Modelled as a function value so
// tests can hand in a buffered channel they push into manually instead
// of waiting for real wall-clock time.
type statusTickerFactory func(d time.Duration) (<-chan time.Time, func())

// statusTickerHook overrides the ticker factory the cobra command uses.
// nil falls through to the production wall-clock ticker.
var statusTickerHook statusTickerFactory

// withStatusTicker installs f as the active ticker factory and restores
// the prior hook on cleanup. Mirrors the seam pattern used by
// withTaskRepoFactory.
func withStatusTicker(t interface{ Cleanup(func()) }, f statusTickerFactory) {
	prev := statusTickerHook
	statusTickerHook = f
	t.Cleanup(func() { statusTickerHook = prev })
}

func activeStatusTicker() statusTickerFactory {
	if statusTickerHook != nil {
		return statusTickerHook
	}
	return defaultStatusTicker
}

// defaultStatusTicker is the production ticker: a real *time.Ticker
// whose channel + Stop are surfaced behind the statusTickerFactory
// shape so the loop does not have to know which implementation it is
// running against.
func defaultStatusTicker(d time.Duration) (<-chan time.Time, func()) {
	tk := time.NewTicker(d)
	return tk.C, tk.Stop
}

// statusContextHook overrides the context the watch loop runs against.
// Used by tests to substitute a cancellable context for cobra's default
// context.Background() (which would otherwise block the loop forever).
// nil falls through to the cobra-supplied context wrapped with a
// signal.NotifyContext so production honours Ctrl+C.
var statusContextHook context.Context

func withStatusContext(t interface{ Cleanup(func()) }, ctx context.Context) {
	prev := statusContextHook
	statusContextHook = ctx
	t.Cleanup(func() { statusContextHook = prev })
}

// newTaskStatusCmd builds `marunage status [--watch] [--interval D] [--json]`.
// One-shot mode prints the actionable rows and exits; --watch loops on
// the injected ticker until the context is cancelled (Ctrl+C in
// production, an explicit cancel in tests).
func newTaskStatusCmd(configPath *string) *cobra.Command {
	var (
		watch    bool
		interval time.Duration
		asJSON   bool
	)

	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show running / waiting_human tasks (use --watch to stream).",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if watch && asJSON {
				return fmt.Errorf("--watch and --json are mutually exclusive")
			}
			if watch && interval <= 0 {
				return fmt.Errorf("--interval must be positive (got %s)", interval)
			}

			repo, closer, err := activeTaskRepoFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			if !watch {
				return writeStatusOnce(cmd.OutOrStdout(), cmd.Context(), repo, asJSON)
			}

			ctx := watchContext(cmd.Context())
			return runStatusWatch(ctx, cmd.OutOrStdout(), repo, statusWatchOpts{
				interval:  interval,
				newTicker: activeStatusTicker(),
			})
		},
	}

	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Refresh the table on every interval until interrupted.")
	cmd.Flags().DurationVar(&interval, "interval", defaultStatusInterval, "Refresh interval in --watch mode.")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the result as JSON for scripting (one-shot only).")

	return cmd
}

// watchContext returns the cancellable context the watch loop should
// use. The test hook short-circuits production wiring; otherwise we
// derive a child context that cancels on the first Ctrl+C so the loop
// shuts down cleanly. The signal handler is intentionally NOT undone on
// return because the loop owns the process lifetime in --watch mode and
// any further SIGINT after the loop returns should hit Go's default
// terminator.
func watchContext(parent context.Context) context.Context {
	if statusContextHook != nil {
		return statusContextHook
	}
	ctx, _ := signal.NotifyContext(parent, os.Interrupt)
	return ctx
}

// statusWatchOpts collects the dependencies runStatusWatch needs. Kept
// as a struct rather than functional options because every caller (the
// cobra command + every test) sets all fields anyway, and the struct
// shape is easier to scan in a test table.
type statusWatchOpts struct {
	interval  time.Duration
	newTicker statusTickerFactory
}

// runStatusWatch renders the status table once on entry, then on every
// tick from the injected ticker until ctx is cancelled. A read error
// aborts the loop so the operator notices a degraded daemon instead of
// re-rendering the last good frame indefinitely.
//
// The initial render is intentional: a one-second wait before the first
// frame would feel like the command had hung. We pay the extra read so
// the table appears immediately.
func runStatusWatch(ctx context.Context, w io.Writer, repo taskRepo, opts statusWatchOpts) error {
	factory := opts.newTicker
	if factory == nil {
		factory = defaultStatusTicker
	}
	tickC, stop := factory(opts.interval)
	defer stop()

	if err := writeStatusFrame(w, ctx, repo); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tickC:
			if err := writeStatusFrame(w, ctx, repo); err != nil {
				return err
			}
		}
	}
}

// writeStatusFrame emits one full screen: the clear sequence followed
// by the table. The clear is part of the same Write so a partial
// terminal flush cannot leave the cursor on a half-cleared screen.
func writeStatusFrame(w io.Writer, ctx context.Context, repo taskRepo) error {
	rows, err := repo.List(ctx, store.ListFilter{Statuses: statusFilterStatuses})
	if err != nil {
		return translateRepoError(err)
	}
	var buf strings.Builder
	buf.WriteString(clearScreen)
	if err := writeStatusText(&buf, rows); err != nil {
		return err
	}
	_, err = io.WriteString(w, buf.String())
	return err
}

// writeStatusOnce is the one-shot path: read once, render once, exit.
// Lives next to writeStatusFrame because the two share writeStatusText
// / writeStatusJSON underneath; only the surrounding wrapper differs.
func writeStatusOnce(w io.Writer, ctx context.Context, repo taskRepo, asJSON bool) error {
	rows, err := repo.List(ctx, store.ListFilter{Statuses: statusFilterStatuses})
	if err != nil {
		return translateRepoError(err)
	}
	if asJSON {
		return writeStatusJSON(w, rows)
	}
	return writeStatusText(w, rows)
}

// writeStatusText renders the table. The empty case prints a single
// "No active workspaces." line so the operator can distinguish "the
// daemon is idle" from "the command crashed before printing anything".
func writeStatusText(w io.Writer, rows []store.Task) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No active workspaces.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tSource\tStatus\tWS\tSummary\tTitle"); err != nil {
		return err
	}
	for _, r := range rows {
		ws := r.WS
		if ws == "" {
			ws = "(none)"
		}
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Source, r.Status, ws, statusSummary(r), r.Title); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// statusSummary picks the cell text for the Summary column. A non-empty
// ResultSummary passes through with embedded whitespace collapsed so the
// tabwriter alignment cannot be broken by a multi-line summary; an empty
// one falls back to a placeholder that matches the row's lifecycle stage
// — "(running)" while dispatch is still working, "(waiting)" once it has
// escalated to a human — so the column never reads as a blank gap and
// never mislabels a stalled row as actively running.
func statusSummary(t store.Task) string {
	if t.ResultSummary != "" {
		return collapseWhitespace(t.ResultSummary)
	}
	if t.Status == store.StatusWaitingHuman {
		return "(waiting)"
	}
	return "(running)"
}

// collapseWhitespace folds any \n, \r, \t, or \f in s into a single
// space so the result fits on one tabwriter row. Leading / trailing
// whitespace is trimmed so the column stays left-aligned even when the
// upstream summary had a trailing newline.
func collapseWhitespace(s string) string {
	if !strings.ContainsAny(s, "\n\r\t\f") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == '\f' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = r == ' '
	}
	return strings.TrimSpace(b.String())
}

// writeStatusJSON marshals rows via the canonical taskJSON shape (same
// wire contract as `marunage list --json`). An empty result still
// serialises as "[]" so callers can `len(arr)` without a nil check.
func writeStatusJSON(w io.Writer, rows []store.Task) error {
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
