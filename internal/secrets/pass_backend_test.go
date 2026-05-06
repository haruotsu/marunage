package secrets_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/haruotsu/marunage/internal/secrets"
)

// captureRunner records the last call made through it and returns a
// preset response. Build one with newCaptureRunner.
type captureRunner struct {
	output []byte
	err    error

	gotStdin string
	gotName  string
	gotArgs  []string
}

func newCaptureRunner(output []byte, runErr error) (*captureRunner, func(io.Reader, string, ...string) ([]byte, error)) {
	cr := &captureRunner{output: output, err: runErr}
	fn := func(stdin io.Reader, name string, args ...string) ([]byte, error) {
		cr.gotName = name
		cr.gotArgs = append([]string(nil), args...)
		if stdin != nil {
			b, _ := io.ReadAll(stdin)
			cr.gotStdin = string(b)
		}
		return cr.output, cr.err
	}
	return cr, fn
}

// equalSlice is a simple equality helper for []string test assertions.
func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- Test 1 & 2: probePassAvailable ---

func TestProbePassAvailable_BinaryMissing(t *testing.T) {
	t.Setenv("PATH", "")
	if err := secrets.ProbePassAvailableForTest(); err == nil {
		t.Error("probePassAvailable with empty PATH must return error; got nil")
	}
}

func TestProbePassAvailable_BinaryPresent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pass")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := secrets.ProbePassAvailableForTest(); err != nil {
		t.Errorf("probePassAvailable with pass in PATH must return nil; got %v", err)
	}
}

// --- Test 3: Backend() ---

func TestPassBackend_Backend(t *testing.T) {
	_, runner := newCaptureRunner(nil, nil)
	store := secrets.NewPassBackendForTest(t.TempDir(), runner)
	if got := store.Backend(); got != "pass" {
		t.Errorf("Backend() = %q; want pass", got)
	}
}

// --- Test 4: Set ---

func TestPassBackend_Set(t *testing.T) {
	cr, runner := newCaptureRunner(nil, nil)
	store := secrets.NewPassBackendForTest(t.TempDir(), runner)

	if err := store.Set("mytoken", "supersecret"); err != nil {
		t.Fatalf("Set: unexpected error: %v", err)
	}

	if cr.gotName != "pass" {
		t.Errorf("Set: exec binary = %q; want pass", cr.gotName)
	}
	wantArgs := []string{"insert", "-m", "-f", "marunage/mytoken"}
	if !equalSlice(cr.gotArgs, wantArgs) {
		t.Errorf("Set: args = %v; want %v", cr.gotArgs, wantArgs)
	}
	if cr.gotStdin != "supersecret" {
		t.Errorf("Set: stdin = %q; want supersecret", cr.gotStdin)
	}
}

// --- Test 5: Get (entry exists) ---

func TestPassBackend_Get(t *testing.T) {
	output := []byte("supersecret\nsome other line\n")
	cr, runner := newCaptureRunner(output, nil)
	store := secrets.NewPassBackendForTest(t.TempDir(), runner)

	val, ok, err := store.Get("mytoken")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false; want true")
	}
	if val != "supersecret" {
		t.Errorf("Get: value = %q; want supersecret", val)
	}
	if cr.gotName != "pass" {
		t.Errorf("Get: exec binary = %q; want pass", cr.gotName)
	}
	wantArgs := []string{"show", "marunage/mytoken"}
	if !equalSlice(cr.gotArgs, wantArgs) {
		t.Errorf("Get: args = %v; want %v", cr.gotArgs, wantArgs)
	}
}

// --- Test 6: Get on missing entry ---

func TestPassBackend_Get_Missing(t *testing.T) {
	notFoundErr := errors.New("exit status 1: Error: marunage/missing is not in the password store.")
	_, runner := newCaptureRunner(nil, notFoundErr)
	store := secrets.NewPassBackendForTest(t.TempDir(), runner)

	val, ok, err := store.Get("missing")
	if err != nil {
		t.Fatalf("Get missing: must return nil error; got %v", err)
	}
	if ok {
		t.Fatal("Get missing: ok = true; want false")
	}
	if val != "" {
		t.Errorf("Get missing: value = %q; want empty", val)
	}
}

// --- Test 7: Delete ---

func TestPassBackend_Delete(t *testing.T) {
	cr, runner := newCaptureRunner(nil, nil)
	store := secrets.NewPassBackendForTest(t.TempDir(), runner)

	if err := store.Delete("mytoken"); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	if cr.gotName != "pass" {
		t.Errorf("Delete: exec binary = %q; want pass", cr.gotName)
	}
	wantArgs := []string{"rm", "-f", "marunage/mytoken"}
	if !equalSlice(cr.gotArgs, wantArgs) {
		t.Errorf("Delete: args = %v; want %v", cr.gotArgs, wantArgs)
	}
}

// --- Test 8: Delete on missing entry (idempotent) ---

func TestPassBackend_Delete_Missing(t *testing.T) {
	notFoundErr := errors.New("exit status 1: Error: marunage/missing is not in the password store.")
	_, runner := newCaptureRunner(nil, notFoundErr)
	store := secrets.NewPassBackendForTest(t.TempDir(), runner)

	if err := store.Delete("missing"); err != nil {
		t.Fatalf("Delete missing must be idempotent; got error: %v", err)
	}
}

// --- Test 9: List ---

func TestPassBackend_List(t *testing.T) {
	storeDir := t.TempDir()
	marunageDir := filepath.Join(storeDir, "marunage")
	if err := os.MkdirAll(marunageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, fname := range []string{"zebra.gpg", "alpha.gpg", "beta.gpg"} {
		if err := os.WriteFile(filepath.Join(marunageDir, fname), []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Non-.gpg file must be excluded from the listing.
	if err := os.WriteFile(filepath.Join(marunageDir, "readme.txt"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	_, runner := newCaptureRunner(nil, nil)
	store := secrets.NewPassBackendForTest(storeDir, runner)

	names, err := store.List()
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	want := []string{"alpha", "beta", "zebra"}
	if !equalSlice(names, want) {
		t.Errorf("List: %v; want %v", names, want)
	}
}

// --- Tests for isPassNotFound ---

// TestIsPassNotFound_NotFoundString verifies detection of pass's "not in
// the password store" message embedded in the error text.
func TestIsPassNotFound_NotFoundString(t *testing.T) {
	err := errors.New("exit status 1: Error: marunage/x is not in the password store.")
	if !secrets.IsPassNotFoundForTest(err) {
		t.Error("isPassNotFound must return true for not-in-store message")
	}
}

// TestIsPassNotFound_OtherError ensures that unrelated errors are not
// misidentified as "not found".
func TestIsPassNotFound_OtherError(t *testing.T) {
	err := errors.New("exit status 1: GPG decryption failed")
	if secrets.IsPassNotFoundForTest(err) {
		t.Error("isPassNotFound must return false for non-not-found errors")
	}
}

// TestIsPassNotFound_Nil confirms nil does not panic or return true.
func TestIsPassNotFound_Nil(t *testing.T) {
	if secrets.IsPassNotFoundForTest(nil) {
		t.Error("isPassNotFound must return false for nil")
	}
}

// TestPassBackend_List_EmptyStore verifies that List on a store with no
// marunage/ directory returns nil, nil (not an error).
func TestPassBackend_List_EmptyStore(t *testing.T) {
	_, runner := newCaptureRunner(nil, nil)
	store := secrets.NewPassBackendForTest(t.TempDir(), runner)

	names, err := store.List()
	if err != nil {
		t.Fatalf("List on empty store: unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List on empty store: %v; want empty", names)
	}
}
