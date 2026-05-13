package cli

import (
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/workspace"
	"github.com/haruotsu/marunage/internal/workspace/cmux"
	"github.com/haruotsu/marunage/internal/workspace/herdr"
)

// newWorkspaceClient builds the production workspace.Client for cfg's
// execution.backend. The caller decides whether to wire a Claude
// readiness probe via withProbe: dispatch / loop need it (they
// WaitReady on the live pane), while reaper / project / web only call
// ListWorkspaces / ReadOutput and skip the probe so WaitReady is never
// invoked. Default backend is "cmux" for back-compat with older
// config.toml files that predate the field.
func newWorkspaceClient(cfg config.Config, withProbe bool) workspace.Client {
	switch cfg.EffectiveBackend() {
	case "herdr":
		var opts []herdr.Option
		if withProbe {
			opts = append(opts, herdr.WithReadinessProbe(herdr.NewClaudeReadinessProbe()))
		}
		return herdr.NewClient(opts...)
	default: // cmux
		var opts []cmux.Option
		if withProbe {
			opts = append(opts, cmux.WithReadinessProbe(cmux.NewClaudeReadinessProbe()))
		}
		return cmux.NewClient(opts...)
	}
}
