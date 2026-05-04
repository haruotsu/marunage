package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/haruotsu/marunage/internal/config"
)

// writeValidConfig marshals cfg to path, creating parent dirs as needed.
func writeValidConfig(t *testing.T, path string, cfg config.Config) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// --- config edit tests ---

func TestConfigEdit_SavesEditedContent(t *testing.T) {
	path := configPathFlag(t)

	// Seed with a known value.
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "set", "core.max_parallel", "3"}, &stdout, &stderr); code != 0 {
		t.Fatalf("seed set exit=%d; stderr=%q", code, stderr.String())
	}

	// Build a "next" config the EDITOR will copy into the tmp file.
	cfg := config.Default()
	cfg.Core.MaxParallel = 9
	nextPath := filepath.Join(t.TempDir(), "next.toml")
	writeValidConfig(t, nextPath, cfg)

	// EDITOR = "cp <nextPath>" → exec.Command("cp", nextPath, tmpFile)
	t.Setenv("EDITOR", "cp "+nextPath)

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config edit exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"--config", path, "config", "get", "core.max_parallel"}, &stdout, &stderr); code != 0 {
		t.Fatalf("config get exit=%d; stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "9" {
		t.Errorf("max_parallel = %q; want %q", got, "9")
	}
}

func TestConfigEdit_ValidationFailure_PreservesOriginal(t *testing.T) {
	path := configPathFlag(t)

	// Seed with a known value.
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "set", "core.max_parallel", "4"}, &stdout, &stderr); code != 0 {
		t.Fatalf("seed exit=%d; stderr=%q", code, stderr.String())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// EDITOR writes invalid TOML to the tmp file.
	badEditor := filepath.Join(t.TempDir(), "bad_editor.sh")
	if err := os.WriteFile(badEditor, []byte("#!/bin/sh\nprintf 'core.max_parallel = -1\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write bad_editor: %v", err)
	}
	t.Setenv("EDITOR", badEditor)

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("config edit with invalid TOML exit=0; want non-zero")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("config.toml was modified despite validation failure")
	}
}

func TestConfigEdit_ValidationFailure_RetainsTmpFile(t *testing.T) {
	path := configPathFlag(t)

	// Seed initial config.
	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "core.max_parallel", "2"}, &stdout, &stderr)

	// EDITOR writes a config with invalid permission_mode to the tmp file.
	badEditor := filepath.Join(t.TempDir(), "bad_editor.sh")
	badContent := `#!/bin/sh
cat > "$1" << 'TOMLEOF'
[core]
db_path = "~/.marunage/tasks.db"
max_parallel = 1
default_cwd = "~/works"
log_level = "info"

[execution]
permission_mode = "INVALID_MODE"
claude_command = "claude"
startup_timeout = 60
on_unknown_permission = "escalate"
human_wait_timeout = "30m"
reaper_stuck_threshold = "24h"

[secrets]
backend = "auto"

[discovery]
interval = "10m"
sources_enabled = ["markdown"]

[reflection]
enabled = false
sample_rate = 1.0

[journal]
enabled = true
interval = "30m"

[notify]
on_complete = true
on_failure = true

[web]
bind = "127.0.0.1"
port = 7777
TOMLEOF
`
	if err := os.WriteFile(badEditor, []byte(badContent), 0o755); err != nil {
		t.Fatalf("write bad_editor: %v", err)
	}
	t.Setenv("EDITOR", badEditor)

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("config edit invalid mode exit=0; want non-zero")
	}
	// Error message should mention the tmp file path so user can re-edit.
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "config.toml.edit.") {
		t.Errorf("error should mention tmp file path; got %q", combined)
	}
}

func TestConfigEdit_RecordsAuditLine(t *testing.T) {
	path := configPathFlag(t)

	// Seed.
	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "core.max_parallel", "3"}, &stdout, &stderr)

	// EDITOR = identity (copy current config back unchanged).
	cfg, _ := config.Load(path)
	samePath := filepath.Join(t.TempDir(), "same.toml")
	writeValidConfig(t, samePath, cfg)
	t.Setenv("EDITOR", "cp "+samePath)

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config edit exit=%d; stderr=%q", code, stderr.String())
	}

	lines := readCLIAuditLines(t, auditLogFor(path))
	var sawEdit bool
	for _, l := range lines {
		if l.Action == "config.edit" {
			sawEdit = true
			if l.Path != path {
				t.Errorf("config.edit Path = %q; want %q", l.Path, path)
			}
		}
	}
	if !sawEdit {
		t.Errorf("audit.log missing config.edit; lines=%+v", lines)
	}
}

func TestConfigEdit_AtomicWrite_NoTmpFileLeftOnSuccess(t *testing.T) {
	path := configPathFlag(t)

	// Seed.
	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "core.max_parallel", "3"}, &stdout, &stderr)

	cfg, _ := config.Load(path)
	samePath := filepath.Join(t.TempDir(), "same.toml")
	writeValidConfig(t, samePath, cfg)
	t.Setenv("EDITOR", "cp "+samePath)

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "edit"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config edit exit=%d; stderr=%q", code, stderr.String())
	}

	// No config.toml.edit.* tmp files should remain.
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "config.toml.edit.*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("tmp files left after successful edit: %v", matches)
	}
}

// --- config wizard tests ---

