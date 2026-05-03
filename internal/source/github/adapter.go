// Package github's adapter.go bridges the inner Plugin to the generic
// source.Plugin contract that PR-70 introduces. Same shape as the markdown
// adapter: a thin pointer-wrapper that opts into source.Sincer and
// source.Completer at compile time so the registry's
// ValidateAgainstManifest cross-check can prove "the manifest declares
// since/complete and the adapter implements them" before the daemon
// dispatches its first call.
package github

import (
	"context"

	"github.com/haruotsu/marunage/internal/source"
)

// Adapter wraps a *Plugin and exposes it as a source.Plugin (plus
// Sincer / Completer). The struct holds a pointer so the adapter and any
// direct caller of the inner Plugin share state — though today Plugin has
// no mutable state, the pointer keeps the door open for a future cache or
// rate-limiter without a churny call-site change.
type Adapter struct {
	inner *Plugin
}

// NewAdapter wraps p. p MUST be a fully-configured *Plugin (typically from
// New(WithRunner(...))); we deliberately do not accept Option values here
// so the configuration knobs stay on the inner type and the adapter
// cannot drift out of sync with them.
func NewAdapter(p *Plugin) *Adapter {
	return &Adapter{inner: p}
}

// Name reports the canonical plugin identifier.
func (a *Adapter) Name() string { return a.inner.Name() }

// List forwards to the inner Plugin. The conversion to source.Task already
// happens inside Plugin.List, so the adapter is a pure passthrough.
func (a *Adapter) List(ctx context.Context) ([]source.Task, error) {
	return a.inner.List(ctx)
}

// Setup forwards opts as-is. The inner Plugin honours NonInteractive;
// passing the struct through keeps a future opt addition compile-checked.
func (a *Adapter) Setup(ctx context.Context, opts source.SetupOptions) error {
	return a.inner.Setup(ctx, opts)
}

// AuthStatus forwards to the inner Plugin.
func (a *Adapter) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return a.inner.AuthStatus(ctx)
}

// Since forwards the checkpoint string verbatim. The inner Plugin
// interprets an empty checkpoint as "first run, return everything" and a
// non-empty one as the RFC3339 high-water mark for the gh `updated:>=`
// qualifier.
func (a *Adapter) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	return a.inner.Since(ctx, checkpoint)
}

// Complete forwards by externalID. The "owner/repo#number" parse and gh
// invocation live on the inner Plugin so a future re-implementation that
// uses GitHub's API directly does not need to touch this file.
func (a *Adapter) Complete(ctx context.Context, externalID string) error {
	return a.inner.Complete(ctx, externalID)
}
