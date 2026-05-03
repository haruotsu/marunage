package initialize

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
)

// TestRun_FirstRun_CreatesLayout pins the documented contract: starting from
// an empty home, init must create ~/.marunage, the required subdirs, and a
// default config.toml. Permissions matter for the audit / secret content
// these directories will eventually hold, so we pin them too.
func TestRun_FirstRun_CreatesLayout(t *testing.T) {
	home := t.TempDir()

	res, err := Run(Options{Home: home})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !res.ConfigCreated {
		t.Errorf("ConfigCreated = false; want true on first run")
	}

	wantDirs := []string{
		filepath.Join(home, ".marunage"),
		filepath.Join(home, ".marunage", "logs"),
		filepath.Join(home, ".marunage", "sources"),
	}
	for _, d := range wantDirs {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("dir %s missing: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s perm = %o; want 0700", d, perm)
		}
	}

	cfgPath := filepath.Join(home, ".marunage", "config.toml")
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("config.toml not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.toml perm = %o; want 0600", perm)
	}

	if res.ConfigPath != cfgPath {
		t.Errorf("ConfigPath = %q; want %q", res.ConfigPath, cfgPath)
	}
}

// TestRun_SecondRun_LeavesUserConfigUntouched pins the idempotency
// invariant. After the user has edited config.toml, a second `marunage
// init` must not silently revert their changes — the OSS first-run UX
// would lose data on every accidental re-run otherwise.
func TestRun_SecondRun_LeavesUserConfigUntouched(t *testing.T) {
	home := t.TempDir()

	if _, err := Run(Options{Home: home}); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	cfgPath := filepath.Join(home, ".marunage", "config.toml")
	userEdit := []byte("# user-modified marker\n[core]\nmax_parallel = 7\n")
	if err := os.WriteFile(cfgPath, userEdit, 0o600); err != nil {
		t.Fatalf("seed user edit: %v", err)
	}

	res, err := Run(Options{Home: home})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.ConfigCreated {
		t.Errorf("ConfigCreated = true on second run; want false")
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after second Run: %v", err)
	}
	if string(got) != string(userEdit) {
		t.Errorf("user-edited config was overwritten\nbefore=%q\nafter =%q", userEdit, got)
	}
}

