package secrets_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haruotsu/marunage/internal/secrets"
)

// openFileStore wires the file backend at a temp HomeDir so the test
// never touches ~/.marunage. Returning the directory along with the
// store keeps perm-asserting tests below from re-deriving the path.
func openFileStore(t *testing.T) (secrets.Store, string) {
	t.Helper()
	home := t.TempDir()
	store, err := secrets.Open(secrets.Config{Backend: "file", HomeDir: home})
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	return store, home
}

// TestFileBackendSetGetListDelete pins the smallest contract: the value
// you wrote is the value you read; List sees the name; Delete removes
// it; a fresh Get returns ok=false with no error so source plugins can
// distinguish "never set" from "backend broken".
func TestFileBackendSetGetListDelete(t *testing.T) {
	store, _ := openFileStore(t)

	if _, ok, err := store.Get("gmail"); err != nil || ok {
		t.Fatalf("Get on empty: ok=%v err=%v; want ok=false err=nil", ok, err)
	}

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

	names, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "gmail" {
		t.Errorf("List = %v; want [gmail]", names)
	}

	if err := store.Delete("gmail"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := store.Get("gmail"); err != nil || ok {
		t.Errorf("Get after Delete: ok=%v err=%v; want ok=false err=nil", ok, err)
	}

	// Deleting a missing key is idempotent so cleanup paths do not have
	// to special-case "is this gone yet?".
	if err := store.Delete("gmail"); err != nil {
		t.Errorf("Delete missing: %v; want nil (idempotent)", err)
	}
}

// TestFileBackendBackendName pins the identifier that `marunage setup
// --list` will print so users can tell at a glance which backend is in
// use. A change here would silently confuse the operator-facing output.
func TestFileBackendBackendName(t *testing.T) {
	store, _ := openFileStore(t)
	if got := store.Backend(); got != "file" {
		t.Errorf("Backend() = %q; want %q", got, "file")
	}
}

// TestFileBackendPermsTighten mirrors the assertion style in
// internal/store/store_test.go: the parent dir is 0700 and each persisted
// secret file is 0600, even if the user's umask is wider. tasks.body
// lives in tasks.db with the same perms; gmail tokens here deserve at
// least the same protection. We pre-create the parent at 0755 to force
// the tighten-existing-dir branch (invisible on macOS where TMPDIR is
// already 0700, real on Linux CI where TMPDIR=/tmp is 0755).
func TestFileBackendPermsTighten(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, ".marunage", "secrets")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("seed parent at 0755: %v", err)
	}

	store, err := secrets.Open(secrets.Config{Backend: "file", HomeDir: home})
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	if err := store.Set("gmail", "tok"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	dirInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("parent dir perm = %o; want 0700", perm)
	}

	fileInfo, err := os.Stat(filepath.Join(parent, "gmail.json"))
	if err != nil {
		t.Fatalf("stat secret file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("secret file perm = %o; want 0600 (tokens live here)", perm)
	}
}
