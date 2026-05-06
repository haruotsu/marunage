package cli

import (
	"fmt"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/source/markdown"
	"github.com/haruotsu/marunage/internal/source/slack"
	"github.com/haruotsu/marunage/internal/source/slack/reaction"
)

// knownBuiltinNames is the authoritative list of source plugin names this
// binary knows how to register. Adding a new built-in means appending here
// and adding a corresponding case in registerBuiltin.
var knownBuiltinNames = []string{"markdown", "slack", "slack:reaction"}

// registerBuiltin registers one named source plugin into r.
// cfg provides options for sources that need config (e.g. slack:reaction).
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
		return nil
	case "slack":
		if err := slack.RegisterBuiltin(r); err != nil {
			return fmt.Errorf("register slack: %w", err)
		}
		return nil
	case "slack:reaction":
		opts := []reaction.Option{
			reaction.WithReactions(cfg.Discovery.Slack.ReactionTrigger.Reactions),
			reaction.WithDMOnComplete(cfg.Discovery.Slack.ReactionTrigger.DMOnComplete),
		}
		if err := reaction.RegisterBuiltin(r, opts...); err != nil {
			return fmt.Errorf("register slack:reaction: %w", err)
		}
		return nil
	default:
		if lenient {
			return nil
		}
		return fmt.Errorf("unknown source %q (built-in plugins: %v)", name, knownBuiltinNames)
	}
}
