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

// NewClient returns a Client wired to ExecRunner by default. Callers in
// production can override the Runner; tests override Runner + timing.
func NewClient(opts ...Option) Client {
	c := &client{
		runner:         ExecRunner{},
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
