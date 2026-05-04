package gmail

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestManifestEmbeddedReturnsExpectedShape locks down the contract
// between the bundled plugin.toml and the adapter's actual capability
// set: if a future PR drops, say, Completer, the manifest must drop
// "complete" too — otherwise ValidateAgainstManifest catches the drift
// here.
func TestManifestEmbeddedReturnsExpectedShape(t *testing.T) {
	t.Parallel()

	m, err := Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != "gmail" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.SyncMode != source.SyncModeBidirectional {
		t.Errorf("SyncMode = %q; Complete writes upstream so we must declare bidirectional", m.SyncMode)
	}
	for _, want := range []source.Capability{
		source.CapList, source.CapSetup, source.CapAuthStatus,
		source.CapSince, source.CapComplete,
	} {
		if !m.HasCapability(want) {
			t.Errorf("manifest missing capability %q", want)
		}
	}
	for _, mustNotHave := range []source.Capability{source.CapAdd, source.CapDelete} {
		if m.HasCapability(mustNotHave) {
			t.Errorf("manifest unexpectedly declares %q (gmail is read-mostly)", mustNotHave)
		}
	}
}

// TestRegisterBuiltinAttachesAdapterAndPassesValidation is the end-to-
// end startup hook the brief asks for: the built-in registration
// helper must (a) register the gmail adapter under name "gmail" and
// (b) survive the capability/interface cross-check using the bundled
// manifest. If either side drifts, this test goes red.
func TestRegisterBuiltinAttachesAdapterAndPassesValidation(t *testing.T) {
	t.Parallel()

	r := source.NewRegistry()
	if err := RegisterBuiltin(r); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, err := r.Get("gmail")
	if err != nil {
		t.Fatalf("Get gmail: %v", err)
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

func TestRegisterBuiltinNilRegistryRejected(t *testing.T) {
	t.Parallel()

	if err := RegisterBuiltin(nil); err == nil {
		t.Fatalf("RegisterBuiltin(nil) returned nil error")
	}
}

// TestRegisterBuiltinForwardsOptions confirms that callers can pass a
// pre-built fake Client through the options slice and have it reach
// the adapter's inner Plugin — i.e. RegisterBuiltin does not silently
// drop options. This is the seam PR-71 will use to wire the real
// client at startup.
func TestRegisterBuiltinForwardsOptions(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{}
	r := source.NewRegistry()
	if err := RegisterBuiltin(r, WithClient(fc), WithQuery("from:test")); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	got, _ := r.Get("gmail")
	a := got.(*Adapter)
	if a.inner.Query() != "from:test" {
		t.Errorf("inner Query() = %q", a.inner.Query())
	}
	if a.inner.client != fc {
		t.Errorf("inner client not the one passed via WithClient")
	}
}
