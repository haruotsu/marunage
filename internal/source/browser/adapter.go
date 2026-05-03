// adapter.go bridges *Plugin to the generic source.Plugin contract from
// internal/source. The adapter is intentionally thin — every method
// forwards to the underlying Plugin — so a future change to the inner
// plugin (e.g. adding a Sincer implementation that diff-hashes the
// rendered page) does not need to touch this file beyond adding the
// matching forwarder method.
package browser

import (
	"context"

	"github.com/haruotsu/marunage/internal/source"
)

// Adapter wraps a *Plugin and exposes it as a source.Plugin. The struct
// holds a pointer so the adapter and any direct caller of the inner
// Plugin share the same driver and config references.
type Adapter struct {
	inner *Plugin
}

// NewAdapter wraps p. p MUST be a fully-configured *Plugin (typically
// from New(WithDriver(...), WithConfig(...))); we deliberately do not
// accept Option values here so the configuration knobs stay on the
// inner type and the adapter cannot drift out of sync with them.
func NewAdapter(p *Plugin) *Adapter {
	return &Adapter{inner: p}
}

// Name reports the canonical plugin identifier (the prefix every per-
// site Source value carries).
func (a *Adapter) Name() string { return pluginName }

// List forwards to the inner Plugin verbatim. The inner already returns
// source.Task values (the per-task Source field carries the per-site
// suffix), so no conversion happens here.
func (a *Adapter) List(ctx context.Context) ([]source.Task, error) {
	return a.inner.List(ctx)
}

// Setup forwards to the inner Plugin. The browser source is inherently
// non-interactive (the on-disk browser.toml is the source of truth),
// so every SetupOptions value the contract currently defines is
// naturally satisfied.
func (a *Adapter) Setup(ctx context.Context, _ source.SetupOptions) error {
	return a.inner.Setup(ctx)
}

// AuthStatus forwards to the inner Plugin (always authenticated).
func (a *Adapter) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return a.inner.AuthStatus(ctx)
}
