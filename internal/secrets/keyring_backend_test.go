package secrets_test

import (
	"sort"
	"testing"

	"github.com/haruotsu/marunage/internal/secrets"
	"github.com/zalando/go-keyring"
)

// withMockKeyring swaps the go-keyring backend for an in-memory mock so
// the test never touches the real macOS Keychain / Secret Service. The
// upstream library exposes MockInit() exactly for this; see
// github.com/zalando/go-keyring keyring_mock.go.
func withMockKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
}

// TestKeyringBackendRoundTrip pins the smallest contract: Set then Get
// returns the value, List sees the name (best-effort in-process index),
// Delete removes it, and a fresh Get is ok=false / err=nil.
func TestKeyringBackendRoundTrip(t *testing.T) {
	withMockKeyring(t)

	store, err := secrets.Open(secrets.Config{Backend: "keyring"})
	if err != nil {
		t.Fatalf("Open(keyring): %v", err)
	}

	if _, ok, err := store.Get("gmail"); err != nil || ok {
		t.Fatalf("Get on empty: ok=%v err=%v; want ok=false err=nil", ok, err)
	}

	if err := store.Set("gmail", "tok-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := store.Get("gmail")
	if err != nil || !ok {
		t.Fatalf("Get after Set: ok=%v err=%v", ok, err)
	}
	if got != "tok-1" {
		t.Errorf("Get value = %q; want %q", got, "tok-1")
	}

	names, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "gmail" {
		t.Errorf("List = %v; want [gmail] (in-process index)", names)
	}

	if err := store.Delete("gmail"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := store.Get("gmail"); err != nil || ok {
		t.Errorf("Get after Delete: ok=%v err=%v; want ok=false err=nil", ok, err)
	}
}

// TestKeyringBackendName pins the operator-facing identifier so
// `marunage setup --list` cannot silently change.
func TestKeyringBackendName(t *testing.T) {
	withMockKeyring(t)

	store, err := secrets.Open(secrets.Config{Backend: "keyring"})
	if err != nil {
		t.Fatalf("Open(keyring): %v", err)
	}
	if got := store.Backend(); got != "keyring" {
		t.Errorf("Backend() = %q; want %q", got, "keyring")
	}
}

// TestKeyringBackendDeleteMissingIsIdempotent mirrors the file backend
// contract: cleaning up a key that was already removed is a no-op so
// teardown paths do not need to check existence first.
func TestKeyringBackendDeleteMissingIsIdempotent(t *testing.T) {
	withMockKeyring(t)

	store, err := secrets.Open(secrets.Config{Backend: "keyring"})
	if err != nil {
		t.Fatalf("Open(keyring): %v", err)
	}
	if err := store.Delete("never-set"); err != nil {
		t.Errorf("Delete missing: %v; want nil", err)
	}
}
