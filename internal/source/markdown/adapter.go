// Package markdown's adapter.go bridges the existing markdown.Plugin Go API
// to the generic source.Plugin contract that PR-70 introduces. The adapter
// is intentionally thin — every method forwards directly to the underlying
// Plugin and only translates the Task type — so a future change to the
// inner plugin (PR-50 follow-ups) does not need to touch this file.
//
// Why a separate type rather than making markdown.Plugin implement
// source.Plugin directly: the inner Plugin's signatures (e.g.
// Since(ctx) — no checkpoint argument) intentionally diverge from the
// generic shape the source package wants (Since(ctx, checkpoint)).
// Hiding that adaptation in a wrapper keeps the inner API focused on
// markdown's needs and the outer one focused on the cross-source
// dispatcher's needs.
package markdown

import (
	"context"

	"github.com/haruotsu/marunage/internal/source"
)

// pluginName is the canonical Source value emitted on every Task and the
// name under which the Adapter is registered. Keeping it as a const (rather
// than re-deriving from the package path) avoids a registry / manifest /
// adapter triple-edit when it eventually changes.
const pluginName = "markdown"

// Adapter wraps a *Plugin and exposes it as a source.Plugin (plus all four
// optional sub-interfaces). The struct holds a pointer so the adapter and
// any direct caller of the inner Plugin share the same mutex and file list.
type Adapter struct {
	inner *Plugin
}

// NewAdapter wraps p. p MUST be a fully-configured *Plugin (typically from
// New(WithFiles(...))); we deliberately do not accept Option values here so
// the configuration knobs stay on the inner type and the adapter cannot
// drift out of sync with them.
func NewAdapter(p *Plugin) *Adapter {
	return &Adapter{inner: p}
}

// Name reports the canonical plugin identifier.
func (a *Adapter) Name() string { return pluginName }

// List forwards to the inner Plugin and converts the result row by row.
// The conversion stamps Source = "markdown" and copies SourcePath / Title /
// Done / ExternalID; LineNumber rides along in RawMetadata so a CLI
// `marunage show` can still link back to the file even though the queue
// schema does not have a dedicated column for it.
func (a *Adapter) List(ctx context.Context) ([]source.Task, error) {
	inner, err := a.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	return convertTasks(inner), nil
}

// Setup forwards to the inner Plugin. The markdown source is inherently
// non-interactive (it just mkdir+touch on the configured paths), so every
// SetupOptions value the contract currently defines is naturally
// satisfied — there is nothing for the adapter to translate. The
// argument is named (not blanked with `_`) so a future PR-71 field that
// the adapter cannot honour produces a compile-error or test failure
// here, not a silent drop.
func (a *Adapter) Setup(ctx context.Context, opts source.SetupOptions) error {
	_ = opts // markdown setup is always non-interactive; no field to forward today.
	return a.inner.Setup(ctx)
}

// AuthStatus is constant for the markdown source: there is no remote
// credential, so the only failure modes ("file unreadable", "disk full")
// are surfaced by List/Add/etc. as ordinary errors. Returning anything
// other than authenticated would force callers to re-run setup for what
// is fundamentally a filesystem permission problem.
func (a *Adapter) AuthStatus(context.Context) (source.AuthStatus, error) {
	return source.AuthAuthenticated, nil
}

// Since forwards to the inner Plugin's mtime-driven Since. The checkpoint
// argument from source.Sincer is ignored: markdown.Plugin keeps its own
// per-file checkpoint state in the injected Checkpointer (KVStateRepo at
// runtime), which the brief explicitly calls out as the design. PR-71's
// scheduler reads/writes a single global "last_run" checkpoint and lets
// individual plugins manage finer-grained state internally.
func (a *Adapter) Since(ctx context.Context, _ string) ([]source.Task, error) {
	inner, err := a.inner.Since(ctx)
	if err != nil {
		return nil, err
	}
	return convertTasks(inner), nil
}

// Add forwards to the inner Plugin and lifts the returned markdown.Task
// into source.Task. notes is passed through unchanged so a future inner
// implementation that captures it does not need an adapter edit.
func (a *Adapter) Add(ctx context.Context, title, notes string) (source.Task, error) {
	t, err := a.inner.Add(ctx, title, notes)
	if err != nil {
		return source.Task{}, err
	}
	return convertTask(t), nil
}

// Complete forwards by ExternalID.
func (a *Adapter) Complete(ctx context.Context, externalID string) error {
	return a.inner.Complete(ctx, externalID)
}

// Delete forwards by ExternalID.
func (a *Adapter) Delete(ctx context.Context, externalID string) error {
	return a.inner.Delete(ctx, externalID)
}

// convertTasks is a tiny lifting helper for the List / Since paths. Pulled
// out so the conversion has one definition; otherwise a future field
// addition would need three identical edits.
func convertTasks(in []Task) []source.Task {
	out := make([]source.Task, len(in))
	for i, t := range in {
		out[i] = convertTask(t)
	}
	return out
}

// convertTask is the markdown.Task -> source.Task adapter. The mapping is:
//
//	ExternalID  <- ExternalID
//	Title       <- Title
//	Done        <- Done
//	SourcePath  <- SourcePath
//	Source      = pluginName ("markdown")
//	RawMetadata { "line_number": LineNumber }
//
// Notes / Body / Priority stay at zero — markdown.Task does not carry them
// today. PR-71 layers triage on top and is responsible for filling those
// in (or not) based on plugin output.
func convertTask(t Task) source.Task {
	return source.Task{
		Source:     pluginName,
		ExternalID: t.ExternalID,
		Title:      t.Title,
		Done:       t.Done,
		SourcePath: t.SourcePath,
		RawMetadata: map[string]any{
			"line_number": t.LineNumber,
		},
	}
}
