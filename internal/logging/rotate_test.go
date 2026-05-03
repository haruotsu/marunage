package logging_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/logging"
)

func backupsOf(t *testing.T, base string) []string {
	t.Helper()
	dir := filepath.Dir(base)
	name := filepath.Base(base)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if e.Name() == name {
			continue
		}
		if !strings.HasPrefix(e.Name(), name+".") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// TestRotatingFileRotatesAtMaxBytes pins the size threshold contract: as
// soon as a Write would push the file past MaxBytes, the current file is
// renamed to <name>.<timestamp> and a fresh file takes its place. Rotation
// happens *before* the new write so callers never see a single file exceed
// the limit.
func TestRotatingFileRotatesAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	r, err := logging.NewRotatingFile(path, 10, 5)
	if err != nil {
		t.Fatalf("NewRotatingFile: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if _, err := r.Write([]byte("0123456789")); err != nil { // exactly 10 bytes
		t.Fatalf("Write 1: %v", err)
	}
	if _, err := r.Write([]byte("abcdefghij")); err != nil { // would push to 20
		t.Fatalf("Write 2: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile current: %v", err)
	}
	if string(current) != "abcdefghij" {
		t.Errorf("current file = %q; want %q (post-rotation content only)", current, "abcdefghij")
	}

	backups := backupsOf(t, path)
	if len(backups) != 1 {
		t.Fatalf("backups = %v; want exactly 1", backups)
	}
	rotated, err := os.ReadFile(filepath.Join(dir, backups[0]))
	if err != nil {
		t.Fatalf("ReadFile rotated: %v", err)
	}
	if string(rotated) != "0123456789" {
		t.Errorf("rotated file = %q; want %q", rotated, "0123456789")
	}
}

// TestRotatingFilePrunesBackups: under sustained writes, only MaxBackups
// rotated files remain. This is the actual long-running daemon scenario —
// without pruning, daemon.log.* would grow without bound and re-create the
// problem rotation was supposed to solve.
func TestRotatingFilePrunesBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	r, err := logging.NewRotatingFile(path, 4, 2)
	if err != nil {
		t.Fatalf("NewRotatingFile: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Each write is 4 bytes and exactly fills the file; the next write
	// triggers a rotation. After 5 writes we expect 4 rotations to have
	// happened, but only 2 backups should remain on disk.
	payloads := []string{"AAAA", "BBBB", "CCCC", "DDDD", "EEEE"}
	for i, p := range payloads {
		if _, err := r.Write([]byte(p)); err != nil {
			t.Fatalf("Write %d (%q): %v", i, p, err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	backups := backupsOf(t, path)
	if len(backups) != 2 {
		t.Fatalf("backups = %v; want 2 (MaxBackups=2)", backups)
	}
}

// TestRotatingFileAppendsToExistingFile guards the daemon-restart case: a
// previously-written daemon.log must be re-opened in append mode so the
// freshly-started process does not blow away earlier entries.
func TestRotatingFileAppendsToExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	if err := os.WriteFile(path, []byte("seed\n"), 0o600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	r, err := logging.NewRotatingFile(path, 1024, 3)
	if err != nil {
		t.Fatalf("NewRotatingFile: %v", err)
	}
	if _, err := r.Write([]byte("after\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "seed\nafter\n" {
		t.Errorf("contents = %q; want %q (no truncation on reopen)", got, "seed\nafter\n")
	}
}
