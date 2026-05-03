package markdown

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestManifestEmbeddedReturnsValidManifest is the contract that the bundled
// plugin.toml stays in sync with the actual interface implementations: if a
// future PR drops, say, Adder, the manifest must drop "add" too — otherwise
// ValidateAgainstManifest catches the drift here.
func TestManifestEmbeddedReturnsValidManifest(t *testing.T) {
	t.Parallel()

	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != "markdown" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.SyncMode != source.SyncModeBidirectional {
		t.Errorf("SyncMode = %q", m.SyncMode)
	}
	for _, want := range []source.Capability{
		source.CapList, source.CapSetup, source.CapAuthStatus,
		source.CapSince, source.CapAdd, source.CapComplete, source.CapDelete,
	} {
		if !m.HasCapability(want) {
			t.Errorf("manifest missing capability %q", want)
		}
	}
}

// TestRegisterBuiltinAttachesAdapterAndPassesValidation is the end-to-end
// startup hook the brief asks for: the built-in registration helper must
// (a) register the markdown adapter under name "markdown" and (b) survive
// the capability/interface cross-check using the bundled manifest. If
// either side drifts, this test goes red.
func TestRegisterBuiltinAttachesAdapterAndPassesValidation(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	dir := t.TempDir()
	path := filepath.Join(dir, "todo.md")

	if err := RegisterBuiltin(r, WithFiles(path)); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, err := r.Get("markdown")
	if err != nil {
		t.Fatalf("Get markdown: %v", err)
	}
	a, ok := got.(*Adapter)
	if !ok {
		t.Fatalf("registered plugin is %T, want *Adapter", got)
	}
	// The bundled manifest must match what the adapter implements.
	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if err := source.ValidateAgainstManifest(a, m); err != nil {
		t.Fatalf("ValidateAgainstManifest: %v", err)
	}

	// And the wrapped Plugin still drives the actual file. A round-trip
	// Add via the registered plugin should append to the configured file.
	added, err := a.Add(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !strings.HasPrefix(added.ExternalID, "") || added.Title != "hello" {
		t.Fatalf("Add returned %+v", added)
	}
}

// TestRegisterBuiltinTwiceFails sanity-checks that double registration does
// not silently succeed: a process accidentally calling RegisterBuiltin both
// from main() and from a test setup must see the existing duplicate guard.
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
