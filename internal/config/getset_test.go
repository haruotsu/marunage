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
func TestSetPermissionModeDerivesClaudeCommand(t *testing.T) {
	c := Default() // starts at "bypass"
	if err := Set(&c, "execution.permission_mode", "default"); err != nil {
		t.Fatalf("Set permission_mode = %v", err)
	}
	if c.Execution.PermissionMode != "default" {
		t.Errorf("PermissionMode = %q; want %q", c.Execution.PermissionMode, "default")
	}
	if c.Execution.ClaudeCommand != "claude" {
		t.Errorf("ClaudeCommand = %q; want auto-derived %q", c.Execution.ClaudeCommand, "claude")
	}
}

// TestSetPermissionModeCustomKeepsCommand: switching to "custom" must NOT
// overwrite the existing claude_command, otherwise users lose their hand-
// written command on every mode flip.
func TestSetPermissionModeCustomKeepsCommand(t *testing.T) {
	c := Default()
	c.Execution.ClaudeCommand = "claude --my-flag"
	if err := Set(&c, "execution.permission_mode", "custom"); err != nil {
		t.Fatalf("Set permission_mode custom = %v", err)
	}
	if c.Execution.ClaudeCommand != "claude --my-flag" {
		t.Errorf("ClaudeCommand = %q; want preserved %q", c.Execution.ClaudeCommand, "claude --my-flag")
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
