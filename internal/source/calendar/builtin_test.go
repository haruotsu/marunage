package calendar

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestManifestEmbeddedReturnsValidManifest — B1.
func TestManifestEmbeddedReturnsValidManifest(t *testing.T) {
	t.Parallel()

	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != PluginName {
		t.Errorf("Name = %q, want %q", m.Name, PluginName)
	}
	if m.Version == "" {
		t.Errorf("Version is empty")
	}
	if m.SyncMode != source.SyncModeReadOnly {
		t.Errorf("SyncMode = %q, want read-only", m.SyncMode)
	}
	for _, want := range []source.Capability{
		source.CapList, source.CapSetup, source.CapAuthStatus,
	} {
		if !m.HasCapability(want) {
			t.Errorf("manifest missing mandatory capability %q", want)
		}
	}
	for _, forbid := range []source.Capability{
		source.CapAdd, source.CapComplete, source.CapDelete, source.CapSince,
	} {
		if m.HasCapability(forbid) {
			t.Errorf("manifest declares optional capability %q on a read-only source", forbid)
		}
	}
}

// TestRegisterBuiltinAttachesAdapterAndPassesValidation — B2.
func TestRegisterBuiltinAttachesAdapterAndPassesValidation(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r, WithClient(&fakeClient{statusOut: source.AuthAuthenticated})); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, err := r.Get(PluginName)
	if err != nil {
		t.Fatalf("Get %s: %v", PluginName, err)
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

// TestRegisterBuiltinTwiceFails — B3.
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

// TestRegisterBuiltinNilRegistry — B4.
func TestRegisterBuiltinNilRegistry(t *testing.T) {
	t.Parallel()

	if err := RegisterBuiltin(nil); err == nil {
		t.Fatalf("RegisterBuiltin(nil) returned nil, want error")
	}
}
