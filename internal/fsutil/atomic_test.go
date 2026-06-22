package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWrite_CreatesFileWithModeAndContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := AtomicWrite(path, []byte("hello"), 0o640); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q; want hello", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("mode = %o; want 0640", perm)
	}
}

func TestAtomicWrite_OverwritesExistingAndLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := AtomicWrite(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q; want new", got)
	}
	// No leftover tmp siblings.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir has %d entries %v; want only the target (no stray tmp)", len(entries), names)
	}
}

func TestAtomicWrite_MissingDirReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "out.txt")
	if err := AtomicWrite(path, []byte("x"), 0o600); err == nil {
		t.Fatal("AtomicWrite to a missing directory returned nil; want an error (caller must ensure the dir exists)")
	}
}
