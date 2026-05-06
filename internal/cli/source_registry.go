package cli

import (
	"fmt"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/source/calendar"
	srcgithub "github.com/haruotsu/marunage/internal/source/github"
	"github.com/haruotsu/marunage/internal/source/gmail"
	"github.com/haruotsu/marunage/internal/source/googletasks"
	"github.com/haruotsu/marunage/internal/source/markdown"
	"github.com/haruotsu/marunage/internal/source/notion"
	"github.com/haruotsu/marunage/internal/source/slack"
	"github.com/haruotsu/marunage/internal/source/slack/reaction"
)

// knownBuiltinNames is the authoritative list of source plugin names this
// binary knows how to register via sources_enabled or --source. Adding a
// new built-in means:
//  1. Append the name here
//  2. Add a corresponding case in registerBuiltin below
//  3. Add a test in source_registry_test.go
//
// Note: "browser" is intentionally excluded — it requires a BrowserDriver
// injected at runtime and cannot be self-registered from config alone.
var knownBuiltinNames = []string{
	"calendar", "github", "gmail", "googletasks",
	"markdown", "notion", "slack", "slack:reaction",
}

// registerBuiltin registers one named source plugin into r.
// cfg provides options for sources that need config (e.g. slack:reaction, gmail).
// files provides file paths for sources that read local files (e.g. markdown).
// lenient: if true, unknown source names are silently skipped (used by web
// dashboard); if false, unknown names return an error (used by discover and loop).
func registerBuiltin(r *source.Registry, name string, cfg config.Config, files []string, lenient bool) error {
	switch name {
	case "markdown":
		var opts []markdown.Option
		if len(files) > 0 {
			opts = append(opts, markdown.WithFiles(files...))
		}
		if err := markdown.RegisterBuiltin(r, opts...); err != nil {
			return fmt.Errorf("register markdown: %w", err)
		}
	case "slack":
		if err := slack.RegisterBuiltin(r); err != nil {
			return fmt.Errorf("register slack: %w", err)
		}
	case "slack:reaction":
		opts := []reaction.Option{
			reaction.WithReactions(cfg.Discovery.Slack.ReactionTrigger.Reactions),
			reaction.WithDMOnComplete(cfg.Discovery.Slack.ReactionTrigger.DMOnComplete),
		}
		if err := reaction.RegisterBuiltin(r, opts...); err != nil {
			return fmt.Errorf("register slack:reaction: %w", err)
		}
	case "gmail":
		gwsOpts := []gmail.GWSOption{}
		if cfg.Discovery.Gmail.NewerThanDays > 0 {
			gwsOpts = append(gwsOpts, gmail.WithNewerThan(cfg.Discovery.Gmail.NewerThanDays))
		}
		var opts []gmail.Option
		opts = append(opts, gmail.WithClient(gmail.NewGWSClient(gwsOpts...)))
		if cfg.Discovery.Gmail.Query != "" {
			opts = append(opts, gmail.WithQuery(cfg.Discovery.Gmail.Query))
		}
		if cfg.Discovery.Gmail.CheckpointKey != "" {
			opts = append(opts, gmail.WithCheckpointKey(cfg.Discovery.Gmail.CheckpointKey))
		}
		if err := gmail.RegisterBuiltin(r, opts...); err != nil {
			return fmt.Errorf("register gmail: %w", err)
		}
	case "github":
		if err := srcgithub.RegisterBuiltin(r); err != nil {
			return fmt.Errorf("register github: %w", err)
		}
	case "calendar":
		if err := calendar.RegisterBuiltin(r); err != nil {
			return fmt.Errorf("register calendar: %w", err)
		}
	case "googletasks":
		if err := googletasks.RegisterBuiltin(r); err != nil {
			return fmt.Errorf("register googletasks: %w", err)
		}
	case "notion":
		if err := notion.RegisterBuiltin(r); err != nil {
			return fmt.Errorf("register notion: %w", err)
		}
	default:
		if lenient {
			return nil
		}
		return fmt.Errorf("unknown source %q (built-in plugins: %v)", name, knownBuiltinNames)
	}
	return nil
}
