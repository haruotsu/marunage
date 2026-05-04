package slack

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// H1 / H2.
func TestManifestEmbedded(t *testing.T) {
	t.Parallel()
	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != "slack" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Version == "" {
		t.Errorf("Version is empty")
	}
	if m.SyncMode != source.SyncModeBidirectional {
		t.Errorf("SyncMode = %q, want bidirectional", m.SyncMode)
	}
	for _, want := range []source.Capability{
		source.CapList, source.CapSetup, source.CapAuthStatus,
		source.CapSince, source.CapComplete,
	} {
		if !m.HasCapability(want) {
			t.Errorf("manifest missing capability %q", want)
		}
	}
	for _, unwanted := range []source.Capability{source.CapAdd, source.CapDelete} {
		if m.HasCapability(unwanted) {
			t.Errorf("manifest unexpectedly declares capability %q", unwanted)
		}
	}
}

// H3.
func TestRegisterBuiltinAttachesAdapterAndPassesValidation(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	if err := RegisterBuiltin(r, WithIncludeMentions(true)); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, err := r.Get("slack")
	if err != nil {
		t.Fatalf("Get slack: %v", err)
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

// H4.
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

// Defensive: nil registry surfaces a friendly error rather than a panic.
func TestRegisterBuiltinNilRegistry(t *testing.T) {
	t.Parallel()
	if err := RegisterBuiltin(nil); err == nil {
		t.Fatalf("expected error on nil registry")
	}
}
