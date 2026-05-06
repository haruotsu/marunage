// Package config defines marunage's typed configuration tree, schema
// validation, and the Load/Save primitives that back `marunage config`
// (see docs/requirement.md "設定ファイル `config.toml`").
//
// Downstream packages read settings via Get / Load and never edit the file
// directly; that contract keeps audit logging (PR-04) and the rollback-on-
// validation-error guarantee centralized here.
package config

import (
	"fmt"
	"time"
)

// Config is the typed view of ~/.marunage/config.toml. The struct mirrors
// the example in docs/requirement.md so each top-level table corresponds to
// one TOML section.
type Config struct {
	Core       CoreConfig       `toml:"core"`
	Secrets    SecretsConfig    `toml:"secrets"`
	Discovery  DiscoveryConfig  `toml:"discovery"`
	Execution  ExecutionConfig  `toml:"execution"`
	Reflection ReflectionConfig `toml:"reflection"`
	Journal    JournalConfig    `toml:"journal"`
	Notify     NotifyConfig     `toml:"notify"`
	Web        WebConfig        `toml:"web"`
}

type CoreConfig struct {
	DBPath      string `toml:"db_path"`
	MaxParallel int    `toml:"max_parallel"`
	DefaultCwd  string `toml:"default_cwd"`
	LogLevel    string `toml:"log_level"`
}

type SecretsConfig struct {
	Backend string `toml:"backend"`
}

type DiscoveryConfig struct {
	Interval       string          `toml:"interval"`
	SourcesEnabled []string        `toml:"sources_enabled"`
	Gmail          DiscoveryGmail  `toml:"gmail"`
	Slack          DiscoverySlack  `toml:"slack"`
	GitHub         DiscoveryGitHub `toml:"github"`
}

type DiscoveryGmail struct {
	Query         string `toml:"query"`
	CheckpointKey string `toml:"checkpoint_key"`
	// NewerThanDays limits discovery to messages received within the past N days.
	// 0 means no time filter. Appended to Query as "newer_than:Nd".
	NewerThanDays int `toml:"newer_than_days"`
	// MaxResults caps the number of messages fetched per discovery run.
	// 0 uses the GWSClient default (50). Each result costs one messages.get
	// subprocess, so keeping this small bounds the N+1 subprocess count.
	MaxResults int `toml:"max_results"`
}

type DiscoverySlack struct {
	MCPServer       string                     `toml:"mcp_server"`
	IncludeDM       bool                       `toml:"include_dm"`
	IncludeMentions bool                       `toml:"include_mentions"`
	ReactionTrigger SlackReactionTriggerConfig `toml:"reaction_trigger"`
}

// SlackReactionTriggerConfig holds the settings for the Slack Reaction
// Trigger source (PR-100). When enabled, the plugin polls for messages
// that have been reacted to with one of the configured reactions and
// creates a task for each matching event.
type SlackReactionTriggerConfig struct {
	// Enabled gates the plugin. Off by default so a fresh install does not
	// start watching reactions until the user opts in.
	Enabled bool `toml:"enabled"`

	// Reactions is the list of emoji names (without colons) to watch.
	// Example: ["todo", "inbox_tray"].
	Reactions []string `toml:"reactions"`

	// DMOnComplete sends a DM to the user who added the reaction when the
	// task transitions to done.
	DMOnComplete bool `toml:"dm_on_complete"`
}

type DiscoveryGitHub struct {
	Filter string `toml:"filter"`
}

type ExecutionConfig struct {
	PermissionMode      string   `toml:"permission_mode"`
	ClaudeCommand       string   `toml:"claude_command"`
	StartupTimeout      int      `toml:"startup_timeout"`
	PromptSkill         string   `toml:"prompt_skill"`
	AllowedCwdPrefixes  []string `toml:"allowed_cwd_prefixes"`
	AutoAcceptTools     []string `toml:"auto_accept_tools"`
	OnUnknownPermission string   `toml:"on_unknown_permission"`
	HumanWaitTimeout    string   `toml:"human_wait_timeout"`
	// ReaperStuckThreshold caps how long a row may stay status=running
	// before PR-44 reaper appends a "stuck running over <threshold>"
	// warning to audit.log + judgment_reason. Stays a Go duration string
	// (parsed via time.ParseDuration) for parity with HumanWaitTimeout
	// and so config.toml stays grep-able. Default "24h" tracks
	// docs/requirement.md PR-44 ("started_at + 24h 超の running を警告").
	ReaperStuckThreshold string            `toml:"reaper_stuck_threshold"`
	LockKeys             map[string]string `toml:"lock_keys"`
}

