package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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

// listWorkspacePattern matches a "workspace:NNN" id at the start of a
// line (after optional indent / dashboard markers). The leading anchor
// keeps a task title that happens to contain "workspace:99" out of the
// alive set — without it, an orphan whose title mentions a workspace
// would be falsely treated as live.
var listWorkspacePattern = regexp.MustCompile(`(?m)^[\s*]*(workspace:\d+)`)

// ListWorkspaceIDs runs `cmux list-workspaces`, harvests the leading
// "workspace:NNN" token from each line, and returns the set. A missing
// cmux binary surfaces as cmux.ErrCmuxNotFound (errors.Is-matchable)
// so PR-32 doctor and the clean command can branch on the typed
// sentinel rather than substring-checking the wrapped diagnostic.
func (c cmuxWorkspaceLister) ListWorkspaceIDs(ctx context.Context) ([]string, error) {
	stdout, stderr, err := c.runner.Run(ctx, "cmux", "list-workspaces")
	if err != nil {
		if isBinaryNotFound(err) {
			return nil, cmux.ErrCmuxNotFound
		}
		return nil, fmt.Errorf("cmux list-workspaces: %w (stderr=%s)", err, string(stderr))
	}
	matches := listWorkspacePattern.FindAllStringSubmatch(string(stdout), -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out, nil
}

// isBinaryNotFound mirrors the unexported helper in internal/cmux: a
// missing binary surfaces as exec.ErrNotFound either directly or
// wrapped in *exec.Error, depending on the Go version. CLI callers
// translate it into cmux.ErrCmuxNotFound so the typed sentinel chain
// stays usable from `errors.Is`.
func isBinaryNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, exec.ErrNotFound)
	}
	return false
}
