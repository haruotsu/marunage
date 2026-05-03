// Package cmux is marunage's wrapper around the external `cmux` CLI. It
// owns the operations the dispatcher (PR-42) needs to drive one Claude
// session per task — see docs/requirement.md lines 152-179. The public
// surface is a Client interface with a default exec.Command-backed
// Runner, and tests inject a fake Runner so the package never spawns a
// real cmux during `go test`. Functional options keep construction
// terse at call sites while leaving room for future knobs without
// breaking callers.
package cmux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Client is the read/write gateway to cmux. The dispatcher (PR-42) holds
// one and shares it across goroutines; concrete implementations must be
// safe for concurrent use because parallel dispatch runs many of these
// methods at once.
type Client interface {
	NewWorkspace(ctx context.Context, opts NewWorkspaceOptions) (Workspace, error)
	WaitReady(ctx context.Context, ws Workspace) error
	Send(ctx context.Context, ws Workspace, text string) error
}

// ReadinessProbe returns whether ws has finished its boot sequence (trust
// prompt, bypass-permissions prompt, ...) and is ready to accept a Send.
// PR-42 will eventually wire this to a tail of cmux's status JSON; the
// interface is split out so this package can ship before that arrives
// and tests can drive WaitReady without a real cmux.
type ReadinessProbe interface {
	IsReady(ctx context.Context, ws Workspace) (bool, error)
}

// ReadinessProbeFunc adapts a plain function into a ReadinessProbe so a
// caller can write `WithReadinessProbe(ReadinessProbeFunc(myFunc))`
// rather than declaring a struct.
type ReadinessProbeFunc func(ctx context.Context, ws Workspace) (bool, error)

func (f ReadinessProbeFunc) IsReady(ctx context.Context, ws Workspace) (bool, error) {
	return f(ctx, ws)
}

// neverReadyProbe is the default. Until PR-42 wires a real probe, calling
// WaitReady on a freshly-built Client always exhausts the timeout — which
// is the right answer: a caller that forgot to inject a probe must not
// silently treat every workspace as ready.
type neverReadyProbe struct{}

func (neverReadyProbe) IsReady(_ context.Context, _ Workspace) (bool, error) {
	return false, nil
}

// Workspace is the typed handle returned by NewWorkspace. ID is the raw
// "workspace:NNN" string cmux uses everywhere — keeping it as a single
// string (rather than an int) lets us round-trip future cmux schemes
// (named workspaces, sharded IDs) without a migration.
type Workspace struct {
	ID   string
	Name string
}

// NewWorkspaceOptions is the input to Client.NewWorkspace. Every field is
// required; missing fields surface as ErrInvalidOptions rather than as a
// confusing cmux error.
type NewWorkspaceOptions struct {
	// CWD is the working directory cmux launches the workspace in. The
	// dispatcher passes Task.CWD here so a per-repository task starts
	// inside that repository's checkout.
	CWD string
	// Command is the literal shell line cmux runs inside the new
	// workspace. The dispatcher feeds config.execution.claude_command
	// (default "claude --dangerously-skip-permissions").
	Command string
	// Name is the human-readable label cmux shows in its dashboard.
	// PR-42 uses "#<id> <title短縮>" so a glance at `cmux dashboard`
	// matches `marunage list` order.
	Name string
}

