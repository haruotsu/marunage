package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

// CE6: a successful edit records a "config.edit" line to audit.log. The audit
// write is best-effort (errors swallowed), so without this test a regression
// that drops the record would go unnoticed.
func TestConfigEdit_RecordsAuditEvent(t *testing.T) {
	path := configPathFlag(t)
	if err := os.WriteFile(path, []byte("[core]\nmax_parallel = 2\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	withEditHook(t, func(p string) error {
		return os.WriteFile(p, []byte("[core]\nmax_parallel = 4\n"), 0o600)
	})

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr); code != 0 {
		t.Fatalf("config edit exit=%d; stderr=%q", code, stderr.String())
	}

	if !auditHasAction(t, auditLogPathFor(path), "config.edit") {
		t.Errorf("audit.log missing a config.edit record after a successful edit")
	}
}

// CE7: a successful edit on an existing config leaves exactly one timestamped
// .bak snapshot holding the PRE-edit content, mirroring config.Save's backup so
// a bad edit stays recoverable even after the editor has overwritten the file.
func TestConfigEdit_SnapshotsPreEditBackup(t *testing.T) {
	path := configPathFlag(t)
	original := "# keep me\n[core]\nmax_parallel = 2\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	withEditHook(t, func(p string) error {
		return os.WriteFile(p, []byte("[core]\nmax_parallel = 4\n"), 0o600)
	})

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr); code != 0 {
		t.Fatalf("config edit exit=%d; stderr=%q", code, stderr.String())
	}

	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one .bak snapshot; got %v", matches)
	}
	got, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != original {
		t.Errorf("backup content = %q; want the pre-edit original %q", got, original)
	}
}

// CE8: an invalid edit that rolls back leaves no .bak snapshot — nothing was
// committed, so there is nothing to recover and we keep the directory clean.
// The edit hook genuinely rewrites the file (and the result is rolled back), so
// this drives the rollback path: an implementation that snapshotted before
// validation, or unconditionally, would leave a .bak here and fail.
func TestConfigEdit_InvalidEditWritesNoBackup(t *testing.T) {
	path := configPathFlag(t)
	original := "# keep me\n[core]\nmax_parallel = 2\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	editorCalled := false
	withEditHook(t, func(p string) error {
		editorCalled = true
		return os.WriteFile(p, []byte("[core]\nmax_parallel = 0\n"), 0o600)
	})

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr); code == 0 {
		t.Fatalf("config edit exit=0; want non-zero on invalid edit")
	}
	if !editorCalled {
		t.Fatal("edit hook was not called; the rollback path was never exercised")
	}
	// The rollback path ran: the file is back to its pre-edit contents...
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != original {
		t.Errorf("config not rolled back: got %q", got)
	}
	// ...and no snapshot was written for the discarded edit.
	matches, _ := filepath.Glob(path + ".bak.*")
	if len(matches) != 0 {
		t.Errorf("invalid edit should leave no .bak snapshot; got %v", matches)
	}
}

// auditHasAction reports whether the audit log at path contains at least one
// JSON line whose action field equals want.
func auditHasAction(t *testing.T, path, want string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit log %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var line struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("unmarshal audit line %q: %v", scanner.Text(), err)
		}
		if line.Action == want {
			return true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan audit log: %v", err)
	}
	return false
}

// CE4: a non-ENOENT read error on the config path aborts the edit before the
// editor runs and never touches the path. A directory at the config path makes
// os.ReadFile fail with EISDIR — the portable stand-in for a transient read
// failure on an existing file. Without the guard, existed would be false, the
// editor would run, and the rollback's os.Remove would delete the real path
// (data loss).
func TestConfigEdit_UnreadableConfigAbortsWithoutTouchingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	editorCalled := false
	withEditHook(t, func(string) error {
		editorCalled = true
		return nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("config edit exit=0; want non-zero when the config is unreadable")
	}
	if editorCalled {
		t.Errorf("editor launched despite an unreadable config; want abort before editing")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config path was removed/altered on an unreadable-read abort: %v", err)
	}
}

// CE5: rollback restores the pre-edit permission mode, not a hard-coded 0o600.
// The edit hook recreates the file with a different mode (as a rename-based
// editor like vim would); rollback must put both the bytes AND the original
// mode back so an edit attempt cannot silently tighten/loosen the config's
// permissions.
func TestConfigEdit_RollbackPreservesOriginalMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# keep me\n[core]\nmax_parallel = 2\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	withEditHook(t, func(p string) error {
		if err := os.WriteFile(p, []byte("[core]\nmax_parallel = 0\n"), 0o600); err != nil {
			return err
		}
		// Simulate an editor that recreated the file under a different mode.
		return os.Chmod(p, 0o600)
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
		t.Errorf("content not rolled back\n--- got ---\n%s\n--- want ---\n%s", got, original)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("rolled-back mode = %o; want 0644 (original mode preserved)", perm)
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
