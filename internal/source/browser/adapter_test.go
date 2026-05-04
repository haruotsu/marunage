package browser

import (
	"context"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestAdapterImplementsPluginInterface is the compile-time witness that
// the adapter satisfies the mandatory contract. The var declaration is
// the assertion; if a method goes missing this file stops compiling.
func TestAdapterImplementsPluginInterface(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := NewAdapter(p)
	var _ source.Plugin = a
	if a.Name() != "browser" {
		t.Fatalf("Name() = %q, want browser", a.Name())
	}
}

// TestAdapterListForwards asserts the adapter does not lose data on the
// way to the source-package shape: every Task the inner Plugin produces
// must appear in adapter output verbatim.
func TestAdapterListForwards(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := NewAdapter(p)
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Source != "browser:slack-saved" {
		t.Errorf("Source = %q", got[0].Source)
	}
}

// TestAdapterAuthStatusIsAuthenticated nails the contract: browser has
// no remote credential the plugin can introspect, so the adapter always
// reports authenticated.
func TestAdapterAuthStatusIsAuthenticated(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := NewAdapter(p)
	got, err := a.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("AuthStatus = %q, want %q", got, source.AuthAuthenticated)
	}
}

// TestAdapterSetupForwards asserts Setup wires through. With a no-op
// inner Setup the assertion is just "no error returned".
func TestAdapterSetupForwards(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := NewAdapter(p)
	if err := a.Setup(context.Background(), source.SetupOptions{}); err != nil {
		t.Errorf("Setup: %v", err)
	}
}

// TestAdapterDoesNotImplementOptionalCapabilities documents the design
// decision: PR-200 is read-only (no Add / Complete / Delete / Since on
// upstream DOM). A future PR may layer Sincer on top using a stored
// page-hash, but PR-200 ships without these so the manifest must NOT
// declare them. The compile-time type-assertions below are the contract
// the manifest validator relies on.
func TestAdapterDoesNotImplementOptionalCapabilities(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := NewAdapter(p)
	if _, ok := any(a).(source.Sincer); ok {
		t.Errorf("adapter must NOT implement source.Sincer in PR-200")
	}
	if _, ok := any(a).(source.Adder); ok {
		t.Errorf("adapter must NOT implement source.Adder")
	}
	if _, ok := any(a).(source.Completer); ok {
		t.Errorf("adapter must NOT implement source.Completer")
	}
	if _, ok := any(a).(source.Deleter); ok {
		t.Errorf("adapter must NOT implement source.Deleter")
	}
}
