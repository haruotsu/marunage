package secrets_test

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/secrets"
)

// TestEnvBackendReadsMarunageEnvVar pins the documented contract from
// docs/requirement.md: the env backend reads MARUNAGE_<UPPER_NAME>_TOKEN.
// Source plugins (gmail, slack...) hand the backend a lower-case source
// name; we upper-case it so the env var convention is consistent regardless
// of how the caller capitalised the input.
func TestEnvBackendReadsMarunageEnvVar(t *testing.T) {
	t.Setenv("MARUNAGE_GMAIL_TOKEN", "env-tok")

	store, err := secrets.Open(secrets.Config{Backend: "env"})
	if err != nil {
		t.Fatalf("Open(env): %v", err)
	}

	got, ok, err := store.Get("gmail")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("Get ok=false; want true (env var was set)")
	}
	if got != "env-tok" {
		t.Errorf("Get value = %q; want %q", got, "env-tok")
	}

	if got := store.Backend(); got != "env" {
		t.Errorf("Backend() = %q; want %q", got, "env")
	}
}

// TestEnvBackendMissingVarIsNotAnError mirrors the file backend's
// "absent secret returns ok=false, no error" contract so callers can
// branch on a single shape across every backend.
func TestEnvBackendMissingVarIsNotAnError(t *testing.T) {
	store, err := secrets.Open(secrets.Config{Backend: "env"})
	if err != nil {
		t.Fatalf("Open(env): %v", err)
	}

	// Make sure the test never reads a leaked env from the surrounding
	// shell; t.Setenv to empty + Unsetenv would also work.
	t.Setenv("MARUNAGE_NOTSET_TOKEN", "")

	_, ok, err := store.Get("notset-but-empty")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Errorf("Get ok=true; want false for unset env var")
	}
}

// TestEnvBackendIsReadOnly enforces the spec: env is a CI / Docker
// fallback, not a writable store. Tools that try to Set must hit a
// typed ErrReadOnly so callers can present the error verbatim
// ("configure MARUNAGE_<NAME>_TOKEN externally") rather than guessing
// from a generic backend failure.
func TestEnvBackendIsReadOnly(t *testing.T) {
	store, err := secrets.Open(secrets.Config{Backend: "env"})
	if err != nil {
		t.Fatalf("Open(env): %v", err)
	}

	if err := store.Set("gmail", "x"); !errors.Is(err, secrets.ErrReadOnly) {
		t.Errorf("Set err = %v; want ErrReadOnly", err)
	}
	if err := store.Delete("gmail"); !errors.Is(err, secrets.ErrReadOnly) {
		t.Errorf("Delete err = %v; want ErrReadOnly", err)
	}
}