// TestRun_AddsMissingSubdir_WithoutTouchingConfig covers the realistic
// recovery path: a user has a hand-rolled config.toml but no logs/. init
// must add the missing subdir without rewriting the config file.
func TestRun_AddsMissingSubdir_WithoutTouchingConfig(t *testing.T) {
	home := t.TempDir()

	if err := os.MkdirAll(filepath.Join(home, ".marunage"), 0o700); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	cfgPath := filepath.Join(home, ".marunage", "config.toml")
	userEdit := []byte("# user-modified marker\n[core]\nmax_parallel = 9\n")
	if err := os.WriteFile(cfgPath, userEdit, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	res, err := Run(Options{Home: home})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ConfigCreated {
		t.Errorf("ConfigCreated = true; want false (config already existed)")
	}

	for _, sub := range []string{"logs", "sources"} {
		if _, err := os.Stat(filepath.Join(home, ".marunage", sub)); err != nil {
			t.Errorf("expected %s to be created: %v", sub, err)
		}
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != string(userEdit) {
		t.Errorf("config was rewritten\nbefore=%q\nafter =%q", userEdit, got)
	}
}

// TestRun_NonDefaultMode_RecordedInConfig covers the user picking a non-
// default permission mode at init time. The choice must round-trip through
// config.toml (so subsequent runs honour it) and the derived claude_command
// must match the documented mapping in docs/requirement.md.
func TestRun_NonDefaultMode_RecordedInConfig(t *testing.T) {
	home := t.TempDir()

	if _, err := Run(Options{Home: home, Mode: "default"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cfg, err := config.Load(filepath.Join(home, ".marunage", "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Execution.PermissionMode != "default" {
		t.Errorf("permission_mode = %q; want %q", cfg.Execution.PermissionMode, "default")
	}
	if cfg.Execution.ClaudeCommand != "claude" {
		t.Errorf("claude_command = %q; want %q (auto-derived)", cfg.Execution.ClaudeCommand, "claude")
	}
}

// TestRun_CustomMode_PicksValidPlaceholder pins the spec rule that "custom"
// is the user-edited escape hatch. We must produce a config.toml that:
//   - records permission_mode="custom" so the user's choice round-trips,
//   - keeps a non-empty claude_command so config.Validate is satisfied
//     (an empty value is rejected at load time, which would brick the
//     freshly-initialised marunage home until the user opened the file).
//
// The placeholder is the documented "default" mode's command — safe enough
// to launch on any machine, and the obvious next thing the user replaces.
func TestRun_CustomMode_PicksValidPlaceholder(t *testing.T) {
	home := t.TempDir()

	if _, err := Run(Options{Home: home, Mode: "custom"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cfg, err := config.Load(filepath.Join(home, ".marunage", "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Execution.PermissionMode != "custom" {
		t.Errorf("permission_mode = %q; want %q", cfg.Execution.PermissionMode, "custom")
	}
	if cfg.Execution.ClaudeCommand == "" {
		t.Errorf("claude_command = %q; want a non-empty placeholder so config validates", cfg.Execution.ClaudeCommand)
	}
}

// TestRun_InvalidMode_ReturnsTypedError ensures malformed user input fails
// loud at the boundary rather than producing a config.toml with a value
// the validator would later reject. ErrInvalidMode is the typed sentinel
// callers can errors.Is against.
func TestRun_InvalidMode_ReturnsTypedError(t *testing.T) {
	home := t.TempDir()

	_, err := Run(Options{Home: home, Mode: "yolo"})
	if err == nil {
		t.Fatalf("Run with invalid mode: err = nil; want non-nil")
	}
	if !errors.Is(err, ErrInvalidMode) {
		t.Errorf("err = %v; want errors.Is(_, ErrInvalidMode)", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".marunage", "config.toml")); statErr == nil {
		t.Errorf("config.toml created despite invalid mode")
	}
}

// recordingAuditor captures every AuditEvent in order so tests can assert
// exact invocation sequences without coupling to a real logging.AuditLog
// (which would require us to spin up a real on-disk file just to read it
// back inside the same test).
type recordingAuditor struct {
	events []config.AuditEvent
}

func (r *recordingAuditor) Record(e config.AuditEvent) {
	r.events = append(r.events, e)
}

// TestRun_AuditCreate pins the "No silent execution" invariant for first-
// run init: a fresh config.toml is the kind of mutation operators need to
// see in audit.log. The action label must be init.create so a future
// auditor querying the log can distinguish creation from later edits.
func TestRun_AuditCreate(t *testing.T) {
	home := t.TempDir()
	rec := &recordingAuditor{}

	res, err := Run(Options{Home: home, Auditor: rec})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var got config.AuditEvent
	for _, e := range rec.events {
		if e.Action == "init.create" {
			got = e
			break
		}
	}
	if got.Action != "init.create" {
		t.Fatalf("no init.create event recorded; events=%+v", rec.events)
	}
	if got.Path != res.ConfigPath {
		t.Errorf("init.create path = %q; want %q", got.Path, res.ConfigPath)
	}
}

// TestRun_AuditSkip pins that re-runs leave a different audit trace
// (init.skip) rather than silently doing nothing or fabricating an
// init.create line for an unchanged file. Operators should see "init was
// invoked but didn't need to do anything" rather than "no record at all"
// — the latter is indistinguishable from "init was never invoked".
func TestRun_AuditSkip(t *testing.T) {
	home := t.TempDir()

	if _, err := Run(Options{Home: home}); err != nil {
		t.Fatalf("seed Run: %v", err)
	}

	rec := &recordingAuditor{}
	if _, err := Run(Options{Home: home, Auditor: rec}); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	var sawCreate, sawSkip bool
	for _, e := range rec.events {
		switch e.Action {
		case "init.create":
			sawCreate = true
		case "init.skip":
			sawSkip = true
		}
	}
	if sawCreate {
		t.Errorf("second Run emitted init.create for an unchanged file; events=%+v", rec.events)
	}
	if !sawSkip {
		t.Errorf("second Run did not emit init.skip; events=%+v", rec.events)
	}
}
