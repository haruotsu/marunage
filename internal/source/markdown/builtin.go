package markdown

import (
	_ "embed"
	"fmt"

	"github.com/haruotsu/marunage/internal/source"
)

// embeddedManifest is the bundled plugin.toml shipped with the binary. We
// embed it via go:embed so the runtime does not need to find a particular
// file path — built-ins must work even when ~/.marunage does not exist
// yet (first run, fresh container, etc.). The same file still serves as
// documentation alongside the source, and a future Phase 4 dynamic
// loader can read the same bytes from disk without changing its parsing
// path.
//
//go:embed plugin.toml
var embeddedManifest []byte

// Manifest returns the parsed view of the bundled plugin.toml. The bytes
// flow through source.LoadManifestFromBytes so the embedded payload is
// validated by the exact same pipeline as on-disk manifests, without the
// I/O failure modes a tempfile detour would add. Validation runs on every
// call rather than at package init so a malformed manifest surfaces to
// the caller (typically tests or RegisterBuiltin) rather than crashing
// every binary that links the package.
func Manifest() (*source.Manifest, error) {
	m, err := source.LoadManifestFromBytes(embeddedManifest)
	if err != nil {
		return nil, fmt.Errorf("markdown embedded manifest: %w", err)
	}
	return m, nil
}

// RegisterBuiltin constructs an Adapter wrapping a fresh markdown.Plugin
// (configured with opts) and registers it in r. It also runs the
// capability/interface cross-check against the bundled manifest so any
// drift between the embedded TOML and the adapter's actual interfaces is
// caught at startup rather than at first dispatch.
//
// opts forward to New, so callers pass WithFiles / WithCheckpointer here
// exactly as they would for a directly-constructed Plugin. Returns
// ErrPluginAlreadyRegistered (from the source package) if r already has a
// "markdown" entry; the registry's duplicate guard is the single source
// of truth.
func RegisterBuiltin(r *source.Registry, opts ...Option) error {
	if r == nil {
		return fmt.Errorf("markdown: nil registry")
	}
	a := NewAdapter(New(opts...))
	m, err := Manifest()
	if err != nil {
		return fmt.Errorf("markdown: load embedded manifest: %w", err)
	}
	if err := source.ValidateAgainstManifest(a, m); err != nil {
		return fmt.Errorf("markdown: built-in fails self-check: %w", err)
	}
	return r.Register(a)
}
