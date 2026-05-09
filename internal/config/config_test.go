package config

import (
	"strings"
	"testing"
)

// TestDefaultConfig pins the documented defaults from docs/requirement.md so
// downstream PRs have a stable baseline to read from.
func TestDefaultConfig(t *testing.T) {
	c := Default()

	if c.Core.MaxParallel != 3 {
		t.Errorf("Core.MaxParallel = %d; want 3", c.Core.MaxParallel)
	}
	if c.Core.LogLevel != "info" {
		t.Errorf("Core.LogLevel = %q; want %q", c.Core.LogLevel, "info")
	}
	if c.Core.DBPath != "~/.marunage/tasks.db" {
		t.Errorf("Core.DBPath = %q; want %q", c.Core.DBPath, "~/.marunage/tasks.db")
	}
	if c.Secrets.Backend != "auto" {
		t.Errorf("Secrets.Backend = %q; want %q", c.Secrets.Backend, "auto")
	}
	if c.Execution.PermissionMode != "bypass" {
		t.Errorf("Execution.PermissionMode = %q; want %q", c.Execution.PermissionMode, "bypass")
	}
	if c.Execution.ClaudeCommand != "claude --dangerously-skip-permissions" {
		t.Errorf("Execution.ClaudeCommand = %q; want bypass-derived command", c.Execution.ClaudeCommand)
	}
	if c.Execution.OnUnknownPermission != "escalate" {
		t.Errorf("Execution.OnUnknownPermission = %q; want %q", c.Execution.OnUnknownPermission, "escalate")
	}
	if c.Execution.HumanWaitTimeout != "30m" {
		t.Errorf("Execution.HumanWaitTimeout = %q; want %q", c.Execution.HumanWaitTimeout, "30m")
	}
	if c.Execution.ReaperStuckThreshold != "24h" {
		t.Errorf("Execution.ReaperStuckThreshold = %q; want %q",
			c.Execution.ReaperStuckThreshold, "24h")
	}
	if c.Reflection.Enabled {
		t.Errorf("Reflection.Enabled = true; want false (cost-conscious default)")
	}
	if c.Reflection.SampleRate != 1.0 {
		t.Errorf("Reflection.SampleRate = %v; want 1.0 (PR-102: when Enabled flips on, every completion runs unless the operator dials it back)",
			c.Reflection.SampleRate)
	}
	if !c.Journal.Enabled {
		t.Errorf("Journal.Enabled = false; want true")
	}
	if c.Web.Bind != "127.0.0.1" {
		t.Errorf("Web.Bind = %q; want %q (localhost-bind default per requirement.md 320)", c.Web.Bind, "127.0.0.1")
	}
	if c.Web.Port != 7777 {
		t.Errorf("Web.Port = %d; want 7777", c.Web.Port)
	}
	if c.Web.Remote {
		t.Errorf("Web.Remote = true; want false (external publish must be opt-in)")
	}

	if err := c.Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v; want nil", err)
	}
}