type ReflectionConfig struct {
	Enabled    bool     `toml:"enabled"`
	SampleRate float64  `toml:"sample_rate"`
	TaggedOnly []string `toml:"tagged_only"`
}

type JournalConfig struct {
	Enabled  bool     `toml:"enabled"`
	Interval string   `toml:"interval"`
	Sources  []string `toml:"sources"`
}

type NotifyConfig struct {
	OnComplete bool `toml:"on_complete"`
	OnFailure  bool `toml:"on_failure"`
}

// WebConfig drives the `marunage web` server (PR-62).
//
// Bind/Port pin where the listener attaches; defaults bind to loopback so a
// fresh install never publishes the dashboard externally without an explicit
// opt-in (docs/requirement.md 320: "localhost bind デフォルト、外部公開は
// オプトイン"). Remote = true is the explicit opt-in CLI surface that lets
// the binary swap to 0.0.0.0 — authentication itself lands in a later PR.
type WebConfig struct {
	Bind   string `toml:"bind"`
	Port   int    `toml:"port"`
	Remote bool   `toml:"remote"`
}

// Allowed values per the documented schema. Centralised so Validate and the
// Set primitive both read from the same source of truth.
var (
	allowedSecretsBackends      = []string{"auto", "keyring", "pass", "age", "file", "env"}
	allowedPermissionModes      = []string{"bypass", "default", "acceptEdits", "plan", "custom"}
	allowedOnUnknownPermissions = []string{"escalate", "fail", "retry"}
	allowedLogLevels            = []string{"debug", "info", "warn", "error"}
)

// IsValidOnUnknownPermission reports whether s is a recognised value
// for execution.on_unknown_permission. Exported so downstream packages
// (notably internal/dispatch) can reuse the same enum without
// duplicating the list — a divergence here would let a typo slip
// through Validate but be rejected later, or vice versa.
func IsValidOnUnknownPermission(s string) bool {
	return contains(allowedOnUnknownPermissions, s)
}

// IsValidPermissionMode reports whether s is a recognised value for
// execution.permission_mode. Same single-source-of-truth rationale as
// IsValidOnUnknownPermission above.
func IsValidPermissionMode(s string) bool {
	return contains(allowedPermissionModes, s)
}

// Default returns the configuration shipped to a freshly initialised user.
// Values are taken from the example block in docs/requirement.md so the
// documentation and the binary cannot drift apart.
func Default() Config {
	return Config{
		Core: CoreConfig{
			DBPath:      "~/.marunage/tasks.db",
			MaxParallel: 3,
			DefaultCwd:  "~/works",
			LogLevel:    "info",
		},
		Secrets: SecretsConfig{
			Backend: "auto",
		},
		Discovery: DiscoveryConfig{
			Interval:       "10m",
			SourcesEnabled: []string{"markdown"},
			Gmail: DiscoveryGmail{
				Query:         "is:unread to:me -label:auto-archived",
				CheckpointKey: "gmail_last_id",
				NewerThanDays: 7,
			},
			Slack: DiscoverySlack{
				MCPServer:       "slack",
				IncludeDM:       true,
				IncludeMentions: true,
				ReactionTrigger: SlackReactionTriggerConfig{
					Enabled:      false,
					Reactions:    []string{"todo", "inbox_tray"},
					DMOnComplete: false,
				},
			},
			GitHub: DiscoveryGitHub{
				Filter: "is:open assignee:@me",
			},
		},
		Execution: ExecutionConfig{
			PermissionMode:     "bypass",
			ClaudeCommand:      ClaudeCommandFor("bypass"),
			StartupTimeout:     60,
			PromptSkill:        "marunage-execute",
			AllowedCwdPrefixes: []string{"~/works", "~/src"},
			AutoAcceptTools: []string{
				"Read", "Grep", "Glob", "WebSearch",
				"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)",
				"Bash(ls:*)", "Bash(cat:*)",
			},
			OnUnknownPermission:  "escalate",
			HumanWaitTimeout:     "30m",
			ReaperStuckThreshold: "24h",
			LockKeys: map[string]string{
				"^repo:.*":  "git-repo",
				"^slack:.*": "slack-channel",
			},
		},
		Reflection: ReflectionConfig{
			Enabled:    false,
			SampleRate: 1.0,
			TaggedOnly: []string{"important"},
		},
		Journal: JournalConfig{
			Enabled:  true,
			Interval: "30m",
			Sources:  []string{"slack", "calendar", "git", "github", "marunage"},
		},
		Notify: NotifyConfig{
			OnComplete: true,
			OnFailure:  true,
		},
		Web: WebConfig{
			Bind: "127.0.0.1",
			Port: 7777,
		},
	}
}

