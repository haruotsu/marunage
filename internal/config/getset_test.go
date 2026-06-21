package config

import (
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	c := Default()
	c.Core.MaxParallel = 7
	c.Execution.PermissionMode = "default"
	c.Execution.AutoAcceptTools = []string{"Read", "Glob"}

	cases := []struct {
		key  string
		want string
	}{
		{"core.max_parallel", "7"},
		{"core.log_level", "info"},
		{"core.db_path", "~/.marunage/tasks.db"},
		{"secrets.backend", "auto"},
		{"execution.permission_mode", "default"},
		{"execution.startup_timeout", "60"},
		{"execution.auto_accept_tools", "Read,Glob"},
		{"reflection.enabled", "false"},
		{"journal.enabled", "true"},
		{"discovery.slack.include_dm", "true"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got, err := Get(c, tc.key)
			if err != nil {
				t.Fatalf("Get(%q) = %v", tc.key, err)
			}
			if got != tc.want {
				t.Errorf("Get(%q) = %q; want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestGetUnknownKey(t *testing.T) {
	_, err := Get(Default(), "core.nonexistent")
	if err == nil {
		t.Fatal("Get unknown key = nil; want error")
	}
	if !strings.Contains(err.Error(), "core.nonexistent") {
		t.Errorf("Get err = %v; want mention of the key", err)
	}
}

// TestGetSetMapFieldDirectsToEdit pins the contract that map-typed config
// keys (e.g. execution.lock_keys) cannot be edited via Get/Set and that the
// error tells the user to reach for `marunage config edit` instead of leaking
// the reflect kind name.
func TestGetSetMapFieldDirectsToEdit(t *testing.T) {
	c := Default()

	_, gerr := Get(c, "execution.lock_keys")
	if gerr == nil {
		t.Fatal("Get(execution.lock_keys) = nil; want error")
	}
	if !strings.Contains(gerr.Error(), "execution.lock_keys") {
		t.Errorf("Get err = %v; want mention of the key", gerr)
	}
	if !strings.Contains(gerr.Error(), "marunage config edit") {
		t.Errorf("Get err = %v; want guidance to use 'marunage config edit'", gerr)
	}

	serr := Set(&c, "execution.lock_keys", "^repo:.*=git-repo")
	if serr == nil {
		t.Fatal("Set(execution.lock_keys) = nil; want error")
	}
	if !strings.Contains(serr.Error(), "execution.lock_keys") {
		t.Errorf("Set err = %v; want mention of the key", serr)
	}
	if !strings.Contains(serr.Error(), "marunage config edit") {
		t.Errorf("Set err = %v; want guidance to use 'marunage config edit'", serr)
	}

	// manage.verdicts is also a map; the same edit-instead-of-set guidance
	// must apply so the inline-table mapping is only changed via the editor.
	if _, err := Get(c, "manage.verdicts"); err == nil || !strings.Contains(err.Error(), "marunage config edit") {
		t.Errorf("Get(manage.verdicts) = %v; want edit guidance", err)
	}
	if err := Set(&c, "manage.verdicts", "ready=pending"); err == nil || !strings.Contains(err.Error(), "marunage config edit") {
		t.Errorf("Set(manage.verdicts) = %v; want edit guidance", err)
	}
}

func TestSet(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
		check func(*testing.T, Config)
	}{
		{
			name:  "int",
			key:   "core.max_parallel",
			value: "5",
			check: func(t *testing.T, c Config) {
				if c.Core.MaxParallel != 5 {
					t.Errorf("Core.MaxParallel = %d; want 5", c.Core.MaxParallel)
				}
			},
		},
		{
			name:  "string",
			key:   "core.log_level",
			value: "debug",
			check: func(t *testing.T, c Config) {
				if c.Core.LogLevel != "debug" {
					t.Errorf("Core.LogLevel = %q; want %q", c.Core.LogLevel, "debug")
				}
			},
		},
		{
			name:  "bool",
			key:   "reflection.enabled",
			value: "true",
			check: func(t *testing.T, c Config) {
				if !c.Reflection.Enabled {
					t.Error("Reflection.Enabled = false; want true")
				}
			},
		},
		{
			name:  "float",
			key:   "reflection.sample_rate",
			value: "0.5",
			check: func(t *testing.T, c Config) {
				if c.Reflection.SampleRate != 0.5 {
					t.Errorf("Reflection.SampleRate = %v; want 0.5", c.Reflection.SampleRate)
				}
			},
		},
		{
			name:  "string slice (CSV)",
			key:   "execution.auto_accept_tools",
			value: "Read,Bash(git status:*)",
			check: func(t *testing.T, c Config) {
				want := []string{"Read", "Bash(git status:*)"}
				if !equalStringSlices(c.Execution.AutoAcceptTools, want) {
					t.Errorf("AutoAcceptTools = %v; want %v", c.Execution.AutoAcceptTools, want)
				}
			},
		},
		{
			name:  "string slice (JSON array)",
			key:   "execution.auto_accept_tools",
			value: `["Read","Bash(git status:*)","Bash(git diff:*)"]`,
			check: func(t *testing.T, c Config) {
				want := []string{"Read", "Bash(git status:*)", "Bash(git diff:*)"}
				if !equalStringSlices(c.Execution.AutoAcceptTools, want) {
					t.Errorf("AutoAcceptTools = %v; want %v", c.Execution.AutoAcceptTools, want)
				}
			},
		},
		{
			name:  "string slice (JSON array with spaces)",
			key:   "execution.auto_accept_tools",
			value: `["Read", "Grep", "Glob"]`,
			check: func(t *testing.T, c Config) {
				want := []string{"Read", "Grep", "Glob"}
				if !equalStringSlices(c.Execution.AutoAcceptTools, want) {
					t.Errorf("AutoAcceptTools = %v; want %v", c.Execution.AutoAcceptTools, want)
				}
			},
		},
		{
			name:  "string slice (empty JSON array)",
			key:   "execution.auto_accept_tools",
			value: `[]`,
			check: func(t *testing.T, c Config) {
				if len(c.Execution.AutoAcceptTools) != 0 {
					t.Errorf("AutoAcceptTools = %v; want empty", c.Execution.AutoAcceptTools)
				}
			},
		},
		{
			name:  "execution.executor",
			key:   "execution.executor",
			value: "local",
			check: func(t *testing.T, c Config) {
				if c.Execution.Executor != "local" {
					t.Errorf("Execution.Executor = %q; want local", c.Execution.Executor)
				}
			},
		},
		{
			name:  "manage.enabled",
			key:   "manage.enabled",
			value: "false",
			check: func(t *testing.T, c Config) {
				if c.Manage.Enabled {
					t.Error("Manage.Enabled = true; want false")
				}
			},
		},
		{
			name:  "manage.rules.boost_if_due_within",
			key:   "manage.rules.boost_if_due_within",
			value: "6h",
			check: func(t *testing.T, c Config) {
				if c.Manage.Rules.BoostIfDueWithin != "6h" {
					t.Errorf("Manage.Rules.BoostIfDueWithin = %q; want 6h", c.Manage.Rules.BoostIfDueWithin)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			if err := Set(&c, tc.key, tc.value); err != nil {
				t.Fatalf("Set(%q, %q) = %v", tc.key, tc.value, err)
			}
			tc.check(t, c)
		})
	}
}

// TestSetPermissionModeDerivesClaudeCommand: changing permission_mode to a
// non-custom value rewrites claude_command so the two stay consistent. This
// matches the spec note "permission_mode から自動生成、custom 時のみ手書き".
//
// Covers all four canonical modes through Set so the CLI path
// (`marunage config set execution.permission_mode <mode>`) cannot regress for
// any mode without a test failing — `bypass` is the default, but a user who
// switches off it and back must see the bypass command resurface verbatim.
func TestSetPermissionModeDerivesClaudeCommand(t *testing.T) {
	cases := []struct {
		mode        string
		wantCommand string
	}{
		{"bypass", "claude --dangerously-skip-permissions"},
		{"default", "claude"},
		{"acceptEdits", "claude --permission-mode acceptEdits"},
		{"plan", "claude --permission-mode plan"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			c := Default()
			// Move off the default so a no-op assignment cannot mask a bug.
			c.Execution.ClaudeCommand = "claude --stale-marker"
			if err := Set(&c, "execution.permission_mode", tc.mode); err != nil {
				t.Fatalf("Set permission_mode=%q = %v", tc.mode, err)
			}
			if c.Execution.PermissionMode != tc.mode {
				t.Errorf("PermissionMode = %q; want %q", c.Execution.PermissionMode, tc.mode)
			}
			if c.Execution.ClaudeCommand != tc.wantCommand {
				t.Errorf("ClaudeCommand = %q; want auto-derived %q", c.Execution.ClaudeCommand, tc.wantCommand)
			}
		})
	}
}

// TestSetPermissionModeCustomKeepsCommand: switching to "custom" must NOT
// overwrite the existing claude_command, otherwise users lose their hand-
// written command on every mode flip. Drives the contract through Set so the
// CLI path (where users only have Set/Get) is exercised end-to-end.
func TestSetPermissionModeCustomKeepsCommand(t *testing.T) {
	c := Default()
	if err := Set(&c, "execution.claude_command", "claude --my-flag"); err != nil {
		t.Fatalf("Set claude_command = %v", err)
	}
	if err := Set(&c, "execution.permission_mode", "custom"); err != nil {
		t.Fatalf("Set permission_mode custom = %v", err)
	}
	if c.Execution.ClaudeCommand != "claude --my-flag" {
		t.Errorf("ClaudeCommand = %q; want preserved %q", c.Execution.ClaudeCommand, "claude --my-flag")
	}
}

// TestSetPermissionModeCustomRejectsEmptyCommand: flipping into custom mode
// without a user-supplied claude_command must fail validation, and the error
// must name execution.claude_command so the user knows which key to fix.
func TestSetPermissionModeCustomRejectsEmptyCommand(t *testing.T) {
	c := Default()
	if err := Set(&c, "execution.claude_command", ""); err != nil {
		t.Fatalf("Set claude_command \"\" under bypass = %v; want nil", err)
	}
	err := Set(&c, "execution.permission_mode", "custom")
	if err == nil {
		t.Fatal("Set permission_mode=custom with empty command = nil; want error")
	}
	if !strings.Contains(err.Error(), "execution.claude_command") {
		t.Errorf("Set err = %v; want mention of execution.claude_command", err)
	}
}

func TestSetRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"unknown key", "core.bogus", "1"},
		{"int parse error", "core.max_parallel", "many"},
		{"int rejects negative via validate", "core.max_parallel", "0"},
		{"bool parse error", "reflection.enabled", "kinda"},
		{"permission_mode out of range", "execution.permission_mode", "yolo"},
		{"log_level out of range", "core.log_level", "trace"},
		{"string slice invalid JSON syntax", "execution.auto_accept_tools", "[invalid"},
		{"string slice non-string JSON array", "execution.auto_accept_tools", "[1, 2, 3]"},
		{"string slice nested JSON array", "execution.auto_accept_tools", `[["a"]]`},
		{"executor out of range", "execution.executor", "podman"},
		{"manage.rules.boost_if_due_within bad duration", "manage.rules.boost_if_due_within", "soon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			err := Set(&c, tc.key, tc.value)
			if err == nil {
				t.Fatalf("Set(%q, %q) = nil; want error", tc.key, tc.value)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
