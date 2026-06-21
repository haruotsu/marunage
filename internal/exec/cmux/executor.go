// Package cmux adapts marunage's existing cmux client (internal/cmux)
// to the backend-agnostic exec.Executor contract. It is the single place
// allowed to speak cmux's vocabulary — NewWorkspace / NewWorkspaceOptions
// / WaitReady / "workspace:NNN" / ReadOutput / ListWorkspaces all stay
// behind this boundary so dispatch / reflection / reaper / web see only
// exec.Session and exec.SessionSpec.
//
// The Executor implements the core exec.Executor plus every capability
// cmux can honour: Attachable (a workspace deeplink), Streamable +
// OutputReader (terminal output), and Lister (live workspaces for the
// reaper).
package cmux

import (
	"context"
	"time"

	cmuxclient "github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/exec"
)

// Handle is the backend-internal value carried in exec.Session.handle for
// cmux sessions. Name preserves the cmux dashboard label; SentinelDir is
// the marunage control directory (~/.marunage/workspaces/<id>) whose
// atomic .exit_code file AwaitExit polls. SentinelDir is empty for
// sessions created by Start today — wiring it through belongs to the
// pipeline PR (R05) that gives the executor the per-task control dir.
type Handle struct {
	Name        string
	SentinelDir string
}

// Executor wraps a cmux client and satisfies exec.Executor + the cmux
// capability set. Construct it with New.
type Executor struct {
	client       cmuxclient.Client
	pollInterval time.Duration
	awaitTimeout time.Duration
}

// Option mutates Executor construction, mirroring the functional-option
// shape used across marunage so call sites stay terse.
type Option func(*Executor)

// WithPollInterval overrides how often Stream / AwaitExit poll cmux.
// Tests squash this to a few milliseconds.
func WithPollInterval(d time.Duration) Option {
	return func(e *Executor) { e.pollInterval = d }
}

// WithAwaitTimeout caps how long AwaitExit waits for the .exit_code
// sentinel before returning ErrAwaitTimeout. Zero means "no cap" (wait
// until ctx is cancelled).
func WithAwaitTimeout(d time.Duration) Option {
	return func(e *Executor) { e.awaitTimeout = d }
}

const defaultPollInterval = 500 * time.Millisecond

// ErrAwaitTimeout and ErrNoSentinelDir are re-exported from the shared exec
// package so existing cmux callers keep matching execcmux.ErrAwaitTimeout /
// execcmux.ErrNoSentinelDir via errors.Is, while AwaitExit delegates the
// actual sentinel polling to exec.AwaitSentinel (the same logic every
// sentinel-based backend shares).
var (
	ErrAwaitTimeout  = exec.ErrAwaitTimeout
	ErrNoSentinelDir = exec.ErrNoSentinelDir
)

