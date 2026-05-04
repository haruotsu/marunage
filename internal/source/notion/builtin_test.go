package notion

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestManifestParsesAndValidates locks in the on-disk plugin.toml shape. The
// manifest flows through source.LoadManifestFromBytes — the same pipeline
// the markdown built-in uses — so a malformed TOML or a manifest that
// declares an unknown capability fails loudly here rather than at startup.
func TestManifestParsesAndValidates(t *testing.T) {
	t.Parallel()

	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != pluginName {
		t.Errorf("Name = %q", m.Name)
	}
	if m.SyncMode != source.SyncModeBidirectional {
		t.Errorf("SyncMode = %q", m.SyncMode)
	}
	for _, want := range []source.Capability{
		source.CapList, source.CapSetup, source.CapAuthStatus, source.CapSince,
		source.CapAdd, source.CapComplete, source.CapDelete,
	} {
		if !m.HasCapability(want) {
			t.Errorf("manifest missing %q", want)
		}
	}
}

// TestRegisterBuiltinSucceedsAgainstFreshRegistry — the happy path: the
// adapter passes ValidateAgainstManifest (capabilities ↔ interfaces match)
// and lands in the registry under "notion".
func TestRegisterBuiltinSucceedsAgainstFreshRegistry(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, err := r.Get(pluginName)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != pluginName {
		t.Errorf("Name = %q", got.Name())
	}
}

// TestRegisterBuiltinDuplicateRejected — a second registration must surface
// the typed source.ErrPluginAlreadyRegistered rather than silently
// overwriting the first one (which would mask copy-paste bugs in plugin
// loading code).
func TestRegisterBuiltinDuplicateRejected(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r); err != nil {
		t.Fatalf("RegisterBuiltin#1: %v", err)
	}
	err := RegisterBuiltin(r)
	if !errors.Is(err, source.ErrPluginAlreadyRegistered) {
		t.Fatalf("RegisterBuiltin#2: err = %v, want ErrPluginAlreadyRegistered", err)
	}
}

// TestRegisterBuiltinNilRegistryRejected guards the doc contract: a nil
// registry is a programmer error, not a "register silently nowhere" hint.
func TestRegisterBuiltinNilRegistryRejected(t *testing.T) {
	t.Parallel()
	if err := RegisterBuiltin(nil); err == nil {
		t.Fatalf("RegisterBuiltin(nil) returned nil error")
	}
}
