package secrets_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/secrets"
)

// fakeStore is a minimal in-memory Store used only to drive the
// auto-select algorithm in tests. The factory closures below decide
// whether a given backend "succeeds" or "is unavailable".
type fakeStore struct {
	name string
}

func (f fakeStore) Backend() string                        { return f.name }
func (fakeStore) Get(string) (string, bool, error)         { return "", false, nil }
func (fakeStore) Set(string, string) error                 { return nil }
func (fakeStore) Delete(string) error                      { return nil }
func (fakeStore) List() ([]string, error)                  { return nil, nil }

// availableFactory returns a Store advertising backendName.
func availableFactory(backendName string) secrets.BackendFactory {
	return func(_ secrets.Config) (secrets.Store, error) {
		return fakeStore{name: backendName}, nil
	}
}

// unavailableFactory mimics a backend whose probe failed (binary
// missing, stub awaiting PR-31, etc.).
func unavailableFactory() secrets.BackendFactory {
	return func(_ secrets.Config) (secrets.Store, error) {
		return nil, secrets.ErrUnsupported
	}
}

// TestAutoSelectPicksKeyringWhenAvailable pins the documented probe
// order: when both keyring and file work, keyring wins. A regression
// here would silently downgrade workstation users from the OS keychain
// to the plaintext file backend.
func TestAutoSelectPicksKeyringWhenAvailable(t *testing.T) {
	factories := map[string]secrets.BackendFactory{
		"keyring": availableFactory("keyring"),
		"pass":    unavailableFactory(),
		"age":     unavailableFactory(),
		"file":    availableFactory("file"),
		"env":     availableFactory("env"),
	}

	store, err := secrets.OpenWithFactories(secrets.Config{Backend: "auto"}, factories)
	if err != nil {
		t.Fatalf("OpenWithFactories: %v", err)
	}
	if got := store.Backend(); got != "keyring" {
		t.Errorf("auto picked %q; want keyring (highest priority available backend)", got)
	}
}

// TestAutoSelectFallsThroughToFile pins step-down: when keyring/pass/age
// are all unavailable, the file backend takes over. file is the
// always-available 0600 fallback per docs/requirement.md.
func TestAutoSelectFallsThroughToFile(t *testing.T) {
	factories := map[string]secrets.BackendFactory{
		"keyring": unavailableFactory(),
		"pass":    unavailableFactory(),
		"age":     unavailableFactory(),
		"file":    availableFactory("file"),
		"env":     availableFactory("env"),
	}

	store, err := secrets.OpenWithFactories(secrets.Config{Backend: "auto"}, factories)
	if err != nil {
		t.Fatalf("OpenWithFactories: %v", err)
	}
	if got := store.Backend(); got != "file" {
		t.Errorf("auto picked %q; want file (only working backend)", got)
	}
}

// TestAutoSelectSkipsErrUnsupportedStubs is the core invariant of the
// auto-select algorithm: pass and age are stubs that must not break
// auto resolution on PR-30. ErrUnsupported is the sentinel that means
// "skip me cleanly"; any other error from a probe must surface.
func TestAutoSelectSkipsErrUnsupportedStubs(t *testing.T) {
	factories := map[string]secrets.BackendFactory{
		"keyring": unavailableFactory(),
		"pass":    unavailableFactory(),
		"age":     unavailableFactory(),
		"file":    availableFactory("file"),
	}

	store, err := secrets.OpenWithFactories(secrets.Config{Backend: "auto"}, factories)
	if err != nil {
		t.Fatalf("auto must skip ErrUnsupported stubs without error: %v", err)
	}
	if got := store.Backend(); got != "file" {
		t.Errorf("auto picked %q; want file", got)
	}
}

