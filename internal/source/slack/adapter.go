// adapter.go bridges *Plugin to the source.Plugin contract used by the
// Discovery dispatcher. The wrapper is deliberately thin so the inner
// Plugin's API can evolve without forcing every caller to chase signature
// changes through this file.
//
// Why a separate type rather than making *Plugin satisfy source.Plugin
// directly: the inner List/Since methods already speak source.Task
// (because the slack package owns no second Task type), but Setup uses
// the package-local SetupOptions translation and Adder/Deleter must be
// excluded from the surface — Slack is read-only-with-notification, not
// bidirectional. Hiding the interface curation in an Adapter type keeps
// those decisions documented in code.

package slack

import (
	"context"

	"github.com/haruotsu/marunage/internal/source"
)

// Adapter exposes a *Plugin as a source.Plugin (plus Sincer and
// Completer). The struct holds a pointer so the adapter and any direct
// caller share the underlying client/checkpointer state.
type Adapter struct {
	inner *Plugin
}

// NewAdapter wraps p. p MUST be a fully-configured *Plugin (typically
// from New(WithClient(...))). Passing nil panics: a half-built adapter
// would defer the failure to first dispatch, which is harder to debug
// than the construction-time crash.
func NewAdapter(p *Plugin) *Adapter {
	if p == nil {
		panic("slack: NewAdapter requires a non-nil Plugin")
	}
	return &Adapter{inner: p}
}

// Name reports the canonical plugin identifier.
func (a *Adapter) Name() string { return a.inner.Name() }

// List forwards to the inner Plugin. The lift to source.Task already
// happens inside the inner Plugin, so no per-row conversion is needed
// here.
func (a *Adapter) List(ctx context.Context) ([]source.Task, error) {
	return a.inner.List(ctx)
}

// Setup forwards to the inner Plugin's Setup, which already accepts the
// shared SetupOptions struct.
func (a *Adapter) Setup(ctx context.Context, opts source.SetupOptions) error {
	return a.inner.Setup(ctx, opts)
}

// AuthStatus forwards to the inner Plugin.
func (a *Adapter) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return a.inner.AuthStatus(ctx)
}

// Since forwards the explicit checkpoint argument to the inner Plugin.
// The inner plugin reconciles it against the configured Checkpointer.
func (a *Adapter) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	return a.inner.Since(ctx, checkpoint)
}

// Complete forwards by externalID. The inner Plugin owns the message
// formatting and channel-id validation.
func (a *Adapter) Complete(ctx context.Context, externalID string) error {
	return a.inner.Complete(ctx, externalID)
}
