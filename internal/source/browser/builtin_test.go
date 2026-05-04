package browser

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestManifestEmbeddedReturnsValidManifest is the contract that the
// bundled plugin.toml stays in sync with the actual interface
// implementations: if a future PR drops AuthStatus, the manifest must
// drop "auth-status" too — otherwise ValidateAgainstManifest catches
// the drift here.
func TestManifestEmbeddedReturnsValidManifest(t *testing.T) {
	t.Parallel()

	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != "browser" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.SyncMode != source.SyncModeReadOnly {
		t.Errorf("SyncMode = %q, want read-only", m.SyncMode)
	}
	for _, want := range []source.Capability{
		source.CapList, source.CapSetup, source.CapAuthStatus,
	} {
		if !m.HasCapability(want) {
			t.Errorf("manifest missing capability %q", want)
		}
	}
	for _, never := range []source.Capability{
		source.CapAdd, source.CapComplete, source.CapDelete, source.CapSince,
	} {
		if m.HasCapability(never) {
			t.Errorf("PR-200 manifest must NOT declare %q", never)
		}
	}
}

// TestRegisterBuiltinAttachesAdapterAndPassesValidation is the end-to-
// end startup hook: RegisterBuiltin registers the adapter under
// "browser" and survives the capability/interface cross-check using the
// bundled manifest.
func TestRegisterBuiltinAttachesAdapterAndPassesValidation(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r, WithDriver(helperFakeDriver()), WithConfig(helperConfig())); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, err := r.Get("browser")
	if err != nil {
		t.Fatalf("Get browser: %v", err)
	}
	a, ok := got.(*Adapter)
	if !ok {
		t.Fatalf("registered plugin is %T, want *Adapter", got)
	}
	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if err := source.ValidateAgainstManifest(a, m); err != nil {
		t.Fatalf("ValidateAgainstManifest: %v", err)
	}
}

// TestRegisterBuiltinTwiceFails sanity-checks that double registration
// does not silently succeed: a process accidentally calling
// RegisterBuiltin both from main() and from a test setup must see the
// existing duplicate guard.
func TestRegisterBuiltinTwiceFails(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r, WithDriver(helperFakeDriver()), WithConfig(helperConfig())); err != nil {
		t.Fatalf("RegisterBuiltin#1: %v", err)
	}
	if err := RegisterBuiltin(r, WithDriver(helperFakeDriver()), WithConfig(helperConfig())); !errors.Is(err, source.ErrPluginAlreadyRegistered) {
		t.Fatalf("RegisterBuiltin#2: want ErrPluginAlreadyRegistered, got %v", err)
	}
}

// TestRegisterBuiltinPropagatesNewError ensures a misconfigured Plugin
// (missing driver / config) fails at registration rather than at first
// scrape.
func TestRegisterBuiltinPropagatesNewError(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	err := RegisterBuiltin(r) // no driver, no config
	if !errors.Is(err, ErrInvalidPlugin) {
		t.Fatalf("err = %v, want ErrInvalidPlugin", err)
	}
}

// TestRegisterBuiltinNilRegistry guards an obvious foot-gun.
func TestRegisterBuiltinNilRegistry(t *testing.T) {
	t.Parallel()

	if err := RegisterBuiltin(nil, WithDriver(helperFakeDriver()), WithConfig(helperConfig())); err == nil {
		t.Fatalf("expected error for nil registry")
	}
}