// Typed sentinel errors. Callers in PR-42 (dispatch) and the CLI layer
// (PR-32 doctor surfaces ErrCmuxNotFound) match on these via errors.Is
// rather than substring-checking the wrapped messages.
var (
	// ErrCmuxNotFound is returned when the cmux binary is missing from
	// PATH. doctor already checks this at startup; this sentinel is the
	// dispatcher's fallback for the race where cmux disappears between
	// `marunage doctor` and the first NewWorkspace call.
	ErrCmuxNotFound = errors.New("cmux: binary not found on PATH")

	// ErrInvalidOptions is returned when NewWorkspaceOptions is missing
	// CWD / Command / Name. Surfaced before any Runner call so a buggy
	// dispatcher cannot accidentally spawn an unnamed workspace.
	ErrInvalidOptions = errors.New("cmux: invalid NewWorkspaceOptions")

	// ErrUnparseableOutput is returned when `cmux new-workspace` exits
	// 0 but its stdout does not contain a "workspace:NNN" banner. The
	// dispatcher must not write a blank ws into tasks.ws, so we fail
	// loudly instead of silently storing "".
	ErrUnparseableOutput = errors.New("cmux: could not parse workspace id from cmux output")

	// ErrInvalidWorkspace is returned by Send when the caller passes a
	// Workspace with an empty ID — typically a sign that a previous
	// NewWorkspace call failed and its zero-value Workspace was reused
	// by mistake.
	ErrInvalidWorkspace = errors.New("cmux: workspace id is empty")

	// ErrTimeout is returned by WaitReady when the readiness probe
	// never reports ready before the configured startup timeout
	// elapses. Distinct from context.DeadlineExceeded so callers can
	// distinguish "cmux is slow" from "the parent deadline fired".
	ErrTimeout = errors.New("cmux: workspace did not become ready before timeout")
)

// Defaults match docs/requirement.md execution dispatcher details. The
// 60s startup timeout is the requirement; the 500ms poll interval is the
// implementation choice — short enough that an interactive `marunage
// dispatch` feels responsive, long enough not to spam cmux's status
// channel during a normal 3-5s startup.
const (
	defaultStartupTimeout = 60 * time.Second
	defaultPollInterval   = 500 * time.Millisecond
)

// client is the production Client. Tests build it through NewClient so
// the struct itself can stay unexported.
type client struct {
	runner         Runner
	probe          ReadinessProbe
	startupTimeout time.Duration
	pollInterval   time.Duration
	clock          func() time.Time

	// fallbackBinary is the name of the Enter-appending wrapper used as
	// failover when `cmux send` fails (see docs/requirement.md execution
	// dispatcher step 2.f). Kept configurable via WithFallbackBinary so
	// future cmux releases can rename it without breaking callers.
	fallbackBinary string
}

// Option mutates client construction. The functional-option shape leaves
// room for future knobs (logger, metrics) without breaking callers.
type Option func(*client)

// WithRunner injects a Runner. Tests pass a fake; production code can
// also pass a wrapping Runner that adds tracing / retry / metrics.
func WithRunner(r Runner) Option {
	return func(c *client) { c.runner = r }
}

// WithReadinessProbe injects the probe WaitReady polls. PR-42 will pass
// a probe backed by `cmux status --json`; tests pass a scripted bool
// stream.
func WithReadinessProbe(p ReadinessProbe) Option {
	return func(c *client) { c.probe = p }
}

// WithStartupTimeout overrides the 60s default. Tests squash this to a
// few milliseconds so timeout-path tests run in under a second.
func WithStartupTimeout(d time.Duration) Option {
	return func(c *client) { c.startupTimeout = d }
}

// WithPollInterval overrides the 500ms default poll cadence.
func WithPollInterval(d time.Duration) Option {
	return func(c *client) { c.pollInterval = d }
}

// WithClock injects a deterministic clock. Mirrors internal/store
// WithClock so timing-sensitive methods (WaitReady) can pin deadlines
// in tests.
func WithClock(now func() time.Time) Option {
	return func(c *client) { c.clock = now }
}

// WithFallbackBinary overrides the name of the ws-send wrapper used when
// `cmux send` fails. Defaults to "ws-send" per requirement.md step 2.f.
func WithFallbackBinary(name string) Option {
	return func(c *client) { c.fallbackBinary = name }
}

