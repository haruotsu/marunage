// adapter.go bridges *Plugin to the source.Plugin contract used by the
// Discovery dispatcher (PR-70). The wrapper is thin so the inner Plugin's
// API can evolve without forcing callers to chase signature changes here.
package reaction

import (
	"context"

	"github.com/haruotsu/marunage/internal/source"
)

// Adapter exposes a *Plugin as a source.Plugin (plus Sincer and Completer).
// The struct holds a pointer so the adapter and any direct caller share the
// underlying client/checkpointer state.
type Adapter struct {
	inner *Plugin
}

// NewAdapter wraps p. p MUST be a fully-configured *Plugin (typically from
// New(WithClient(...))). Passing nil panics so construction-time errors are
// caught immediately rather than deferred to the first dispatch call.
func NewAdapter(p *Plugin) *Adapter {
	if p == nil {
		panic("slack/reaction: NewAdapter requires a non-nil Plugin")
	}
	return &Adapter{inner: p}
}

// Name reports the canonical plugin identifier ("slack:reaction").
func (a *Adapter) Name() string { return a.inner.Name() }

// List forwards to the inner Plugin.
func (a *Adapter) List(ctx context.Context) ([]source.Task, error) {
	return a.inner.List(ctx)
}

// Setup forwards to the inner Plugin's Setup.
func (a *Adapter) Setup(ctx context.Context, opts source.SetupOptions) error {
	return a.inner.Setup(ctx, opts)
}

// AuthStatus forwards to the inner Plugin.
func (a *Adapter) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return a.inner.AuthStatus(ctx)
}

// Since forwards the explicit checkpoint argument to the inner Plugin.
func (a *Adapter) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	return a.inner.Since(ctx, checkpoint)
}

// Complete forwards by externalID. The inner Plugin owns the DM routing and
// message formatting.
func (a *Adapter) Complete(ctx context.Context, externalID string) error {
	return a.inner.Complete(ctx, externalID)
}