func TestConfigWizard_Section_Core_UpdatesMaxParallel(t *testing.T) {
	path := configPathFlag(t)

	// Seed.
	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "core.max_parallel", "3"}, &stdout, &stderr)

	// Provide: max_parallel=8, empty (keep log_level), empty (keep db_path).
	withStdinReader(t, strings.NewReader("8\n\n\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "core"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("wizard exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "core.max_parallel"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "8" {
		t.Errorf("max_parallel = %q; want %q", got, "8")
	}
}

func TestConfigWizard_EmptyEnterKeepsCurrentValue(t *testing.T) {
	path := configPathFlag(t)

	// Seed with log_level=warn.
	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "core.log_level", "warn"}, &stdout, &stderr)

	// For core section: empty, empty (keep log_level=warn), empty.
	withStdinReader(t, strings.NewReader("\n\n\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "core"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("wizard exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "core.log_level"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "warn" {
		t.Errorf("log_level = %q; want %q (kept)", got, "warn")
	}
}

func TestConfigWizard_InvalidValue_ReturnsError(t *testing.T) {
	path := configPathFlag(t)

	// Seed.
	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "core.max_parallel", "3"}, &stdout, &stderr)

	// Provide: max_parallel=-999 (invalid).
	withStdinReader(t, strings.NewReader("-999\n\n\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "core"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("wizard with invalid value exit=0; want non-zero")
	}

	// Config should be unchanged.
	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "core.max_parallel"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "3" {
		t.Errorf("max_parallel = %q after invalid wizard; want unchanged %q", got, "3")
	}
}

func TestConfigWizard_Section_Secrets(t *testing.T) {
	path := configPathFlag(t)

	// Seed.
	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "secrets.backend", "auto"}, &stdout, &stderr)

	// Provide: backend=keyring.
	withStdinReader(t, strings.NewReader("keyring\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "secrets"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("wizard secrets exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "secrets.backend"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "keyring" {
		t.Errorf("secrets.backend = %q; want %q", got, "keyring")
	}
}

func TestConfigWizard_Section_Discovery(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "discovery.interval", "5m"}, &stdout, &stderr)

	// Provide: interval=15m, empty (keep sources_enabled).
	withStdinReader(t, strings.NewReader("15m\n\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "discovery"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("wizard discovery exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "discovery.interval"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "15m" {
		t.Errorf("discovery.interval = %q; want %q", got, "15m")
	}
}

func TestConfigWizard_Section_Execution(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "execution.permission_mode", "bypass"}, &stdout, &stderr)

	// permission_mode=default, empty (claude_command derives), empty, empty.
	withStdinReader(t, strings.NewReader("default\n\n\n\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "execution"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("wizard execution exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "execution.permission_mode"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "default" {
		t.Errorf("execution.permission_mode = %q; want %q", got, "default")
	}
}

func TestConfigWizard_Section_Reflection(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "reflection.enabled", "false"}, &stdout, &stderr)

	// enabled=true, empty (keep sample_rate).
	withStdinReader(t, strings.NewReader("true\n\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "reflection"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("wizard reflection exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "reflection.enabled"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "true" {
		t.Errorf("reflection.enabled = %q; want %q", got, "true")
	}
}

func TestConfigWizard_UnknownSection_Fails(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "bogus"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("wizard --section bogus exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "bogus") {
		t.Errorf("stderr should mention unknown section; got %q", stderr.String())
	}
}

func TestConfigWizard_RecordsAuditLine(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "core.max_parallel", "3"}, &stdout, &stderr)

	withStdinReader(t, strings.NewReader("\n\n\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard", "--section", "core"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("wizard exit=%d; stderr=%q", code, stderr.String())
	}

	lines := readCLIAuditLines(t, auditLogFor(path))
	var sawWizard bool
	for _, l := range lines {
		if l.Action == "config.wizard" {
			sawWizard = true
			if l.Path != path {
				t.Errorf("config.wizard Path = %q; want %q", l.Path, path)
			}
		}
	}
	if !sawWizard {
		t.Errorf("audit.log missing config.wizard; lines=%+v", lines)
	}
}

func TestConfigWizard_NoSection_RunsAllSections(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	Execute([]string{"--config", path, "config", "set", "core.max_parallel", "3"}, &stdout, &stderr)

	// Prompts per section: core(3), secrets(1), discovery(2), execution(4), reflection(2) = 12 lines.
	// core: max_parallel=5, log_level=keep, db_path=keep
	// secrets: backend=file
	// discovery: interval=keep, sources_enabled=keep
	// execution: permission_mode=keep, claude_command=keep, on_unknown_permission=keep, human_wait_timeout=keep
	// reflection: enabled=keep, sample_rate=keep
	withStdinReader(t, strings.NewReader("5\n\n\nfile\n\n\n\n\n\n\n\n\n"))

	stdout.Reset()
	stderr.Reset()
	code := Execute([]string{"--config", path, "config", "wizard"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("wizard (all sections) exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "core.max_parallel"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "5" {
		t.Errorf("max_parallel = %q; want %q", got, "5")
	}

	stdout.Reset()
	stderr.Reset()
	Execute([]string{"--config", path, "config", "get", "secrets.backend"}, &stdout, &stderr)
	if got := strings.TrimSpace(stdout.String()); got != "file" {
		t.Errorf("secrets.backend = %q; want %q", got, "file")
	}
}