// NewClient returns a Client wired to ExecRunner / a never-ready probe by
// default. Callers in production can override the Runner and probe;
// tests override Runner + probe + timing.
func NewClient(opts ...Option) Client {
	c := &client{
		runner:         ExecRunner{},
		probe:          neverReadyProbe{},
		startupTimeout: defaultStartupTimeout,
		pollInterval:   defaultPollInterval,
		clock:          time.Now,
		fallbackBinary: "ws-send",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// workspacePattern matches a "workspace:NNN" token anywhere in cmux
// stdout. Using a regex (rather than strings.HasPrefix) tolerates cmux
// variants that prefix with "Created " or print other diagnostics on
// the same line.
var workspacePattern = regexp.MustCompile(`workspace:\d+`)

func (c *client) NewWorkspace(ctx context.Context, opts NewWorkspaceOptions) (Workspace, error) {
	if opts.CWD == "" || opts.Command == "" || opts.Name == "" {
		return Workspace{}, fmt.Errorf("%w: CWD=%q Command=%q Name=%q",
			ErrInvalidOptions, opts.CWD, opts.Command, opts.Name)
	}

	args := []string{
		"new-workspace",
		"--cwd", opts.CWD,
		"--command", opts.Command,
		"--name", opts.Name,
	}
	stdout, stderr, err := c.runner.Run(ctx, "cmux", args...)
	if err != nil {
		if isBinaryNotFound(err) {
			return Workspace{}, ErrCmuxNotFound
		}
		return Workspace{}, fmt.Errorf("cmux new-workspace: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}

	id := workspacePattern.FindString(string(stdout))
	if id == "" {
		return Workspace{}, fmt.Errorf("%w: stdout=%q", ErrUnparseableOutput, strings.TrimSpace(string(stdout)))
	}
	return Workspace{ID: id, Name: opts.Name}, nil
}

// newlineCollapser replaces any run of CR/LF with a single space.
// Documented behaviour: docs/requirement.md execution dispatcher step
// 2.e — a Claude prompt typed on multiple lines is sent as one logical
// line so cmux's input handler does not interpret intermediate Enters
// as premature submits.
var newlineCollapser = regexp.MustCompile(`[\r\n]+`)

func (c *client) Send(ctx context.Context, ws Workspace, text string) error {
	if ws.ID == "" {
		return ErrInvalidWorkspace
	}
	payload := newlineCollapser.ReplaceAllString(text, " ")

	_, stderr, err := c.runner.Run(ctx, "cmux", "send", ws.ID, payload)
	if err == nil {
		return nil
	}
	// A missing cmux binary is not something `ws-send` can rescue, so
	// surface the typed sentinel immediately rather than retrying.
	if isBinaryNotFound(err) {
		return ErrCmuxNotFound
	}
	// Primary failed for some other reason; try the Enter-appending
	// fallback before surfacing the error so a transient cmux send
	// glitch does not abort dispatch (requirement.md step 2.f).
	primaryErr := err
	primaryStderr := strings.TrimSpace(string(stderr))

	_, fbStderr, fbErr := c.runner.Run(ctx, c.fallbackBinary, ws.ID, payload)
	if fbErr == nil {
		return nil
	}
	return fmt.Errorf("cmux send failed (%v, stderr=%s); %s fallback also failed: %w (stderr=%s)",
		primaryErr, primaryStderr, c.fallbackBinary, fbErr, strings.TrimSpace(string(fbStderr)))
}

// WaitReady blocks until the readiness probe reports ws is ready, the
// startup timeout elapses (-> ErrTimeout), or the parent context is
// cancelled (-> ctx.Err()). It probes once before the first sleep so a
// workspace that came up during NewWorkspace's exec round-trip does not
// pay an extra pollInterval before being noticed.
func (c *client) WaitReady(ctx context.Context, ws Workspace) error {
	if ws.ID == "" {
		return ErrInvalidWorkspace
	}

	deadline := c.clock().Add(c.startupTimeout)
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	ready, err := c.probe.IsReady(ctx, ws)
	if err != nil {
		return fmt.Errorf("cmux readiness probe: %w", err)
	}
	if ready {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !c.clock().Before(deadline) {
				return ErrTimeout
			}
			ready, err := c.probe.IsReady(ctx, ws)
			if err != nil {
				return fmt.Errorf("cmux readiness probe: %w", err)
			}
			if ready {
				return nil
			}
		}
	}
}
