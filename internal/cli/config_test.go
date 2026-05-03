package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configPathFlag points the binary at a tempfile so the test does not touch
// the developer's real ~/.marunage/config.toml.
func configPathFlag(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.toml")
}

func TestConfigGet_PrintsDefaultValue(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "get", "execution.permission_mode"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config get exit=%d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "bypass" {
		t.Errorf("config get stdout = %q; want %q", got, "bypass")
	}
}

func TestConfigGet_UnknownKeyFails(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "get", "core.bogus"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("config get unknown exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "core.bogus") {
		t.Errorf("config get unknown stderr=%q; want mention of key", stderr.String())
	}
}

func TestConfigSet_WritesAndPersists(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "set", "core.max_parallel", "5"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config set exit=%d; stderr=%q", code, stderr.String())
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"--config", path, "config", "get", "core.max_parallel"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config get exit=%d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "5" {
		t.Errorf("config get after set = %q; want %q", got, "5")
	}
}

func TestConfigSet_RejectsInvalidValue(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "set", "execution.permission_mode", "yolo"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("config set invalid exit=0; want non-zero")
	}

	if _, err := os.Stat(path); err == nil {
		t.Errorf("config file was created despite validation failure: %s", path)
	}
}

// TestConfigSet_InvalidLeavesExistingFileUntouched pins the rollback invariant
// for the realistic case: a valid config already exists on disk, the user
// fat-fingers a key, and the previously-saved file must come out byte-for-byte
// unchanged. Without this assertion the regression where Save half-writes a
// new file before validation would slip past TestConfigSet_RejectsInvalidValue
// (which only checks that the file was not *created*).
func TestConfigSet_InvalidLeavesExistingFileUntouched(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "set", "core.max_parallel", "7"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("seed config set exit=%d; stderr=%q", code, stderr.String())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"--config", path, "config", "set", "execution.permission_mode", "yolo"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("invalid config set exit=0; want non-zero")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after invalid set: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("config file mutated by invalid set\nbefore=%q\nafter =%q", before, after)
	}
}

// TestConfigSet_PermissionModeDerivesClaudeCommand exercises the spec rule
// end-to-end through the CLI: setting the mode rewrites claude_command on
// disk, which downstream `config get` then reflects.
func TestConfigSet_PermissionModeDerivesClaudeCommand(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "set", "execution.permission_mode", "default"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config set permission_mode exit=%d; stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"--config", path, "config", "get", "execution.claude_command"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config get claude_command exit=%d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "claude" {
		t.Errorf("execution.claude_command = %q; want auto-derived %q", got, "claude")
	}
}

func TestConfigGet_RequiresKey(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "get"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("config get (no key) exit=0; want non-zero")
	}
}

func TestConfigSet_RequiresKeyAndValue(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "set", "core.max_parallel"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("config set (missing value) exit=0; want non-zero")
	}
}
