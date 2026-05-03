package source

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// regStubPlugin gives the registry tests a Plugin that does the minimum the
// interface requires. We do not reuse stubPlugin from source_test.go because
// these tests assert that the *same* instance comes back from Get, so the
// type must allow distinguishing identity (a value receiver on a struct
// would let two unrelated instances compare equal).
type regStubPlugin struct {
	name string
}

func (p *regStubPlugin) Name() string                              { return p.name }
func (p *regStubPlugin) List(context.Context) ([]Task, error)      { return nil, nil }
func (p *regStubPlugin) Setup(context.Context, SetupOptions) error { return nil }
func (p *regStubPlugin) AuthStatus(context.Context) (AuthStatus, error) {
	return AuthAuthenticated, nil
}

func TestRegistryRegisterAndGetReturnsSameInstance(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	p := &regStubPlugin{name: "stub"}
	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Get("stub")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != p {
		t.Fatalf("Get returned a different pointer: %p vs %p", got, p)
	}
}

func TestRegistryGetUnknownReturnsTypedError(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	_, err := r.Get("nope")
	if !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("want ErrPluginNotFound, got %v", err)
	}
}

func TestRegistryRejectsDuplicateName(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if err := r.Register(&regStubPlugin{name: "a"}); err != nil {
		t.Fatalf("Register#1: %v", err)
	}
	err := r.Register(&regStubPlugin{name: "a"})
	if !errors.Is(err, ErrPluginAlreadyRegistered) {
		t.Fatalf("want ErrPluginAlreadyRegistered, got %v", err)
	}
}

func TestRegistryRejectsEmptyName(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	err := r.Register(&regStubPlugin{name: ""})
	if err == nil {
		t.Fatalf("Register accepted empty name")
	}
}

func TestRegistryNamesIsSorted(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	for _, n := range []string{"gmail", "markdown", "slack"} {
		if err := r.Register(&regStubPlugin{name: n}); err != nil {
			t.Fatalf("Register %s: %v", n, err)
		}
	}
	got := r.Names()
	want := []string{"gmail", "markdown", "slack"}
	if len(got) != len(want) {
		t.Fatalf("Names = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names = %v, want sorted %v", got, want)
		}
	}
}

func TestRegistryConcurrentRegisterIsSafe(t *testing.T) {
	t.Parallel()

	// Race detector is the assertion here; the test passes if -race does
	// not flag the registry's internal map.
	r := NewRegistry()
	var wg sync.WaitGroup
	const n = 16
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			p := &regStubPlugin{name: fixedName(i)}
			_ = r.Register(p)
			_, _ = r.Get(p.name)
		}(i)
	}
	wg.Wait()
}

func fixedName(i int) string {
	const letters = "abcdefghijklmnop"
	return string(letters[i%len(letters)]) + "-plugin"
}

// TestValidateAgainstManifestRequiresMatchingCapabilities locks in the
// "capability ↔ interface" cross-check that the brief calls out: a manifest
// declaring `add` is a lie if the plugin does not implement Adder, so
// startup must refuse the registration outright instead of letting the
// dispatch loop discover the mismatch at first invocation.
func TestValidateAgainstManifestRequiresMatchingCapabilities(t *testing.T) {
	t.Parallel()

	// regStubPlugin only satisfies the mandatory interface — it does not
	// implement Adder. A manifest that promises `add` must therefore fail.
	m := &Manifest{
		Name:         "stub",
		Version:      "0.1",
		SyncMode:     SyncModeBidirectional,
		Capabilities: []Capability{CapList, CapSetup, CapAuthStatus, CapAdd},
	}
	err := ValidateAgainstManifest(&regStubPlugin{name: "stub"}, m)
	if !errors.Is(err, ErrCapabilityMismatch) {
		t.Fatalf("want ErrCapabilityMismatch, got %v", err)
	}
}

// fullPlugin satisfies all four optional interfaces so we can assert the
// happy path: a manifest declaring everything matches a plugin implementing
// everything.
type fullPlugin struct{ regStubPlugin }

func (p *fullPlugin) Since(context.Context, string) ([]Task, error) {
	return nil, nil
}
func (p *fullPlugin) Add(context.Context, string, string) (Task, error) {
	return Task{}, nil
}
func (p *fullPlugin) Complete(context.Context, string) error { return nil }
func (p *fullPlugin) Delete(context.Context, string) error   { return nil }

func TestValidateAgainstManifestPassesWhenInterfacesMatch(t *testing.T) {
	t.Parallel()

	p := &fullPlugin{regStubPlugin: regStubPlugin{name: "full"}}
	m := &Manifest{
		Name:         "full",
		Version:      "0.1",
		SyncMode:     SyncModeBidirectional,
		Capabilities: []Capability{CapList, CapSetup, CapAuthStatus, CapSince, CapAdd, CapComplete, CapDelete},
	}
	if err := ValidateAgainstManifest(p, m); err != nil {
		t.Fatalf("ValidateAgainstManifest: %v", err)
	}
}

// TestValidateAgainstManifestRejectsNameMismatch keeps the manifest's
// `plugin.name` honest. If a registry caller hands in a manifest for the
// wrong plugin, the cross-check catches it before the registry stores the
// wrong (name, plugin) pair.
func TestValidateAgainstManifestRejectsNameMismatch(t *testing.T) {
	t.Parallel()

	p := &regStubPlugin{name: "markdown"}
	m := &Manifest{
		Name:         "gmail",
		Version:      "0.1",
		SyncMode:     SyncModeReadOnly,
		Capabilities: []Capability{CapList, CapSetup, CapAuthStatus},
	}
	err := ValidateAgainstManifest(p, m)
	if err == nil {
		t.Fatalf("want error for name mismatch, got nil")
	}
}