// TestAutoSelectExcludesEnv pins the security-critical decision spelled
// out in the package doc: env is NEVER chosen by auto, even when
// keyring/pass/age/file all fail. Otherwise a misconfigured workstation
// would silently appear to "work" while reading process environment
// instead of asking the user to set up a real backend.
func TestAutoSelectExcludesEnv(t *testing.T) {
	factories := map[string]secrets.BackendFactory{
		"keyring": unavailableFactory(),
		"pass":    unavailableFactory(),
		"age":     unavailableFactory(),
		"file":    unavailableFactory(),
		"env":     availableFactory("env"),
	}

	_, err := secrets.OpenWithFactories(secrets.Config{Backend: "auto"}, factories)
	if err == nil {
		t.Fatal("auto resolved to a backend; want error (env must require explicit opt-in)")
	}
}

// TestExplicitFileSkipsKeyring pins the deterministic-override contract:
// a user who pinned secrets.backend = "file" gets file even when keyring
// would have probed successfully. Otherwise the override is meaningless.
func TestExplicitFileSkipsKeyring(t *testing.T) {
	probedKeyring := false
	factories := map[string]secrets.BackendFactory{
		"keyring": func(_ secrets.Config) (secrets.Store, error) {
			probedKeyring = true
			return fakeStore{name: "keyring"}, nil
		},
		"file": availableFactory("file"),
	}

	store, err := secrets.OpenWithFactories(secrets.Config{Backend: "file"}, factories)
	if err != nil {
		t.Fatalf("OpenWithFactories: %v", err)
	}
	if got := store.Backend(); got != "file" {
		t.Errorf("explicit file picked %q; want file", got)
	}
	if probedKeyring {
		t.Errorf("explicit Backend=file must not probe keyring")
	}
}

// TestExplicitEnvOverride pins the dual override contract: a user who
// asked for env gets env, even if no env var is currently set. Reading
// MARUNAGE_FOO_TOKEN happens at Get time, not Open time, so the backend
// itself must construct successfully.
func TestExplicitEnvOverride(t *testing.T) {
	store, err := secrets.Open(secrets.Config{Backend: "env"})
	if err != nil {
		t.Fatalf("Open(env): %v", err)
	}
	if got := store.Backend(); got != "env" {
		t.Errorf("Backend() = %q; want env", got)
	}
}

// TestUnknownBackendRejectedBeforeProbe is the schema guard: a typo in
// config.toml should fail before any backend touches disk, so the user
// sees "unknown backend 'flie'" rather than a half-created
// ~/.marunage/secrets/ directory.
func TestUnknownBackendRejectedBeforeProbe(t *testing.T) {
	probed := false
	factories := map[string]secrets.BackendFactory{
		"file": func(_ secrets.Config) (secrets.Store, error) {
			probed = true
			return fakeStore{name: "file"}, nil
		},
	}

	_, err := secrets.OpenWithFactories(secrets.Config{Backend: "garbage"}, factories)
	if err == nil {
		t.Fatal("Open with unknown backend = nil; want validation error")
	}
	if !errors.Is(err, secrets.ErrUnknownBackend) {
		t.Errorf("error must wrap ErrUnknownBackend so callers can branch on errors.Is; got %v", err)
	}
	if !strings.Contains(err.Error(), "garbage") {
		t.Errorf("error must mention the offending value; got %v", err)
	}
	if probed {
		t.Errorf("unknown backend must be rejected before any factory is called")
	}
}

// TestAutoOrderIsKeyringFirst guards against an accidental reorder of
// the documented probe sequence. Without this, a refactor that put
// "file" before "keyring" would silently downgrade every workstation.
func TestAutoOrderIsKeyringFirst(t *testing.T) {
	order := secrets.AutoOrder()
	want := []string{"keyring", "pass", "age", "file"}
	if len(order) != len(want) {
		t.Fatalf("AutoOrder length = %d; want %d", len(order), len(want))
	}
	for i, name := range want {
		if order[i] != name {
			t.Errorf("AutoOrder[%d] = %q; want %q", i, order[i], name)
		}
	}
}
