package cli

import (
	"context"
	"fmt"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/workspace"
)

// workspaceLister is the read-only probe `marunage clean` uses to
// decide which tasks have a stale ws reference. It is intentionally
// narrower than workspace.Client so tests can inject a fake lister
// without satisfying the full backend surface.
type workspaceLister interface {
	// ListWorkspaceIDs returns every workspace id the configured
	// backend currently considers live. Order is unspecified; clean
	// turns the slice into a set before doing the orphan diff.
	ListWorkspaceIDs(ctx context.Context) ([]string, error)
}

// workspaceListerFactory opens a workspaceLister given the resolved
// configPath. The factory shape mirrors taskRepoFactory so tests can
// install a fake via withWorkspaceListerFactory and production code
// falls through to productionWorkspaceListerFactory.
type workspaceListerFactory func(ctx context.Context, configPath string) (workspaceLister, error)

// workspaceListerFactoryHook is the package-private slot tests use via
// withWorkspaceListerFactory. Production callers see nil and fall through.
var workspaceListerFactoryHook workspaceListerFactory

// withWorkspaceListerFactory swaps in a fake factory and restores the
// prior hook on test completion, mirroring withMirrorFactory.
func withWorkspaceListerFactory(t interface{ Cleanup(func()) }, f workspaceListerFactory) {
	prev := workspaceListerFactoryHook
	workspaceListerFactoryHook = f
	t.Cleanup(func() { workspaceListerFactoryHook = prev })
}

// activeWorkspaceListerFactory returns the test override when one is
// installed, otherwise the production implementation.
func activeWorkspaceListerFactory() workspaceListerFactory {
	if workspaceListerFactoryHook != nil {
		return workspaceListerFactoryHook
	}
	return productionWorkspaceListerFactory
}

// productionWorkspaceListerFactory wires a clientWorkspaceLister
// backed by whichever backend the config selects. clean used to ship
// its own cmux-specific regex parser; now both backends share the same
// ListWorkspaces implementation living in internal/workspace/<backend>.
func productionWorkspaceListerFactory(_ context.Context, configPath string) (workspaceLister, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", configPath, err)
	}
	return clientWorkspaceLister{client: newWorkspaceClient(cfg, false)}, nil
}

// clientWorkspaceLister adapts a workspace.Client into the narrow
// workspaceLister interface clean consumes. ListWorkspaces is the only
// method we touch, so the wrapping struct stays trivial.
type clientWorkspaceLister struct {
	client workspace.Client
}

func (c clientWorkspaceLister) ListWorkspaceIDs(ctx context.Context) ([]string, error) {
	ws, err := c.client.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out, nil
}
