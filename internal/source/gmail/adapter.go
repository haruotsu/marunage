// Package gmail's adapter.go bridges the gmail.Plugin Go API to the
// generic source.Plugin contract introduced in PR-70. The adapter is
// intentionally thin — every method forwards directly to the underlying
// Plugin — so a future change to the inner plugin does not need to
// touch this file.
//
// Why a separate type rather than making *Plugin implement source.Plugin
// directly: the inner Plugin's signatures already match closely, but
// keeping the adapter explicit gives us one place to diverge when the
// generic and concrete shapes inevitably drift (PR-71 may extend
// SetupOptions, future capabilities may grow). Mirrors the pattern
// markdown.Adapter established for PR-50.
package gmail

import (
	"context"

	"github.com/haruotsu/marunage/internal/source"
)

// Adapter wraps a *Plugin and exposes it as a source.Plugin (plus the
// optional Sincer / Completer sub-interfaces). The struct holds a
// pointer so the adapter and any direct caller of the inner Plugin
// share the same mutex and configuration.
type Adapter struct {
	inner *Plugin
}

// NewAdapter wraps p. p MUST be a fully-configured *Plugin (typically
// from New(WithClient(...), WithCheckpointer(...))); the adapter does
// not accept Option values so configuration knobs stay on the inner
// type and the two cannot drift out of sync.
func NewAdapter(p *Plugin) *Adapter {
	return &Adapter{inner: p}
}

// Name reports the canonical plugin identifier.
func (a *Adapter) Name() string { return pluginName }

// List forwards directly to the inner Plugin. The inner Plugin already
// returns []source.Task, so the adapter has nothing to translate.
func (a *Adapter) List(ctx context.Context) ([]source.Task, error) {
	return a.inner.List(ctx)
}

// Setup forwards SetupOptions through to the inner Plugin's Setup,
// which in turn hands them to the configured Client.
func (a *Adapter) Setup(ctx context.Context, opts source.SetupOptions) error {
	return a.inner.Setup(ctx, opts)
}

// AuthStatus forwards to the inner Plugin's credential probe.
func (a *Adapter) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return a.inner.AuthStatus(ctx)
}

// Since forwards. The checkpoint argument from source.Sincer is unused —
// gmail.Plugin keeps its checkpoint state in the injected Checkpointer
// (KVStateRepo at runtime) under p.checkpointKey. PR-71's scheduler
// only ever supplies a single global "last_run" checkpoint and lets
// individual plugins manage finer-grained state internally, exactly
// like the markdown adapter.
func (a *Adapter) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	return a.inner.Since(ctx, checkpoint)
}

// Complete forwards by ExternalID. Gmail message ids carry no
// adapter-specific translation.
func (a *Adapter) Complete(ctx context.Context, externalID string) error {
	return a.inner.Complete(ctx, externalID)
}
