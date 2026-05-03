package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
)

// configPathInsideHome lays out a tempdir as if it were a fresh home:
// returns <home>/.marunage/config.toml without actually creating any of
// the parent directories. init is responsible for creating them.
func configPathInsideHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	return filepath.Join(home, ".marunage", "config.toml")
}

// TestInit_NonInteractive_DefaultsToBypass pins the --non-interactive
// promise: the command runs end-to-end without stdin and produces a
// config.toml the rest of marunage can load.
func TestInit_NonInteractive_DefaultsToBypass(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init", "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init --non-interactive exit=%d; stderr=%q", code, stderr.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load %s: %v", cfgPath, err)
	}
	if cfg.Execution.PermissionMode != "bypass" {
		t.Errorf("permission_mode = %q; want %q", cfg.Execution.PermissionMode, "bypass")
	}
}

// TestInit_NonInteractive_WithMode pins the --mode flag's role as the
// non-interactive equivalent of the prompt.
func TestInit_NonInteractive_WithMode(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init", "--non-interactive", "--mode", "default"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init --mode default exit=%d; stderr=%q", code, stderr.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Execution.PermissionMode != "default" {
		t.Errorf("permission_mode = %q; want %q", cfg.Execution.PermissionMode, "default")
	}
	if cfg.Execution.ClaudeCommand != "claude" {
		t.Errorf("claude_command = %q; want %q", cfg.Execution.ClaudeCommand, "claude")
	}
}

// TestInit_NonInteractive_RejectsInvalidMode pins the rollback invariant:
// invalid input must not produce any on-disk artefact.
func TestInit_NonInteractive_RejectsInvalidMode(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init", "--non-interactive", "--mode", "yolo"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("init invalid mode exit=0; want non-zero; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "yolo") {
		t.Errorf("stderr missing rejected value 'yolo'; got %q", stderr.String())
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Errorf("config.toml was created despite invalid mode")
	}
}

// TestInit_Interactive_DefaultEnter selects option 1 (bypass) by hitting
// Enter — the documented default the prompt highlights with [1].
func TestInit_Interactive_DefaultEnter(t *testing.T) {
	cfgPath := configPathInsideHome(t)
	withStdinReader(t, strings.NewReader("\n"))

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init exit=%d; stderr=%q", code, stderr.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Execution.PermissionMode != "bypass" {
		t.Errorf("permission_mode = %q; want %q", cfg.Execution.PermissionMode, "bypass")
	}
}

// TestInit_Interactive_NumericChoice pins the documented numeric mapping
// from docs/requirement.md (1=bypass, 2=default, ...). "2" must select
// "default".
func TestInit_Interactive_NumericChoice(t *testing.T) {
	cfgPath := configPathInsideHome(t)
	withStdinReader(t, strings.NewReader("2\n"))

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init exit=%d; stderr=%q", code, stderr.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Execution.PermissionMode != "default" {
		t.Errorf("permission_mode = %q; want %q", cfg.Execution.PermissionMode, "default")
	}
}

// TestInit_Interactive_RepromptsOnInvalidChoice covers the UX detail that
// a typo doesn't dump the user back to the shell with no config.
func TestInit_Interactive_RepromptsOnInvalidChoice(t *testing.T) {
	cfgPath := configPathInsideHome(t)
	withStdinReader(t, strings.NewReader("9\n2\n"))

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init exit=%d; stderr=%q", code, stderr.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Execution.PermissionMode != "default" {
		t.Errorf("permission_mode = %q; want %q (after reprompt)", cfg.Execution.PermissionMode, "default")
	}
}

// TestInit_GuidanceMentionsDoctorAndSetup pins the "next steps" surface:
// a freshly initialised user must see what to run next, in order
// (doctor → setup --skills).
func TestInit_GuidanceMentionsDoctorAndSetup(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init", "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init exit=%d; stderr=%q", code, stderr.String())
	}

	out := stdout.String()
	doctorIdx := strings.Index(out, "marunage doctor")
	setupIdx := strings.Index(out, "marunage setup --skills")
	if doctorIdx < 0 {
		t.Errorf("guidance missing 'marunage doctor'; stdout=%q", out)
	}
	if setupIdx < 0 {
		t.Errorf("guidance missing 'marunage setup --skills'; stdout=%q", out)
	}
	if doctorIdx >= 0 && setupIdx >= 0 && doctorIdx > setupIdx {
		t.Errorf("guidance lists doctor after setup; want doctor first\nstdout=%q", out)
	}
}

// TestInit_SecondRun_IsIdempotent pins the user-visible idempotency:
// re-running init exits 0 and signals "already initialized" so users can
// safely include it in setup scripts.
func TestInit_SecondRun_IsIdempotent(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", cfgPath, "init", "--non-interactive"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first init exit=%d; stderr=%q", code, stderr.String())
	}
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after first init: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", cfgPath, "init", "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("second init exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already initialized") {
		t.Errorf("stdout missing 'already initialized' on re-run; got %q", stdout.String())
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after second init: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("config.toml mutated by re-run\nbefore=%q\nafter =%q", before, after)
	}
}

