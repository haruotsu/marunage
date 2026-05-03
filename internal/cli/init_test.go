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
// after a successful first run.
func TestInit_WritesAuditLine(t *testing.T) {
	cfgPath := configPathInsideHome(t)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", cfgPath, "init", "--non-interactive"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit=%d; stderr=%q", code, stderr.String())
	}

	auditPath := auditLogFor(cfgPath)
	lines := readCLIAuditLines(t, auditPath)

	var sawCreate bool
	for _, l := range lines {
		if l.Action == "init.create" {
			sawCreate = true
			if l.Path != cfgPath {
				t.Errorf("init.create path = %q; want %q", l.Path, cfgPath)
			}
		}
	}
	if !sawCreate {
		t.Errorf("audit.log missing init.create line; lines=%+v", lines)
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
