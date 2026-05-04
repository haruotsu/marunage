package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/source/markdown"
	"github.com/haruotsu/marunage/internal/source/slack"
	"github.com/haruotsu/marunage/internal/source/slack/reaction"
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

// builtinRegistrar is the registration-side knowledge for one built-in
// plugin: how to attach it to a Registry given the user's CLI flags.
// Returning a registrar (rather than calling Register inline) keeps the
// (name, register, --file requirement) triple in one table, which is the
// only way the "unknown source" error message and the actual built-in set
// stay in lockstep when PR-80+ adds Gmail / Slack / etc.
type builtinRegistrar func(r *source.Registry, files []string) error

// builtins is the single source of truth for "which Discovery sources can
// `marunage discover --once --source X` resolve right now". Adding a new
// built-in (PR-80 Gmail, PR-82 Slack, ...) means appending one entry here
// and nothing else; the unknown-source error and the registration switch
// derive their behaviour from this map.
var builtins = map[string]builtinRegistrar{
	"markdown": func(r *source.Registry, files []string) error {
		if len(files) == 0 {
			return fmt.Errorf("source markdown: --file is required (at least one path)")
		}
		if err := markdown.RegisterBuiltin(r, markdown.WithFiles(files...)); err != nil {
			return fmt.Errorf("register markdown: %w", err)
		}
		return nil
	},
	// PR-82: register the Slack source with no Client wired. Discover-once
	// then surfaces ErrClientNotConfigured at List time, which is the
	// documented "user has not run `marunage setup slack` yet" signal.
	// The runtime daemon (PR-71+) supplies the real Client via dependency
	// injection at startup; until then `--source slack` is reachable but
	// inert, so the unknown-source error message stays correct as soon as
	// PR-82 lands.
	"slack": func(r *source.Registry, _ []string) error {
		if err := slack.RegisterBuiltin(r); err != nil {
			return fmt.Errorf("register slack: %w", err)
		}
		return nil
	},
	// PR-100: register the Slack Reaction Trigger source with no Client wired.
	// Discover-once surfaces ErrClientNotConfigured at List time, signalling
	// that the user has not configured the reaction client yet. The daemon
	// loop supplies options (WithReactions / WithDMOnComplete) from config
	// when discovery.slack.reaction_trigger.enabled is true.
	"slack:reaction": func(r *source.Registry, _ []string) error {
		if err := reaction.RegisterBuiltin(r); err != nil {
			return fmt.Errorf("register slack:reaction: %w", err)
		}
		return nil
	},
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
	if reg, ok := builtins[sourceName]; ok {
		if err := reg(r, files); err != nil {
			return err
		}
	}
	// An unknown source falls through with no registration; r.Get below
	// returns ErrPluginNotFound, which we translate into a user-friendly
	// message listing the built-ins this binary knows about.

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

// builtinNames returns the sorted list of built-in plugin names derived
// from the builtins table. Sorting keeps the user-facing error message
// stable across runs (Go's map iteration is randomised); deriving from
// the same map the registration switch uses prevents the two from
// drifting when PR-80+ lands.
func builtinNames() []string {
	names := make([]string, 0, len(builtins))
	for n := range builtins {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
