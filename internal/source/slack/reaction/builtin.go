package reaction

import (
	_ "embed"
	"fmt"

	"github.com/haruotsu/marunage/internal/source"
)

// embeddedManifest is the bundled plugin.toml shipped alongside the binary.
// Embedded via go:embed so the runtime does not need to find a particular
// file path — built-ins must work even when ~/.marunage does not exist yet.
//
//go:embed plugin.toml
var embeddedManifest []byte

// Manifest returns the parsed view of the bundled plugin.toml. Validation
// runs on every call so a malformed manifest surfaces to the caller — typically
// RegisterBuiltin or a unit test — rather than silently crashing at init time.
func Manifest() (*source.Manifest, error) {
	m, err := source.LoadManifestFromBytes(embeddedManifest)
	if err != nil {
		return nil, fmt.Errorf("slack/reaction embedded manifest: %w", err)
	}
	return m, nil
}

// RegisterBuiltin constructs an Adapter wrapping a fresh reaction.Plugin
// (configured with opts) and registers it in r. It also runs the
// capability/interface cross-check against the bundled manifest so any drift
// between the embedded TOML and the adapter's actual interfaces is caught at
// startup rather than at first dispatch.
//
// opts forward to New, so callers pass WithClient / WithCheckpointer /
// WithReactions / WithDMOnComplete here exactly as they would for a directly-
// constructed Plugin. Returns source.ErrPluginAlreadyRegistered if r already
// has a "slack:reaction" entry.
func RegisterBuiltin(r *source.Registry, opts ...Option) error {
	if r == nil {
		return fmt.Errorf("slack/reaction: nil registry")
	}
	a := NewAdapter(New(opts...))
	m, err := Manifest()
	if err != nil {
		return fmt.Errorf("slack/reaction: load embedded manifest: %w", err)
	}
	if err := source.ValidateAgainstManifest(a, m); err != nil {
		return fmt.Errorf("slack/reaction: built-in fails self-check: %w", err)
	}
	return r.Register(a)
}
