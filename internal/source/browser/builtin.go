package browser

import (
	_ "embed"
	"fmt"

	"github.com/haruotsu/marunage/internal/source"
)

// embeddedManifest is the bundled plugin.toml shipped with the binary.
// We embed it via go:embed so the runtime does not need to find a
// particular file path — built-ins must work even when ~/.marunage does
// not exist yet (first run, fresh container, etc.).
//
//go:embed plugin.toml
var embeddedManifest []byte

// Manifest returns the parsed view of the bundled plugin.toml. The bytes
// flow through source.LoadManifestFromBytes so the embedded payload is
// validated by the exact same pipeline as on-disk manifests.
func Manifest() (*source.Manifest, error) {
	m, err := source.LoadManifestFromBytes(embeddedManifest)
	if err != nil {
		return nil, fmt.Errorf("browser embedded manifest: %w", err)
	}
	return m, nil
}

// RegisterBuiltin constructs an Adapter wrapping a fresh browser.Plugin
// (configured with opts) and registers it in r. It also runs the
// capability/interface cross-check against the bundled manifest so any
// drift between the embedded TOML and the adapter's actual interfaces
// is caught at startup rather than at first dispatch.
//
// opts forward to New, so callers pass WithDriver / WithConfig here
// exactly as they would for a directly-constructed Plugin. Returns
// ErrPluginAlreadyRegistered (from the source package) if r already has
// a "browser" entry.
func RegisterBuiltin(r *source.Registry, opts ...Option) error {
	if r == nil {
		return fmt.Errorf("browser: nil registry")
	}
	p, err := New(opts...)
	if err != nil {
		return err
	}
	a := NewAdapter(p)
	m, err := Manifest()
	if err != nil {
		return fmt.Errorf("browser: load embedded manifest: %w", err)
	}
	if err := source.ValidateAgainstManifest(a, m); err != nil {
		return fmt.Errorf("browser: built-in fails self-check: %w", err)
	}
	return r.Register(a)
}
