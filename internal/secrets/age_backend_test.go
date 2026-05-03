package secrets_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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

// TestAgeBackendGetMissingKey pins the "absent vs error" distinction:
// a Get on a key that was never Set must return ok=false with a nil
// error so source plugins can prompt for setup, not surface a fatal
// "backend broken" message. Covers both the no-vault-on-disk branch
// and the vault-exists-but-key-not-in-it branch.
func TestAgeBackendGetMissingKey(t *testing.T) {
	store, _ := openAgeStore(t)

	// No vault on disk: the file does not exist, no passphrase needed.
	if v, ok, err := store.Get("missing"); err != nil || ok || v != "" {
		t.Fatalf("Get on empty store: v=%q ok=%v err=%v; want \"\", false, nil", v, ok, err)
	}

	// Vault exists but the key is not in it: must still distinguish
	// from "backend broken".
	if err := store.Set("present", "val"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, ok, err := store.Get("missing"); err != nil || ok || v != "" {
		t.Fatalf("Get on vault without key: v=%q ok=%v err=%v; want \"\", false, nil", v, ok, err)
	}
}

// TestAgeBackendDeleteMissingIsIdempotent pins idempotency: cleanup
// paths in source plugins call Delete unconditionally, and a missing
// entry must not fail. Covers both the no-vault and vault-without-key
// branches so the no-op stays uniform.
func TestAgeBackendDeleteMissingIsIdempotent(t *testing.T) {
	store, _ := openAgeStore(t)

	// No vault on disk yet: Delete must be a clean no-op.
	if err := store.Delete("missing"); err != nil {
		t.Fatalf("Delete missing on empty store: %v; want nil", err)
	}

	// Vault exists, key does not: still a no-op.
	if err := store.Set("present", "val"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Delete("missing"); err != nil {
		t.Fatalf("Delete missing on populated store: %v; want nil", err)
	}

	// And the unrelated key is still there.
	if v, ok, err := store.Get("present"); err != nil || !ok || v != "val" {
		t.Errorf("present after no-op delete: v=%q ok=%v err=%v", v, ok, err)
	}
}

// TestAgeBackendListAlphaSorted pins deterministic ordering: List is
// surfaced in `marunage setup --list` output and to source plugin
// reflection, so unsorted iteration would produce flaky CLI output.
// Mirrors fileBackend's contract.
func TestAgeBackendListAlphaSorted(t *testing.T) {
	store, _ := openAgeStore(t)

	for _, name := range []string{"slack", "gmail", "github"} {
		if err := store.Set(name, "tok-"+name); err != nil {
			t.Fatalf("Set %s: %v", name, err)
		}
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"github", "gmail", "slack"}
	if len(got) != len(want) {
		t.Fatalf("List len = %d; want %d (got %v)", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("List[%d] = %q; want %q (full got = %v)", i, got[i], name, got)
		}
	}
}

// TestAgeBackendFilePermsAndFormat pins two facts about the on-disk
// vault that defense-in-depth depends on:
//
//   - the file is mode 0600 (otherwise a wider umask would let other
//     users read the ciphertext, and age's scrypt is the only thing
//     standing between them and the cleartext);
//   - the file starts with the canonical "age-encryption.org/v1"
//     header so a corrupt or accidentally-truncated write fails fast
//     rather than producing a "looks like JSON" file.
//
// Both invariants are easier to lose than to keep — a refactor that
// drops the explicit Chmod, or one that switches encryption libraries,
// would silently regress here without this test.
func TestAgeBackendFilePermsAndFormat(t *testing.T) {
	store, home := openAgeStore(t)
	if err := store.Set("gmail", "tok"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	path := filepath.Join(home, ".marunage", "secrets.age")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat secrets.age: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secrets.age perm = %o; want 0600 (tokens live here)", perm)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read secrets.age: %v", err)
	}
	if !bytes.HasPrefix(body, []byte("age-encryption.org/v1")) {
		t.Errorf("secrets.age does not start with age v1 header; got prefix = %q", string(body[:min(len(body), 32)]))
	}
}

// TestAgeBackendPassphraseRequiredWithoutTTYOrEnv pins the headless-
// failure mode: when MARUNAGE_AGE_PASSPHRASE is unset AND no TTY is
// attached, the backend must fail with ErrPassphraseRequired rather
// than blocking on a read that would never succeed. This is the
// failure mode `marunage setup` translates into "set
// MARUNAGE_AGE_PASSPHRASE or run from a TTY".
func TestAgeBackendPassphraseRequiredWithoutTTYOrEnv(t *testing.T) {
	home := t.TempDir()
	// Explicitly clear any inherited passphrase env so the test
	// reflects a stock CI environment.
	t.Setenv("MARUNAGE_AGE_PASSPHRASE", "")
	secrets.ResetPassphraseCacheForTest()

	// Inject a "no TTY" prompter that mimics the production code path
	// when stdin is not a terminal.
	restore := secrets.SetTTYPassphrasePrompterForTest(func(_ bool) (string, error) {
		return "", secrets.ErrPassphraseRequired
	})
	defer restore()

	store, err := secrets.Open(secrets.Config{Backend: "age", HomeDir: home})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = store.Set("gmail", "tok")
	if err == nil {
		t.Fatal("Set without env or TTY = nil; want ErrPassphraseRequired")
	}
	if !errors.Is(err, secrets.ErrPassphraseRequired) {
		t.Errorf("Set error = %v; want errors.Is(..., ErrPassphraseRequired)", err)
	}
}

// TestAgeBackendProbeAlwaysSucceeds pins the auto-select integration:
// unlike pass (which needs the `pass` binary) or keyring (which needs
// a desktop session), age has zero environmental requirements at
// construction time — the passphrase is acquired lazily on first
// Set/Get. So newAgeBackend must never return ErrUnsupported, or the
// auto-select chain would skip past it on every workstation.
func TestAgeBackendProbeAlwaysSucceeds(t *testing.T) {
	home := t.TempDir()
	// No env, no TTY override: even in the stock environment the probe
	// must succeed so the backend lands in the auto-select chain.
	store, err := secrets.Open(secrets.Config{Backend: "age", HomeDir: home})
	if err != nil {
		t.Fatalf("Open(age) failed at construction time: %v; "+
			"newAgeBackend must defer passphrase acquisition until first use", err)
	}
	if got := store.Backend(); got != "age" {
		t.Errorf("Backend() = %q; want %q", got, "age")
	}
}

// TestAgeBackendWrongPassphraseIsTypedError pins the corruption-vs-
// wrong-passphrase distinction: when an existing vault cannot be
// decrypted with the supplied passphrase, callers must get
// ErrPassphraseIncorrect (so the CLI can re-prompt) rather than a raw
// age error (which would look like file corruption to the user).
func TestAgeBackendWrongPassphraseIsTypedError(t *testing.T) {
	home := t.TempDir()

	// Seed the vault with passphrase A.
	t.Setenv("MARUNAGE_AGE_PASSPHRASE", "passphrase-A")
	secrets.ResetPassphraseCacheForTest()
	first, err := secrets.Open(secrets.Config{Backend: "age", HomeDir: home})
	if err != nil {
		t.Fatalf("Open with passphrase A: %v", err)
	}
	if err := first.Set("gmail", "tok"); err != nil {
		t.Fatalf("Set with passphrase A: %v", err)
	}

	// Re-Open with passphrase B, clearing the cache so the new env
	// value is what gets used. Get must return ErrPassphraseIncorrect,
	// not a generic "age decrypt" error string.
	t.Setenv("MARUNAGE_AGE_PASSPHRASE", "passphrase-B")
	secrets.ResetPassphraseCacheForTest()
	second, err := secrets.Open(secrets.Config{Backend: "age", HomeDir: home})
	if err != nil {
		t.Fatalf("Open with passphrase B: %v", err)
	}
	_, _, err = second.Get("gmail")
	if err == nil {
		t.Fatal("Get with wrong passphrase = nil; want ErrPassphraseIncorrect")
	}
	if !errors.Is(err, secrets.ErrPassphraseIncorrect) {
		t.Errorf("Get error = %v; want errors.Is(..., ErrPassphraseIncorrect)", err)
	}
}

// TestAgeBackendPersistsAcrossOpen pins durability: a value written by
// one Open must be readable by a fresh Open against the same HomeDir.
// Without this guarantee the daemon would lose every secret on
// restart, defeating the whole point of an on-disk vault.
func TestAgeBackendPersistsAcrossOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MARUNAGE_AGE_PASSPHRASE", "persist-passphrase")
	secrets.ResetPassphraseCacheForTest()

	first, err := secrets.Open(secrets.Config{Backend: "age", HomeDir: home})
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := first.Set("gmail", "tok-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Force a second Open with the same HomeDir, simulating a
	// process restart that comes back to the same vault file.
	second, err := secrets.Open(secrets.Config{Backend: "age", HomeDir: home})
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	got, ok, err := second.Get("gmail")
	if err != nil || !ok {
		t.Fatalf("Get on second Open: ok=%v err=%v", ok, err)
	}
	if got != "tok-1" {
		t.Errorf("Get value = %q; want %q (persistence broken)", got, "tok-1")
	}
}
