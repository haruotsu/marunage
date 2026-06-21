package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadMissingFile mirrors the spec contract: a fresh user with no
// config file gets the documented defaults rather than an error. Downstream
// `marunage init` is the one that materialises the file on disk.
func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(missing) = %v; want nil", err)
	}
	if c.Core.MaxParallel != 3 {
		t.Errorf("Load(missing).Core.MaxParallel = %d; want default 3", c.Core.MaxParallel)
	}
}

func TestLoadParsesTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[core]
db_path = "/tmp/tasks.db"
max_parallel = 7
default_cwd = "~/works"
log_level = "debug"

[secrets]
backend = "age"

[discovery]
interval = "5m"
sources_enabled = ["gmail", "github"]

[execution]
permission_mode = "default"
claude_command = "claude"
startup_timeout = 90
prompt_skill = "marunage-execute"
allowed_cwd_prefixes = ["~/works"]
auto_accept_tools = ["Read"]
on_unknown_permission = "fail"
human_wait_timeout = "10m"

[reflection]
enabled = true
sample_rate = 0.25
tagged_only = ["important"]

[journal]
enabled = false
interval = "1h"
sources = ["git"]

[notify]
on_complete = false
on_failure = true
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}

	if c.Core.MaxParallel != 7 {
		t.Errorf("Core.MaxParallel = %d; want 7", c.Core.MaxParallel)
	}
	if c.Secrets.Backend != "age" {
		t.Errorf("Secrets.Backend = %q; want %q", c.Secrets.Backend, "age")
	}
	if c.Execution.PermissionMode != "default" {
		t.Errorf("Execution.PermissionMode = %q; want %q", c.Execution.PermissionMode, "default")
	}
	if c.Reflection.SampleRate != 0.25 {
		t.Errorf("Reflection.SampleRate = %v; want 0.25", c.Reflection.SampleRate)
	}
}

// TestLoadParsesManageSection pins that the redesign §6 [manage] block —
// including the inline-table [manage.verdicts] mapping and execution.executor
// — round-trips through the loader and passes validation.
func TestLoadParsesManageSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[execution]
permission_mode = "bypass"
claude_command = "claude"
startup_timeout = 60
on_unknown_permission = "escalate"
human_wait_timeout = "30m"
executor = "tmux"

[manage]
enabled = true
llm_scoring = false

[manage.rules]
block_if_deps_incomplete = true
escalate_if_body_empty = false
drop_if_cwd_violation = true
boost_if_due_within = "12h"

[manage.verdicts]
ready = { status = "pending", dispatchable = true }
needs_human = { status = "waiting_human", dispatchable = false, notify = true }
drop = { status = "skipped", dispatchable = false }
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if c.Execution.Executor != "tmux" {
		t.Errorf("Execution.Executor = %q; want tmux", c.Execution.Executor)
	}
	if c.Manage.LLMScoring {
		t.Errorf("Manage.LLMScoring = true; want false (explicit override)")
	}
	if c.Manage.Rules.BoostIfDueWithin != "12h" {
		t.Errorf("Manage.Rules.BoostIfDueWithin = %q; want 12h", c.Manage.Rules.BoostIfDueWithin)
	}
	if got := c.Manage.Verdicts["needs_human"]; got.Status != "waiting_human" || !got.Notify {
		t.Errorf("Manage.Verdicts[needs_human] = %+v; want status=waiting_human notify=true", got)
	}
	if got := c.Manage.Verdicts["ready"]; got.Status != "pending" || !got.Dispatchable {
		t.Errorf("Manage.Verdicts[ready] = %+v; want status=pending dispatchable=true", got)
	}
}

// TestLoadRejectsInvalidExecutor pins that a bad execution.executor is caught
// at load time, not deep inside the dispatcher.
func TestLoadRejectsInvalidExecutor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[execution]
permission_mode = "bypass"
claude_command = "claude"
startup_timeout = 60
on_unknown_permission = "escalate"
human_wait_timeout = "30m"
executor = "podman"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load = nil; want validation error")
	}
	if !strings.Contains(err.Error(), "execution.executor") {
		t.Errorf("Load err = %v; want mention of execution.executor", err)
	}
}

func TestLoadRejectsInvalidSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[core]
db_path = "/tmp/tasks.db"
max_parallel = 0
log_level = "info"

[secrets]
backend = "auto"

[discovery]
interval = "10m"

[execution]
permission_mode = "bypass"
claude_command = "claude"
startup_timeout = 60
on_unknown_permission = "escalate"
human_wait_timeout = "30m"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load = nil; want validation error")
	}
	if !strings.Contains(err.Error(), "core.max_parallel") {
		t.Errorf("Load err = %v; want mention of core.max_parallel", err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	c := Default()
	c.Core.MaxParallel = 5

	if err := Save(path, c, nil); err != nil {
		t.Fatalf("Save = %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if got.Core.MaxParallel != 5 {
		t.Errorf("round-trip MaxParallel = %d; want 5", got.Core.MaxParallel)
	}
	if got.Secrets.Backend != c.Secrets.Backend {
		t.Errorf("round-trip Secrets.Backend = %q; want %q", got.Secrets.Backend, c.Secrets.Backend)
	}
}

// TestSaveRoundTripPreservesSecretsBackend pins the round-trip contract
// for the [secrets] table introduced in PR-30: a non-default backend
// written via Save and read back via Load must come out the way it went
// in. Without this test, accidentally renaming the toml tag on
// SecretsConfig.Backend would silently fall back to "auto" on the next
// Load and override the user's explicit choice.
func TestSaveRoundTripPreservesSecretsBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	c := Default()
	c.Secrets.Backend = "keyring"

	if err := Save(path, c, nil); err != nil {
		t.Fatalf("Save = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if got.Secrets.Backend != "keyring" {
		t.Errorf("round-trip Secrets.Backend = %q; want %q", got.Secrets.Backend, "keyring")
	}
}

func TestSaveCreatesTimestampedBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	first := Default()
	first.Core.MaxParallel = 2
	if err := Save(path, first, nil); err != nil {
		t.Fatalf("first Save = %v", err)
	}

	second := Default()
	second.Core.MaxParallel = 9
	if err := Save(path, second, nil); err != nil {
		t.Fatalf("second Save = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var foundBackup bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.toml.bak.") {
			foundBackup = true
			break
		}
	}
	if !foundBackup {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("no config.toml.bak.<ts> backup created; entries=%v", names)
	}
}

func TestSaveRollsBackOnInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Seed the file with a valid baseline.
	if err := Save(path, Default(), nil); err != nil {
		t.Fatalf("seed Save = %v", err)
	}
	originalBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	bad := Default()
	bad.Core.MaxParallel = -1 // violates schema

	err = Save(path, bad, nil)
	if err == nil {
		t.Fatal("Save(invalid) = nil; want validation error")
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after rollback: %v", err)
	}
	if string(gotBytes) != string(originalBytes) {
		t.Errorf("file mutated after rejected Save\n--- before ---\n%s\n--- after ---\n%s", originalBytes, gotBytes)
	}
}

// TestSaveCallsAuditor wires the PR-04 audit interface contract: every
// successful Save emits exactly one event so audit.log will not silently
// miss configuration changes.
func TestSaveCallsAuditor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	rec := &recordingAuditor{}
	if err := Save(path, Default(), rec); err != nil {
		t.Fatalf("Save = %v", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("auditor events = %d; want 1", len(rec.events))
	}
	if rec.events[0].Action != "config.save" {
		t.Errorf("event.Action = %q; want %q", rec.events[0].Action, "config.save")
	}
	if rec.events[0].Path != path {
		t.Errorf("event.Path = %q; want %q", rec.events[0].Path, path)
	}
}

type recordingAuditor struct {
	events []AuditEvent
}

func (r *recordingAuditor) Record(e AuditEvent) {
	r.events = append(r.events, e)
}
