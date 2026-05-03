package calendar

import (
	_ "embed"
	"fmt"

	"github.com/haruotsu/marunage/internal/source"
)

// embeddedManifest is the bundled plugin.toml shipped with the binary.
// Embedding via go:embed keeps the runtime independent of where ~/.marunage
// lives — built-ins must work even on a fresh container with no disk
// configuration. The same bytes flow through source.LoadManifestFromBytes
// so the on-disk and in-memory paths cannot drift in their accept/reject
// rules.
//
//go:embed plugin.toml
var embeddedManifest []byte

// Manifest returns the parsed view of the bundled plugin.toml. Validation
// runs on every call (rather than at package init) so a malformed manifest
// surfaces to the caller rather than crashing every binary that links the
// package — exactly the policy the markdown built-in already follows.
func Manifest() (*source.Manifest, error) {
	m, err := source.LoadManifestFromBytes(embeddedManifest)
	if err != nil {
		return nil, fmt.Errorf("calendar embedded manifest: %w", err)
	}
	return m, nil
}

// RegisterBuiltin constructs an Adapter wrapping a fresh calendar.Plugin
// (configured with opts) and registers it in r. The capability/interface
// cross-check (ValidateAgainstManifest) runs here so any drift between
// the embedded TOML and the adapter's actual interfaces is caught at
// startup rather than at first dispatch.
//
// opts forward to New, so callers pass WithClient / WithClock here exactly
// as they would for a directly-constructed Plugin. Returns
// ErrPluginAlreadyRegistered (from the source package) if r already has a
// "calendar" entry; the registry's duplicate guard is the single source of
// truth.
func RegisterBuiltin(r *source.Registry, opts ...Option) error {
	if r == nil {
		return fmt.Errorf("calendar: nil registry")
	}
	a := NewAdapter(New(opts...))
	m, err := Manifest()
	if err != nil {
		return fmt.Errorf("calendar: load embedded manifest: %w", err)
	}
	if err := source.ValidateAgainstManifest(a, m); err != nil {
		return fmt.Errorf("calendar: built-in fails self-check: %w", err)
	}
	return r.Register(a)
}
