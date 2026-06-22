package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func withEditHook(t *testing.T, fn func(string) error) {
	t.Helper()
	prev := editFileHook
	editFileHook = fn
	t.Cleanup(func() { editFileHook = prev })
}

// CE1: a valid in-place edit persists, preserves the user's comments (the file
// is never re-serialised), and reports success.
func TestConfigEdit_AppliesValidChangeAndPreservesComments(t *testing.T) {
	path := configPathFlag(t)
	if err := os.WriteFile(path, []byte("# my hand-written config\n[core]\nmax_parallel = 2\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	withEditHook(t, func(p string) error {
		return os.WriteFile(p, []byte("# my hand-written config\n[core]\nmax_parallel = 4\n"), 0o600)
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config edit exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Config saved.") {
		t.Errorf("stdout=%q; want 'Config saved.'", stdout.String())
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(got), "# my hand-written config") {
		t.Errorf("comment not preserved:\n%s", got)
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"--config", path, "config", "get", "core.max_parallel"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config get exit=%d; stderr=%q", code, stderr.String())
	}
	if v := strings.TrimSpace(stdout.String()); v != "4" {
		t.Errorf("max_parallel = %q; want 4", v)
	}
}

// CE2: an edit that produces an invalid config is rolled back byte-for-byte to
// the pre-edit contents and exits non-zero.
func TestConfigEdit_InvalidRollsBackToOriginal(t *testing.T) {
	path := configPathFlag(t)
	original := "# keep me\n[core]\nmax_parallel = 2\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	withEditHook(t, func(p string) error {
		// max_parallel = 0 fails Validate (must be > 0).
		return os.WriteFile(p, []byte("[core]\nmax_parallel = 0\n"), 0o600)
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("config edit exit=0; want non-zero on invalid edit")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != original {
		t.Errorf("config not rolled back\n--- got ---\n%s\n--- want ---\n%s", got, original)
	}
}

// CE3: an invalid edit to a previously-absent file removes the half-written
// file rather than leaving an invalid config on disk.
func TestConfigEdit_InvalidOnFreshFileRemovesIt(t *testing.T) {
	path := configPathFlag(t) // does not exist yet
	withEditHook(t, func(p string) error {
		return os.WriteFile(p, []byte("this is = = not valid toml\n"), 0o600)
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("config edit exit=0; want non-zero on invalid edit")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("fresh invalid config should be removed; stat err=%v", err)
	}
}