// TestValidate covers each of the documented schema constraints. Tabular form
// keeps it cheap to add new constraints as the schema grows.
func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "valid default",
			mutate:  func(c *Config) {},
			wantErr: "",
		},
		{
			name:    "max_parallel must be positive",
			mutate:  func(c *Config) { c.Core.MaxParallel = 0 },
			wantErr: "core.max_parallel",
		},
		{
			name:    "max_parallel rejects negative",
			mutate:  func(c *Config) { c.Core.MaxParallel = -1 },
			wantErr: "core.max_parallel",
		},
		{
			name:    "log_level must be known",
			mutate:  func(c *Config) { c.Core.LogLevel = "verbose" },
			wantErr: "core.log_level",
		},
		{
			name:    "db_path must be non-empty",
			mutate:  func(c *Config) { c.Core.DBPath = "" },
			wantErr: "core.db_path",
		},
		{
			name:    "secrets.backend must be known",
			mutate:  func(c *Config) { c.Secrets.Backend = "vault" },
			wantErr: "secrets.backend",
		},
		{
			name:    "execution.permission_mode must be known",
			mutate:  func(c *Config) { c.Execution.PermissionMode = "yolo" },
			wantErr: "execution.permission_mode",
		},
		{
			name: "execution.claude_command empty rejected for custom mode",
			mutate: func(c *Config) {
				c.Execution.PermissionMode = "custom"
				c.Execution.ClaudeCommand = ""
			},
			wantErr: "execution.claude_command",
		},
		{
			name:    "execution.startup_timeout must be positive",
			mutate:  func(c *Config) { c.Execution.StartupTimeout = 0 },
			wantErr: "execution.startup_timeout",
		},
		{
			name:    "execution.on_unknown_permission must be known",
			mutate:  func(c *Config) { c.Execution.OnUnknownPermission = "panic" },
			wantErr: "execution.on_unknown_permission",
		},
		{
			name:    "execution.human_wait_timeout must parse as duration",
			mutate:  func(c *Config) { c.Execution.HumanWaitTimeout = "not-a-duration" },
			wantErr: "execution.human_wait_timeout",
		},
		{
			name:    "execution.reaper_stuck_threshold must parse as duration",
			mutate:  func(c *Config) { c.Execution.ReaperStuckThreshold = "soon" },
			wantErr: "execution.reaper_stuck_threshold",
		},
		{
			name:    "execution.reaper_stuck_threshold rejects empty",
			mutate:  func(c *Config) { c.Execution.ReaperStuckThreshold = "" },
			wantErr: "execution.reaper_stuck_threshold",
		},
		{
			name:    "discovery.interval must parse as duration",
			mutate:  func(c *Config) { c.Discovery.Interval = "soon" },
			wantErr: "discovery.interval",
		},
		{
			name:    "reflection.sample_rate clamped to [0,1] (above)",
			mutate:  func(c *Config) { c.Reflection.SampleRate = 1.5 },
			wantErr: "reflection.sample_rate",
		},
		{
			name:    "reflection.sample_rate clamped to [0,1] (below)",
			mutate:  func(c *Config) { c.Reflection.SampleRate = -0.1 },
			wantErr: "reflection.sample_rate",
		},
		{
			name:    "web.bind must be non-empty",
			mutate:  func(c *Config) { c.Web.Bind = "" },
			wantErr: "web.bind",
		},
		{
			name:    "web.port must be in [1, 65535]",
			mutate:  func(c *Config) { c.Web.Port = 0 },
			wantErr: "web.port",
		},
		{
			name:    "web.port rejects above 65535",
			mutate:  func(c *Config) { c.Web.Port = 70000 },
			wantErr: "web.port",
		},
		{
			name:    "journal.interval must parse as duration",
			mutate:  func(c *Config) { c.Journal.Interval = "never" },
			wantErr: "journal.interval",
		},
		{
			name: "reaction_trigger enabled with empty reactions",
			mutate: func(c *Config) {
				c.Discovery.Slack.ReactionTrigger.Enabled = true
				c.Discovery.Slack.ReactionTrigger.Reactions = nil
			},
			wantErr: "discovery.slack.reaction_trigger.reactions",
		},
		{
			name: "reaction_trigger disabled with empty reactions is OK",
			mutate: func(c *Config) {
				c.Discovery.Slack.ReactionTrigger.Enabled = false
				c.Discovery.Slack.ReactionTrigger.Reactions = nil
			},
			wantErr: "",
		},
		{
			name:    "discovery.gmail.newer_than_days rejects negative",
			mutate:  func(c *Config) { c.Discovery.Gmail.NewerThanDays = -1 },
			wantErr: "discovery.gmail.newer_than_days",
		},
		{
			name:    "discovery.gmail.max_results rejects negative",
			mutate:  func(c *Config) { c.Discovery.Gmail.MaxResults = -1 },
			wantErr: "discovery.gmail.max_results",
		},
		{
			name:    "discovery.dispatch_interval must parse as duration",
			mutate:  func(c *Config) { c.Discovery.DispatchInterval = "not-a-duration" },
			wantErr: "discovery.dispatch_interval",
		},
		{
			name:    "discovery.dispatch_interval rejects negative",
			mutate:  func(c *Config) { c.Discovery.DispatchInterval = "-30s" },
			wantErr: "discovery.dispatch_interval",
		},
		{
			name:    "discovery.dispatch_interval accepts empty (opt-out)",
			mutate:  func(c *Config) { c.Discovery.DispatchInterval = "" },
			wantErr: "",
		},
		{
			name:    "discovery.dispatch_interval accepts zero (disabled)",
			mutate:  func(c *Config) { c.Discovery.DispatchInterval = "0s" },
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil; want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %v; want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestSecretsBackendAcceptsAllDocumentedValues pins the positive side of
// the schema constraint (Validate rejects garbage; this test asserts that
// every documented value is also accepted). Without this, narrowing the
// allowed set to e.g. just "auto" would slip through unnoticed because
// the negative test only checks one rejected value.
func TestSecretsBackendAcceptsAllDocumentedValues(t *testing.T) {
	for _, backend := range []string{"auto", "keyring", "pass", "age", "file", "env"} {
		t.Run(backend, func(t *testing.T) {
			c := Default()
			c.Secrets.Backend = backend
			if err := c.Validate(); err != nil {
				t.Errorf("Validate() rejected documented backend %q: %v", backend, err)
			}
		})
	}
}

// TestPermissionModeDerivesClaudeCommand documents the spec rule: setting
// permission_mode to one of the four named modes overrides claude_command.
// custom is the only mode that lets the user keep an arbitrary command.
func TestPermissionModeDerivesClaudeCommand(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"bypass", "claude --dangerously-skip-permissions"},
		{"default", "claude"},
		{"acceptEdits", "claude --permission-mode acceptEdits"},
		{"plan", "claude --permission-mode plan"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			got := ClaudeCommandFor(tc.mode)
			if got != tc.want {
				t.Errorf("ClaudeCommandFor(%q) = %q; want %q", tc.mode, got, tc.want)
			}
		})
	}
}
