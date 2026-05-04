package googletasks

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestManifestEmbeddedReturnsValidManifest pins the embedded plugin.toml
// to the contract it claims: name + sync_mode + every capability the
// adapter actually backs. If a future PR drops Adder / Completer /
// Deleter without dropping the matching capability, this test goes red
// before RegisterBuiltin's self-check would surface the same drift at
// runtime.
func TestManifestEmbeddedReturnsValidManifest(t *testing.T) {
	t.Parallel()

	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != pluginName {
		t.Errorf("Name = %q, want %q", m.Name, pluginName)
	}
	if m.SyncMode != source.SyncModeBidirectional {
		t.Errorf("SyncMode = %q, want %q", m.SyncMode, source.SyncModeBidirectional)
	}
	for _, want := range []source.Capability{
		source.CapList, source.CapSetup, source.CapAuthStatus,
		source.CapAdd, source.CapComplete, source.CapDelete,
	} {
		if !m.HasCapability(want) {
			t.Errorf("manifest missing capability %q", want)
		}
	}
	// `since` is intentionally NOT advertised: the upstream API has no
	// efficient delta endpoint, and a fake "Since == List" implementation
	// would mislead the dispatcher into thinking it can cheap-poll. If
	// that changes, both the manifest and Plugin must move together.
	if m.HasCapability(source.CapSince) {
		t.Errorf("manifest must NOT declare since (no upstream delta endpoint yet)")
	}
}

// TestRegisterBuiltinAttachesPluginAndPassesValidation is the startup
// hook documented in the markdown built-in: registering the source must
// (a) put a *Plugin under name "googletasks" and (b) survive
// ValidateAgainstManifest using the bundled manifest. Either side
// drifting must be loud, not silent.
func TestRegisterBuiltinAttachesPluginAndPassesValidation(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, err := r.Get(pluginName)
	if err != nil {
		t.Fatalf("Get %q: %v", pluginName, err)
	}
	if _, ok := got.(*Plugin); !ok {
		t.Fatalf("registered plugin is %T, want *Plugin", got)
	}
	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if err := source.ValidateAgainstManifest(got, m); err != nil {
		t.Fatalf("ValidateAgainstManifest: %v", err)
	}
}

// TestRegisterBuiltinTwiceFails sanity-checks the duplicate-registration
// guard. A future bootstrap that accidentally double-registers must see
// the typed error rather than silently overwriting the first plugin.
func TestRegisterBuiltinTwiceFails(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r); err != nil {
		t.Fatalf("RegisterBuiltin#1: %v", err)
	}
	if err := RegisterBuiltin(r); !errors.Is(err, source.ErrPluginAlreadyRegistered) {
		t.Fatalf("RegisterBuiltin#2: want ErrPluginAlreadyRegistered, got %v", err)
	}
}

// TestRegisterBuiltinNilRegistry refuses a nil registry up front. The
// markdown built-in does the same; copying the contract avoids a future
// PR registering with one source's helper and crashing when it picks the
// other.
func TestRegisterBuiltinNilRegistry(t *testing.T) {
	t.Parallel()

	if err := RegisterBuiltin(nil); err == nil {
		t.Fatalf("RegisterBuiltin(nil): want error, got nil")
	}
}