// ClaudeCommandFor returns the canonical claude_command for one of the four
// non-custom permission modes. custom is intentionally excluded because the
// user supplies the command verbatim.
func ClaudeCommandFor(mode string) string {
	switch mode {
	case "bypass":
		return "claude --dangerously-skip-permissions"
	case "default":
		return "claude"
	case "acceptEdits":
		return "claude --permission-mode acceptEdits"
	case "plan":
		return "claude --permission-mode plan"
	}
	return ""
}

// Validate reports the first schema violation it finds. Returning the first
// error (rather than aggregating) keeps the failure message focused; users
// fix one thing, retry, and surface the next.
func (c Config) Validate() error {
	if c.Core.DBPath == "" {
		return fmt.Errorf("core.db_path: must be a non-empty path")
	}
	if c.Core.MaxParallel <= 0 {
		return fmt.Errorf("core.max_parallel: must be > 0 (got %d)", c.Core.MaxParallel)
	}
	if !contains(allowedLogLevels, c.Core.LogLevel) {
		return fmt.Errorf("core.log_level: %q not in %v", c.Core.LogLevel, allowedLogLevels)
	}
	if !contains(allowedSecretsBackends, c.Secrets.Backend) {
		return fmt.Errorf("secrets.backend: %q not in %v", c.Secrets.Backend, allowedSecretsBackends)
	}
	if !contains(allowedPermissionModes, c.Execution.PermissionMode) {
		return fmt.Errorf("execution.permission_mode: %q not in %v", c.Execution.PermissionMode, allowedPermissionModes)
	}
	if c.Execution.PermissionMode == "custom" && c.Execution.ClaudeCommand == "" {
		return fmt.Errorf("execution.claude_command: required when execution.permission_mode = %q", "custom")
	}
	if c.Execution.StartupTimeout <= 0 {
		return fmt.Errorf("execution.startup_timeout: must be > 0 (got %d)", c.Execution.StartupTimeout)
	}
	if !contains(allowedOnUnknownPermissions, c.Execution.OnUnknownPermission) {
		return fmt.Errorf("execution.on_unknown_permission: %q not in %v", c.Execution.OnUnknownPermission, allowedOnUnknownPermissions)
	}
	if _, err := time.ParseDuration(c.Execution.HumanWaitTimeout); err != nil {
		return fmt.Errorf("execution.human_wait_timeout: %w", err)
	}
	if _, err := time.ParseDuration(c.Execution.ReaperStuckThreshold); err != nil {
		return fmt.Errorf("execution.reaper_stuck_threshold: %w", err)
	}
	if _, err := time.ParseDuration(c.Discovery.Interval); err != nil {
		return fmt.Errorf("discovery.interval: %w", err)
	}
	if c.Journal.Enabled {
		d, err := time.ParseDuration(c.Journal.Interval)
		if err != nil {
			return fmt.Errorf("journal.interval: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("journal.interval: must be > 0 (got %v)", d)
		}
	}
	rt := c.Discovery.Slack.ReactionTrigger
	if rt.Enabled && len(rt.Reactions) == 0 {
		return fmt.Errorf("discovery.slack.reaction_trigger.reactions: must be non-empty when enabled = true")
	}
	if c.Reflection.SampleRate < 0 || c.Reflection.SampleRate > 1 {
		return fmt.Errorf("reflection.sample_rate: must be in [0,1] (got %v)", c.Reflection.SampleRate)
	}
	if c.Web.Bind == "" {
		return fmt.Errorf("web.bind: must be a non-empty host or IP")
	}
	if c.Web.Port < 1 || c.Web.Port > 65535 {
		return fmt.Errorf("web.port: must be in [1,65535] (got %d)", c.Web.Port)
	}
	return nil
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
