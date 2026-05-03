package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// newTaskCleanCmd builds `marunage clean`. The subcommand walks every
// task whose ws column is non-empty, asks cmux for the live workspace
// set, and reports / clears the diff.
//
// docs/pr_split_plan.md PR-22 splits responsibility with PR-44's reaper:
// clean is the **manual, CLI-driven sweep** an operator runs after a
// crash or a long-running cmux restart; reaper is the in-process loop
// that PR-44 ships later. The two converge on the same SetWorkspace(id,
// "") primitive but differ in cadence and trigger.
//
// Default is dry-run: no DB writes happen unless --apply is passed. The
// asymmetry is deliberate — a stale ws looks like a real workspace until
// you check, and the cost of a wrong --apply (silently dropping a live
// reference) outweighs the cost of running the command twice. --apply is
// the affirmative gate.
func newTaskCleanCmd(configPath *string) *cobra.Command {
	var (
		apply  bool
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:          "clean",
		Short:        "Reap dead workspace references (dry-run by default; pass --apply to mutate).",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, closer, err := activeTaskRepoFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			lister, err := activeWorkspaceListerFactory()(cmd.Context(), *configPath)
			if err != nil {
				return fmt.Errorf("workspace lister: %w", err)
			}
			ids, err := lister.ListWorkspaceIDs(cmd.Context())
			if err != nil {
				return fmt.Errorf("list workspaces: %w", err)
			}
			alive := make(map[string]struct{}, len(ids))
			for _, id := range ids {
				alive[id] = struct{}{}
			}

			rows, err := repo.List(cmd.Context(), store.ListFilter{})
			if err != nil {
				return translateRepoError(err)
			}

			var orphans []store.Task
			for _, r := range rows {
				if r.WS == "" {
					continue
				}
				if _, ok := alive[r.WS]; ok {
					continue
				}
				orphans = append(orphans, r)
			}

			applied := 0
			if apply {
				for _, o := range orphans {
					if err := repo.SetWorkspace(cmd.Context(), o.ID, ""); err != nil {
						return fmt.Errorf("clear ws on task #%d: %w", o.ID, translateRepoError(err))
					}
					applied++
				}
			}

			if asJSON {
				return writeCleanJSON(cmd.OutOrStdout(), orphans, apply, applied)
			}
			return writeCleanText(cmd.OutOrStdout(), orphans, apply, applied)
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "Actually clear orphan ws references (default is dry-run).")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit a structured report on stdout.")

	return cmd
}

// writeCleanText prints one line per orphan plus a summary so the
// operator can grep either layer. Empty result emits "No orphan
// workspace references." mirroring `list`'s "No tasks." convention.
func writeCleanText(w io.Writer, orphans []store.Task, apply bool, applied int) error {
	if len(orphans) == 0 {
		_, err := fmt.Fprintln(w, "No orphan workspace references.")
		return err
	}
	verb := "would clear"
	if apply {
		verb = "cleared"
	}
	for _, o := range orphans {
		if _, err := fmt.Fprintf(w, "task #%d: %s ws %s\n", o.ID, verb, o.WS); err != nil {
			return err
		}
	}
	if apply {
		_, err := fmt.Fprintf(w, "Cleared %d orphan workspace reference(s).\n", applied)
		return err
	}
	_, err := fmt.Fprintf(w, "Found %d orphan workspace reference(s); pass --apply to clear.\n", len(orphans))
	return err
}

// cleanReportEntry is one orphan in the JSON report. Field names use
// snake_case to stay consistent with taskJSON.
type cleanReportEntry struct {
	ID int64  `json:"id"`
	WS string `json:"ws"`
}

// cleanReport is the --json shape. Always-present keys ("orphans" /
// "applied" / "dry_run") let consumers branch on the booleans without a
// nil check on the slice.
type cleanReport struct {
	Orphans []cleanReportEntry `json:"orphans"`
	Applied int                `json:"applied"`
	DryRun  bool               `json:"dry_run"`
}

// writeCleanJSON serialises the report. The orphans slice is non-nil
// even when empty so consumers can rely on len(arr) without a null check.
func writeCleanJSON(w io.Writer, orphans []store.Task, apply bool, applied int) error {
	entries := make([]cleanReportEntry, 0, len(orphans))
	for _, o := range orphans {
		entries = append(entries, cleanReportEntry{ID: o.ID, WS: o.WS})
	}
	report := cleanReport{
		Orphans: entries,
		Applied: applied,
		DryRun:  !apply,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
