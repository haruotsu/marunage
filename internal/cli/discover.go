package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/source/markdown"
)

// newDiscoverCmd builds the `marunage discover` command. PR-70 ships only
// the `--once --source <name>` form: a synchronous one-shot that resolves
// the source through the plugin registry and dumps List output as JSON to
// stdout. The daemon loop, kvstate integration, parallelism, and the
// triage hand-off all live in PR-71+; this command's contract is "give
// me the raw list right now" so PR-71 can layer scheduling on top
// without the CLI shape changing.
//
// Why required flags rather than a default --source: while only the
// markdown built-in exists, defaulting to it would silently mask a bug
// the moment PR-80 (Gmail) lands and a user types `marunage discover` in
// muscle memory. Forcing --source up-front makes the choice explicit and
// future-proofs the help text.
func newDiscoverCmd() *cobra.Command {
	var (
		once       bool
		sourceName string
		files      []string
	)
	cmd := &cobra.Command{
		Use:           "discover",
		Short:         "Run the Discovery layer once and emit tasks as JSON.",
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !once {
				// PR-71 owns the loop; nudging callers to be explicit
				// stops a future "discover with no flags ran the daemon
				// loop on the foreground" surprise.
				return fmt.Errorf("--once is required: PR-71 will introduce the daemon loop")
			}
			if sourceName == "" {
				return fmt.Errorf("--source is required")
			}
			return runDiscoverOnce(cmd, sourceName, files)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run the source's list step exactly once and exit.")
	cmd.Flags().StringVar(&sourceName, "source", "", "Source plugin name (required; e.g. markdown).")
	cmd.Flags().StringArrayVar(&files, "file", nil,
		"For sources that read local files (markdown), at least one path. May be repeated.")
	return cmd
}

// runDiscoverOnce wires the registry lookup, source-specific configuration,
// and JSON serialisation. Pulled out of the cobra closure so tests could in
// principle swap the registry constructor without touching cobra; today
// they exercise it through Execute(...) end-to-end.
func runDiscoverOnce(cmd *cobra.Command, sourceName string, files []string) error {
	r := source.NewRegistry()
	// Built-ins register at command time rather than at package init so a
	// test importing this package does not get a global side effect — and
	// so failures (e.g. embedded manifest drift) attach to the user-
	// visible command, not to a TestMain we do not own.
	switch sourceName {
	case "markdown":
		if len(files) == 0 {
			return fmt.Errorf("source markdown: --file is required (at least one path)")
		}
		if err := markdown.RegisterBuiltin(r, markdown.WithFiles(files...)); err != nil {
			return fmt.Errorf("register markdown: %w", err)
		}
	default:
		// Register-by-name keeps the registry small (one entry per call
		// rather than every built-in eagerly), which means an unknown
		// name surfaces as ErrPluginNotFound from r.Get below — exactly
		// the typed error the brief asks us to surface to the user.
	}

	plugin, err := r.Get(sourceName)
	if err != nil {
		if errors.Is(err, source.ErrPluginNotFound) {
			return fmt.Errorf("unknown source %q (built-in plugins: %v)", sourceName, builtinNames())
		}
		return err
	}

	tasks, err := plugin.List(context.Background())
	if err != nil {
		return fmt.Errorf("list %s: %w", sourceName, err)
	}

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(tasksToJSON(tasks))
}

// builtinNames returns the static list of built-ins this PR knows about.
// Hard-coded rather than derived from a registry because the discover
// command builds the registry lazily and the user-facing error needs to
// list every option, not only those a particular invocation registered.
func builtinNames() []string {
	return []string{"markdown"}
}

// tasksToJSON converts source.Task values into snake_case-keyed JSON
// objects that match the requirement.md tasks-table column names. Using a
// hand-rolled map (rather than struct tags on source.Task) keeps the
// Discovery package free of CLI-shaped concerns: the queue layer (PR-71)
// will write a different mapping when it materialises tasks into SQLite,
// and we do not want one shape locked in by stdout.
func tasksToJSON(tasks []source.Task) []map[string]any {
	out := make([]map[string]any, len(tasks))
	for i, t := range tasks {
		entry := map[string]any{
			"source":      t.Source,
			"external_id": t.ExternalID,
			"title":       t.Title,
			"done":        t.Done,
		}
		if t.Body != "" {
			entry["body"] = t.Body
		}
		if t.Notes != "" {
			entry["notes"] = t.Notes
		}
		if t.Priority != "" {
			entry["priority"] = t.Priority
		}
		if t.SourcePath != "" {
			entry["source_path"] = t.SourcePath
		}
		if len(t.RawMetadata) > 0 {
			entry["raw_metadata"] = t.RawMetadata
		}
		out[i] = entry
	}
	return out
}
