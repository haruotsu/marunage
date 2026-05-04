package notion

import (
	"context"

	"github.com/haruotsu/marunage/internal/source"
)

// pluginName is the canonical Source value emitted on every Task and the
// name under which the Adapter is registered. Centralised so the manifest /
// registry / adapter cannot drift if the value ever changes.
const pluginName = "notion"

// Adapter wraps a *Plugin and exposes it as a source.Plugin (plus all four
// optional sub-interfaces). Mirrors the markdown adapter shape so a reviewer
// who already understands one source recognises the other immediately.
type Adapter struct {
	inner *Plugin
}

// NewAdapter wraps p. p MUST be a fully-configured *Plugin (typically from
// New(WithClient(...), WithDatabaseID(...), ...)); we deliberately do not
// accept Option values here so configuration knobs stay on the inner type
// and the adapter cannot drift out of sync with them.
func NewAdapter(p *Plugin) *Adapter { return &Adapter{inner: p} }

// Name reports the canonical plugin identifier.
func (a *Adapter) Name() string { return pluginName }

// List forwards to the inner Plugin and lifts every notion.Task into a
// source.Task. The lift stamps Source = pluginName and copies provenance
// fields (last_edited_time, database_id) into RawMetadata so downstream
// materialisation can reconstruct the page's origin without re-querying
// Notion.
func (a *Adapter) List(ctx context.Context) ([]source.Task, error) {
	inner, err := a.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	return convertTasks(inner), nil
}

// Setup translates source.SetupOptions into the package-local SetupOpts.
// The two are field-equivalent today; keeping them as separate types lets
// the inner package stay free of the source-package import while the
// adapter performs the trivial value-copy.
func (a *Adapter) Setup(ctx context.Context, opts source.SetupOptions) error {
	return a.inner.Setup(ctx, SetupOpts{NonInteractive: opts.NonInteractive})
}

// AuthStatus forwards to the inner Plugin verbatim. The inner method already
// returns source.AuthStatus values, so no translation is needed.
func (a *Adapter) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return a.inner.AuthStatus(ctx)
}

// Since forwards to the inner Plugin's last_edited_time-driven Since. The
// checkpoint argument from source.Sincer is ignored: notion.Plugin keeps
// its own per-database checkpoint in the injected Checkpointer (KVStateRepo
// at runtime). PR-71's scheduler reads/writes a single global "last_run"
// checkpoint and lets individual plugins manage finer-grained state
// internally — same contract as markdown's adapter.
func (a *Adapter) Since(ctx context.Context, _ string) ([]source.Task, error) {
	inner, err := a.inner.Since(ctx)
	if err != nil {
		return nil, err
	}
	return convertTasks(inner), nil
}

// Add forwards to the inner Plugin and lifts the returned notion.Task into
// source.Task. notes is currently ignored (Notion's API does not have a
// first-class "notes" concept; future PRs could map it to a rich-text
// property), but accepting the argument today keeps the adapter signature
// stable.
func (a *Adapter) Add(ctx context.Context, title, notes string) (source.Task, error) {
	t, err := a.inner.Add(ctx, title, notes)
	if err != nil {
		return source.Task{}, err
	}
	return convertTask(t), nil
}

// Complete forwards by ExternalID. On Notion's data model "complete" means
// "archive the page" — the API has no permanent delete and no global
// "done" flag at the page level. Distinguishing semantically from Delete
// is left to a future PR that wires WithStatusProperty.
func (a *Adapter) Complete(ctx context.Context, externalID string) error {
	return a.inner.Complete(ctx, externalID)
}

// Delete forwards by ExternalID. Same archive-as-delete caveat as Complete;
// the rationale is documented at length on Plugin.Delete.
func (a *Adapter) Delete(ctx context.Context, externalID string) error {
	return a.inner.Delete(ctx, externalID)
}

// convertTasks is a tiny lifting helper so List / Since share one definition.
// A future field addition to source.Task (RawMetadata bag, etc.) needs one
// edit, not two.
func convertTasks(in []Task) []source.Task {
	out := make([]source.Task, len(in))
	for i, t := range in {
		out[i] = convertTask(t)
	}
	return out
}

// convertTask is the notion.Task → source.Task adapter. The mapping is:
//
//	ExternalID  <- ExternalID (Notion page id, UUID)
//	Title       <- Title
//	Done        <- Done (== archived)
//	SourcePath  <- SourcePath (== page.url)
//	Source      = pluginName
//	RawMetadata { "last_edited_time": ..., "database_id": ... }
//
// Notes / Body / Priority stay zero — the inner Task does not carry them
// today. PR-71's triage layer fills these in (or not) per its rules.
func convertTask(t Task) source.Task {
	return source.Task{
		Source:     pluginName,
		ExternalID: t.ExternalID,
		Title:      t.Title,
		Done:       t.Done,
		SourcePath: t.SourcePath,
		RawMetadata: map[string]any{
			"last_edited_time": t.LastEditedTime,
			"database_id":      t.DatabaseID,
		},
	}
}