// TestInit_HelpDescribesCommand exercises the --help path so cobra's help
// generator stays wired and the user-facing description mentions the
// distinguishing artefacts (~/.marunage and "permission mode").
func TestInit_HelpDescribesCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"init", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init --help exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"~/.marunage", "permission mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("init --help output missing %q\nfull output:\n%s", want, out)
		}
	}
}

// TestInit_WritesAuditLine pins the "No silent execution" invariant for
// init through the real CLI wiring: the file-backed AuditLog under
// ~/.marunage/logs/audit.log must contain exactly one init.create entry
// after a successful first run, with no spurious init.skip alongside it
// (the two events are mutually exclusive per invocation).
func TestInit_WritesAuditLine(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", cfgPath, "init", "--non-interactive"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit=%d; stderr=%q", code, stderr.String())
	}

	auditPath := auditLogFor(cfgPath)
	lines := readCLIAuditLines(t, auditPath)

	var createCount, skipCount int
	for _, l := range lines {
		switch l.Action {
		case "init.create":
			createCount++
			if l.Path != cfgPath {
				t.Errorf("init.create path = %q; want %q", l.Path, cfgPath)
			}
		case "init.skip":
			skipCount++
		}
	}
	if createCount != 1 {
		t.Errorf("init.create count = %d; want 1; lines=%+v", createCount, lines)
	}
	if skipCount != 0 {
		t.Errorf("init.skip count = %d; want 0 on first run; lines=%+v", skipCount, lines)
	}
}

// TestInit_CustomMode_WarnsUserToEdit pins the safety nudge from the
// review: when a user picks "custom", the placeholder claude_command is
// `claude` (default-mode equivalent) — but their effective behaviour is
// not what the "custom" label implies. The CLI must explicitly tell
// them to edit claude_command before running marunage, otherwise the
// custom label silently lies.
func TestInit_CustomMode_WarnsUserToEdit(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init", "--non-interactive", "--mode", "custom"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init --mode custom exit=%d; stderr=%q", code, stderr.String())
	}

	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "claude_command") {
		t.Errorf("custom-mode notice missing 'claude_command' guidance; output=%q", combined)
	}
}

// TestInit_InvalidMode_LeavesAuditUntouched mirrors the rationale in
// internal/cli/config.go (open audit only after validation): a typo'd
// --mode must not leave a stale logs/ tree on disk. The previous
// implementation opened logging.NewAuditLog before validating --mode,
// which created ~/.marunage/logs/audit.log even though no config was
// written. This test pins the "no side effects on rejected input"
// invariant for init.
func TestInit_InvalidMode_LeavesAuditUntouched(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", cfgPath, "init", "--non-interactive", "--mode", "yolo"}, &stdout, &stderr); code == 0 {
		t.Fatalf("init invalid mode exit=0; want non-zero")
	}

	auditPath := auditLogFor(cfgPath)
	if _, err := os.Stat(auditPath); !os.IsNotExist(err) {
		t.Errorf("audit.log should not exist after rejected --mode; stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(auditPath)); !os.IsNotExist(err) {
		t.Errorf("logs/ dir should not exist after rejected --mode; stat err=%v", err)
	}
}

// TestInit_RejectsConfigOutsideMarunageConvention pins the safety
// invariant flagged by the security review: when --config does not point
// inside a `.marunage/` directory, the previous implementation silently
// fell back to os.UserHomeDir() and provisioned the *real* ~/.marunage
// — a destructive surprise for users testing init in throwaway dirs or
// pointing at /etc-style paths. The fix: refuse to proceed and tell the
// user the convention.
func TestInit_RejectsConfigOutsideMarunageConvention(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "marunage.toml") // NOT inside .marunage/

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "init", "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("init unconventional --config exit=0; want non-zero")
	}
	// stderr should mention the convention so the user knows what to fix.
	if !strings.Contains(stderr.String(), ".marunage") {
		t.Errorf("stderr should mention .marunage convention; got %q", stderr.String())
	}
	// No layout artefacts under tmp.
	for _, sub := range []string{"logs", "sources", "marunage.toml"} {
		if _, err := os.Stat(filepath.Join(tmp, sub)); err == nil {
			t.Errorf("init touched %s despite rejection", sub)
		}
	}
}

// TestInit_NoLongerLeafStub ensures the migration from "stub" to real
// implementation took effect, mirroring the doctor test pattern.
func TestInit_NoLongerLeafStub(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	_ = Execute([]string{"--config", cfgPath, "init", "--non-interactive"}, &stdout, &stderr)
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "not yet implemented") {
		t.Errorf("init still routed to the not-yet-implemented stub\noutput:\n%s", combined)
	}
}
