package cli

import (
	"context"
	"fmt"
	"regexp"

	"github.com/haruotsu/marunage/internal/cmux"
)

// workspaceLister is the read-only probe `marunage clean` uses to decide
// which tasks have a stale ws reference. The interface lives in the CLI
// layer (rather than `internal/cmux`) for two reasons:
//
//  1. PR-40 (the cmux wrapper) ships only NewWorkspace / WaitReady / Send
//     because those were the dispatch-layer's needs at the time. Adding a
//     listing method to the cmux.Client interface would expand PR-40's
//     surface for a feature only PR-22 consumes.
//  2. Keeping the abstraction here lets PR-22 swap implementations cheaply
//     once cmux ships an authoritative `list-workspaces --json` (the
//     current production wiring is the best-effort regex parse below).
//
// All implementations must be safe for concurrent use; the CLI is single-
// threaded today but that is not a guarantee future callers should rely on.
type workspaceLister interface {
	// ListWorkspaceIDs returns every "workspace:NNN" id cmux currently
	// considers live. Order is unspecified; clean turns the slice into a
	// set before doing the orphan diff.
	ListWorkspaceIDs(ctx context.Context) ([]string, error)
}

// workspaceListerFactory opens a workspaceLister given the resolved
// configPath. The factory shape mirrors taskRepoFactory so tests can
// install a fake via withWorkspaceListerFactory and production code falls
// through to productionWorkspaceListerFactory.
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

// productionWorkspaceListerFactory wires a cmuxWorkspaceLister backed by
// the default ExecRunner. configPath is unused today; reserving the slot
// keeps the factory shape uniform with mirrorFactory / taskRepoFactory so
// a future cmux-binary-path config knob has somewhere to land.
func productionWorkspaceListerFactory(_ context.Context, _ string) (workspaceLister, error) {
	return cmuxWorkspaceLister{runner: cmux.ExecRunner{}}, nil
}

// cmuxWorkspaceLister is the production workspaceLister: it shells out to
// `cmux list-workspaces` and harvests every "workspace:NNN" token from
// stdout. The regex parse (rather than a JSON contract) is deliberate: at
// the time PR-22 ships, cmux does not document a stable `--json` flag for
// listing, and the textual banner is the path PR-40's NewWorkspace already
// parses for round-trips. When cmux ships a structured listing API, swap
// this implementation rather than the workspaceLister interface.
type cmuxWorkspaceLister struct {
	runner cmux.Runner
}

// listWorkspacePattern mirrors the unexported pattern in internal/cmux but
// is duplicated here so adding a method to cmux.Client is not a
// prerequisite for PR-22. The two patterns must stay in sync: any change
// to cmux's "workspace:NNN" banner format breaks both call sites at once,
// which is the whole point of pinning it twice.
var listWorkspacePattern = regexp.MustCompile(`workspace:\d+`)

// ListWorkspaceIDs runs `cmux list-workspaces`, harvests every
// "workspace:NNN" token from stdout, and returns the set. The runner
// surfaces exec.ErrNotFound when cmux is missing from PATH; the caller in
// task_clean.go propagates that into a non-zero exit so the operator sees
// the cause rather than a silent "no orphans found" answer.
func (c cmuxWorkspaceLister) ListWorkspaceIDs(ctx context.Context) ([]string, error) {
	stdout, stderr, err := c.runner.Run(ctx, "cmux", "list-workspaces")
	if err != nil {
		return nil, fmt.Errorf("cmux list-workspaces: %w (stderr=%s)", err, string(stderr))
	}
	return listWorkspacePattern.FindAllString(string(stdout), -1), nil
}
