package github

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestManifestEmbeddedReturnsValidManifest is the contract that the bundled
// plugin.toml stays in sync with the actual interface implementations: if a
// future PR drops, say, Completer, the manifest must drop "complete" too —
// otherwise ValidateAgainstManifest catches the drift here.
func TestManifestEmbeddedReturnsValidManifest(t *testing.T) {
	t.Parallel()

	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != "github" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.SyncMode != source.SyncModeBidirectional {
		t.Errorf("SyncMode = %q", m.SyncMode)
	}
	for _, want := range []source.Capability{
		source.CapList, source.CapSetup, source.CapAuthStatus,
		source.CapSince, source.CapComplete,
	} {
		if !m.HasCapability(want) {
			t.Errorf("manifest missing capability %q", want)
		}
	}
	// PR-83 is read-mostly: Add and Delete are intentionally NOT advertised.
	if m.HasCapability(source.CapAdd) {
		t.Errorf("manifest must not declare `add`; PR-83 does not implement it")
	}
	if m.HasCapability(source.CapDelete) {
		t.Errorf("manifest must not declare `delete`; PR-83 does not implement it")
	}
}

func TestRegisterBuiltinAttachesAdapterAndPassesValidation(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, err := r.Get("github")
	if err != nil {
		t.Fatalf("Get github: %v", err)
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
