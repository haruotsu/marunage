package secrets_test

import (
	"os"
	"testing"

	"github.com/haruotsu/marunage/internal/secrets"
)

// TestMain lowers age's scrypt work factor for the entire package so the
// suite finishes in seconds under `-race -count=2`. Production keeps
// the default (15); the test override (10) trades brute-force margin
// for runtime, which is the right call for fixture passphrases that
// never leave the temp dir.
func TestMain(m *testing.M) {
	restore := secrets.SetAgeScryptLogNForTest(10)
	defer restore()
	os.Exit(m.Run())
}

// openAgeStore wires the age backend at a temp HomeDir with a known
// passphrase so each test runs without touching ~/.marunage and without
// any TTY interaction. It mirrors the openFileStore helper above.
func openAgeStore(t *testing.T) (secrets.Store, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("MARUNAGE_AGE_PASSPHRASE", "test-passphrase")
	secrets.ResetPassphraseCacheForTest()
	store, err := secrets.Open(secrets.Config{Backend: "age", HomeDir: home})
	if err != nil {
		t.Fatalf("Open(age): %v", err)
	}
	return store, home
}

// TestAgeBackendRoundTripWithEnvPassphrase pins the headline contract:
// a Set/Get round-trip succeeds when the passphrase comes from
// MARUNAGE_AGE_PASSPHRASE so CI / Docker / tests never hit a TTY prompt.
func TestAgeBackendRoundTripWithEnvPassphrase(t *testing.T) {
	store, _ := openAgeStore(t)

	if err := store.Set("gmail", "tok-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := store.Get("gmail")
	if err != nil || !ok {
		t.Fatalf("Get after Set: ok=%v err=%v; want ok=true err=nil", ok, err)
	}
	if got != "tok-1" {
		t.Errorf("Get value = %q; want %q", got, "tok-1")
	}
}
