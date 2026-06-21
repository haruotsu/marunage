// Package exec is marunage's backend-agnostic execution layer. It hides
// how a Claude session is launched, fed a prompt, and watched for
// completion behind a small interface so the rest of marunage
// (dispatch / reflection / reaper / web) never speaks a concrete
// backend's vocabulary. cmux is the first implementation
// (internal/exec/cmux); tmux / local-process / docker land in later PRs
// without touching any consumer.
//
// Design (docs/redesign_layering.md §4): every backend MUST implement the
// three Executor methods — that is the minimum a "fire-and-forget" run
// needs. Richer abilities (human attach, live output) differ per backend
// and are expressed as optional capability interfaces a caller
// type-asserts for, mirroring the io.WriterTo / http.Flusher idiom:
//
//	if a, ok := executor.(exec.Attachable); ok {
//	    link, _ := a.Attach(ctx, sess)
//	}
package exec

import "context"

// Executor is the backend-agnostic minimum every execution target must
// provide. A backend that satisfies only this interface still supports
// marunage's core "dispatch a task, wait for it to finish" loop.
type Executor interface {
	// Start launches an isolated session in spec.Cwd and runs spec.Command
	// inside it (the claude invocation derived from the permission mode).
	//
	// Readiness is part of Start: when it returns a nil error the session
	// is ready to accept Send. When the session was created but never
	// became ready, Start returns a non-nil error AND a Session whose ID
	// is populated, so the caller can tell "nothing was created, retry"
	// (empty ID) apart from "a session leaked, mark it failed and let the
	// reaper reclaim it" (non-empty ID). A failure before anything was
	// created returns the zero Session.
	Start(ctx context.Context, spec SessionSpec) (Session, error)

	// Send delivers prompt to an already-started session. Newline folding
	// (so a multi-line prompt is not submitted line-by-line) and any
	// submit keystroke are the implementation's concern.
	Send(ctx context.Context, s Session, prompt string) error

	// AwaitExit blocks until the session's process exits and returns its
	// exit code. The completion mechanism (cmux's atomic .exit_code
	// sentinel, a local cmd.Wait, ...) is hidden behind this method so
	// callers never poll a backend-specific artefact themselves.
	AwaitExit(ctx context.Context, s Session) (int, error)
}

// Attachable is the optional capability for backends that can hand a
// human an interactive view of a running session. The returned string is
// opaque attach instructions: cmux returns a URI deeplink, tmux returns a
// runnable `tmux attach` command. A consumer must render it as plain text
// rather than assume a clickable URI. A bare local process cannot attach
// and simply does not implement this interface.
type Attachable interface {
	Attach(ctx context.Context, s Session) (deeplink string, err error)
}

// Streamable is the optional capability for backends that can publish a
// session's live terminal output. The returned channel delivers output
// chunks until it is closed (session gone or ctx cancelled).
type Streamable interface {
	Stream(ctx context.Context, s Session) (<-chan []byte, error)
}

// OutputReader is the optional capability for backends that can return a
// point-in-time snapshot of a session's visible output. The web live-
// stream endpoint polls this and emits a diff; it is split from
// Streamable so a poll-based consumer does not have to manage a channel
// lifecycle.
type OutputReader interface {
	ReadOutput(ctx context.Context, s Session) (string, error)
}

// Lister is the optional capability for backends that can enumerate the
// sessions they currently consider live. The reaper diffs this against
// the running rows in tasks.db to detect sessions that vanished out from
// under marunage (invariant #5 "Crash safety").
type Lister interface {
	ListSessions(ctx context.Context) ([]Session, error)
}

// SessionSpec is the backend-agnostic launch request. It deliberately
// carries no cmux vocabulary: a backend translates these fields into its
// own create call (cmux maps Cwd/Command/Name onto NewWorkspaceOptions).
type SessionSpec struct {
	// Cwd is the working directory the session is launched in.
	Cwd string
	// Command is the literal command line the session runs (the claude
	// invocation derived from the configured permission mode).
	Command string
	// Name is the human-readable label a backend shows in its dashboard.
	Name string
	// Env are extra environment variables for the session. Optional;
	// nil/empty means "inherit the launcher's environment unchanged".
	Env map[string]string
}

// Session is the opaque handle a backend returns from Start (and Lister).
// ID is the backend's stable reference (cmux's "workspace:NNN"); handle
// carries any richer backend-internal value the public API should not
// expose. Callers pass Session back into Send / AwaitExit / capabilities
// verbatim.
type Session struct {
	ID     string
	handle any
}

// NewSession builds a Session from a backend reference and an optional
// backend-internal handle. Consumers that already hold a stored session
// id (dispatch re-sending, the web send endpoint) use NewSession(id, nil)
// to address an existing session without going through Start.
func NewSession(id string, handle any) Session {
	return Session{ID: id, handle: handle}
}

// Handle returns the backend-internal value stashed at construction, or
// nil. Only the backend that created the Session knows the concrete type;
// it type-asserts the value back when it needs it.
func (s Session) Handle() any { return s.handle }
