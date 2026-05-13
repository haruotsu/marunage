// Package workspace defines the backend-neutral surface marunage's
// dispatcher uses to drive one Claude Code session per task. A backend
// (cmux, herdr, ...) implements the Client interface; the dispatcher
// never knows which backend it holds.
//
// The interface is the same shape that internal/cmux historically
// exposed — extracted here so additional backends can slot in without
// the dispatcher growing a switch statement.
package workspace

import (
	"context"
	"errors"
)

// Client is the read/write gateway to whichever terminal multiplexer is
// orchestrating Claude sessions. The dispatcher holds one and shares it
// across goroutines; concrete implementations must be safe for
// concurrent use because parallel dispatch runs many of these methods at
// once.
type Client interface {
	NewWorkspace(ctx context.Context, opts NewWorkspaceOptions) (Workspace, error)
	WaitReady(ctx context.Context, ws Workspace) error
	Send(ctx context.Context, ws Workspace, text string) error
	// ListWorkspaces returns every workspace the backend currently
	// considers live. The reaper diffs this against tasks.ws to detect
	// rows whose workspace has disappeared (mark failed). Order is
	// unspecified; callers turn the slice into a set before doing the
	// diff.
	ListWorkspaces(ctx context.Context) ([]Workspace, error)

	// ReadOutput captures the current visible terminal pane for ws. The
	// returned string is the trimmed backend-rendered pane content.
	// The web UI polls this to stream live output to the browser.
	ReadOutput(ctx context.Context, ws Workspace) (string, error)
}

// ReadinessProbe returns whether ws has finished its boot sequence
// (trust prompt, bypass-permissions prompt, ...) and is ready to accept
// a Send. Splitting probes out per backend keeps WaitReady backend-neutral.
type ReadinessProbe interface {
	IsReady(ctx context.Context, ws Workspace) (bool, error)
}

// ReadinessProbeFunc adapts a plain function into a ReadinessProbe.
type ReadinessProbeFunc func(ctx context.Context, ws Workspace) (bool, error)

// IsReady satisfies ReadinessProbe by delegating to the wrapped function.
func (f ReadinessProbeFunc) IsReady(ctx context.Context, ws Workspace) (bool, error) {
	return f(ctx, ws)
}

// Workspace is the typed handle returned by NewWorkspace. ID is the raw
// identifier the backend uses everywhere — keeping it as a single string
// (rather than a typed int) lets us round-trip backend-specific schemes
// (cmux's "workspace:NNN", herdr's pane ids, ...) without a migration.
type Workspace struct {
	ID   string
	Name string
}

// NewWorkspaceOptions is the input to Client.NewWorkspace. Every field
// is required; missing fields surface as ErrInvalidOptions rather than
// as a confusing backend error.
type NewWorkspaceOptions struct {
	// CWD is the working directory the backend launches the workspace
	// in. The dispatcher passes Task.CWD here so a per-repository task
	// starts inside that repository's checkout.
	CWD string
	// Command is the literal shell line the backend runs inside the new
	// workspace. The dispatcher feeds config.execution.claude_command
	// (default "claude --dangerously-skip-permissions").
	Command string
	// Name is the human-readable label the backend shows in its
	// dashboard. The dispatcher uses "#<id> <title短縮>" so a glance at
	// the backend dashboard matches `marunage list` order.
	Name string
}

// Typed sentinel errors shared by every backend. Callers in dispatch
// and the CLI layer match on these via errors.Is rather than substring-
// checking the wrapped messages.
var (
	// ErrInvalidOptions is returned when NewWorkspaceOptions is missing
	// CWD / Command / Name. Surfaced before any backend call so a buggy
	// dispatcher cannot accidentally spawn an unnamed workspace.
	ErrInvalidOptions = errors.New("workspace: invalid NewWorkspaceOptions")

	// ErrUnparseableOutput is returned when the backend command exits 0
	// but its stdout cannot be parsed into a workspace handle. The
	// dispatcher must not write a blank ws into tasks.ws, so we fail
	// loudly instead of silently storing "".
	ErrUnparseableOutput = errors.New("workspace: could not parse workspace id from backend output")

	// ErrInvalidWorkspace is returned by Send when the caller passes a
	// Workspace with an empty ID — typically a sign that a previous
	// NewWorkspace call failed and its zero-value Workspace was reused
	// by mistake.
	ErrInvalidWorkspace = errors.New("workspace: workspace id is empty")

	// ErrTimeout is returned by WaitReady when the readiness probe
	// never reports ready before the configured startup timeout
	// elapses. Distinct from context.DeadlineExceeded so callers can
	// distinguish "backend is slow" from "the parent deadline fired".
	ErrTimeout = errors.New("workspace: workspace did not become ready before timeout")

	// ErrBackendNotFound is returned when the backend binary is missing
	// from PATH. doctor already checks this at startup; this sentinel
	// is the dispatcher's fallback for the race where the backend
	// binary disappears between `marunage doctor` and the first
	// NewWorkspace call. Backend-specific errors (cmux.ErrCmuxNotFound)
	// wrap this so errors.Is(err, workspace.ErrBackendNotFound) matches
	// either backend.
	ErrBackendNotFound = errors.New("workspace: backend binary not found on PATH")
)