// New wraps client in an Executor. The client carries its own runner and
// readiness probe (the CLI wires cmux.NewClaudeReadinessProbe), so the
// Executor only needs poll-cadence knobs of its own.
func New(client cmuxclient.Client, opts ...Option) *Executor {
	e := &Executor{
		client:       client,
		pollInterval: defaultPollInterval,
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.pollInterval <= 0 {
		e.pollInterval = defaultPollInterval
	}
	return e
}

// Start creates a cmux workspace from spec and waits for it to become
// ready. Per the exec.Executor contract it returns a Session whose ID is
// populated even when readiness fails, so the dispatcher can preserve the
// workspace reference and mark the row failed (rather than retrying and
// leaking a second workspace).
func (e *Executor) Start(ctx context.Context, spec exec.SessionSpec) (exec.Session, error) {
	// spec.Env is intentionally not forwarded: the cmux CLI has no
	// per-workspace environment knob, so the session inherits the launcher's
	// environment unchanged (the pre-refactor behaviour). A backend that can
	// honour Env (local process, docker) reads it in its own Start.
	ws, err := e.client.NewWorkspace(ctx, cmuxclient.NewWorkspaceOptions{
		CWD:     spec.Cwd,
		Command: spec.Command,
		Name:    spec.Name,
	})
	if err != nil {
		// Nothing was created: zero Session signals "retryable" upstream.
		return exec.Session{}, err
	}
	session := exec.NewSession(ws.ID, Handle{Name: ws.Name})
	if err := e.client.WaitReady(ctx, ws); err != nil {
		// Created but not ready: return the populated Session so the caller
		// keeps the ws reference and fails the row instead of retrying.
		return session, err
	}
	return session, nil
}

// Send folds embedded newlines (the cmux client does this) and delivers
// prompt to the workspace identified by s.ID.
func (e *Executor) Send(ctx context.Context, s exec.Session, prompt string) error {
	return e.client.Send(ctx, e.workspace(s), prompt)
}

// Attach returns the cmux deeplink a human pastes to take over the
// session. cmux addresses workspaces by their "workspace:NNN" id, so the
// deeplink is that reference behind a cmux:// scheme.
func (e *Executor) Attach(_ context.Context, s exec.Session) (string, error) {
	if s.ID == "" {
		return "", cmuxclient.ErrInvalidWorkspace
	}
	return "cmux://" + s.ID, nil
}

// ReadOutput returns the current visible terminal text for s.
func (e *Executor) ReadOutput(ctx context.Context, s exec.Session) (string, error) {
	return e.client.ReadOutput(ctx, e.workspace(s))
}

// ListSessions returns every workspace cmux currently considers live, as
// exec.Sessions carrying only the id (the reaper diffs ids against
// tasks.ws and needs nothing more).
func (e *Executor) ListSessions(ctx context.Context) ([]exec.Session, error) {
	live, err := e.client.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]exec.Session, 0, len(live))
	for _, ws := range live {
		out = append(out, exec.NewSession(ws.ID, Handle{Name: ws.Name}))
	}
	return out, nil
}

// Stream polls the session's visible output and emits a chunk on the
// returned channel whenever the text changes. The channel closes when ctx
// is cancelled or a read fails (workspace gone). The first non-empty read
// is always emitted so a subscriber sees the current state immediately.
func (e *Executor) Stream(ctx context.Context, s exec.Session) (<-chan []byte, error) {
	if s.ID == "" {
		return nil, cmuxclient.ErrInvalidWorkspace
	}
	ch := make(chan []byte)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(e.pollInterval)
		defer ticker.Stop()
		var last string
		for {
			out, err := e.client.ReadOutput(ctx, e.workspace(s))
			if err != nil {
				return
			}
			if out != last {
				last = out
				select {
				case ch <- []byte(out):
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return ch, nil
}

// AwaitExit polls the session's .exit_code sentinel until it appears,
// ctx is cancelled, or WithAwaitTimeout elapses. The sentinel is written
// atomically by the dispatched Claude (echo $? > .exit_code.tmp && mv),
// so a reader sees either the final value or no file at all. The parsed
// exit code is returned even when non-zero; a non-nil error is reserved
// for I/O / timeout / cancellation.
func (e *Executor) AwaitExit(ctx context.Context, s exec.Session) (int, error) {
	return exec.AwaitSentinel(ctx, e.sentinelDir(s), e.pollInterval, e.awaitTimeout)
}

// workspace reconstructs the cmux handle from an exec.Session. Only the ID
// is needed for Send / ReadOutput; the Name is preserved through the
// handle when present so a future cmux call that wants the label has it.
func (e *Executor) workspace(s exec.Session) cmuxclient.Workspace {
	ws := cmuxclient.Workspace{ID: s.ID}
	if h, ok := s.Handle().(Handle); ok {
		ws.Name = h.Name
	}
	return ws
}

// sentinelDir extracts the control directory from the session handle, or
// "" when none was wired.
func (e *Executor) sentinelDir(s exec.Session) string {
	if h, ok := s.Handle().(Handle); ok {
		return h.SentinelDir
	}
	return ""
}

// Compile-time proof the Executor honours every interface it advertises.
var (
	_ exec.Executor     = (*Executor)(nil)
	_ exec.Attachable   = (*Executor)(nil)
	_ exec.Streamable   = (*Executor)(nil)
	_ exec.OutputReader = (*Executor)(nil)
	_ exec.Lister       = (*Executor)(nil)
)
