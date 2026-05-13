// Package cmux is marunage's wrapper around the external `cmux` CLI. It
// owns the operations the dispatcher needs to drive one Claude session
// per task. The Client interface is defined in internal/workspace so a
// second backend (herdr) can plug in alongside this one; the cmux
// package implements that interface with an exec.Command-backed Runner.
// Tests inject a fake Runner so the package never spawns a real cmux
// during `go test`.
package cmux

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/workspace"
)

// Re-exports of backend-neutral types. Existing callers that import
// this package keep writing `cmux.Client`, `cmux.Workspace`, ...
// unchanged — but those names now refer to the canonical definitions
// in the workspace package so the herdr backend can satisfy the same
// Client interface.
type (
	Client              = workspace.Client
	ReadinessProbe      = workspace.ReadinessProbe
	ReadinessProbeFunc  = workspace.ReadinessProbeFunc
	Workspace           = workspace.Workspace
	NewWorkspaceOptions = workspace.NewWorkspaceOptions
)

// neverReadyProbe is the default. Calling WaitReady on a freshly-built
// Client always exhausts the timeout — which is the right answer: a
// caller that forgot to inject a probe must not silently treat every
// workspace as ready.
type neverReadyProbe struct{}

func (neverReadyProbe) IsReady(_ context.Context, _ Workspace) (bool, error) {
	return false, nil
}

// Typed sentinel errors. Callers in dispatch and the CLI layer (doctor
// surfaces ErrCmuxNotFound) match on these via errors.Is rather than
// substring-checking the wrapped messages.
var (
	// ErrCmuxNotFound is returned when the cmux binary is missing from
	// PATH. doctor already checks this at startup; this sentinel is
	// the dispatcher's fallback for the race where cmux disappears
	// between `marunage doctor` and the first NewWorkspace call. It
	// wraps workspace.ErrBackendNotFound so callers that hold a
	// Client interface can errors.Is against the backend-neutral
	// sentinel.
	ErrCmuxNotFound = fmt.Errorf("cmux: binary not found on PATH: %w", workspace.ErrBackendNotFound)

	// The following are aliased to backend-neutral sentinels so cmux
	// and herdr return the same error values for the same conditions.
	ErrInvalidOptions    = workspace.ErrInvalidOptions
	ErrUnparseableOutput = workspace.ErrUnparseableOutput
	ErrInvalidWorkspace  = workspace.ErrInvalidWorkspace
	ErrTimeout           = workspace.ErrTimeout
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

// WithClock injects a deterministic clock used to compute the WaitReady
// deadline. Note that the poll cadence still relies on time.NewTicker
// (real wall clock), so this option pins the timeout decision but does
// not eliminate sleep-based test timing for the polling loop itself.
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

// listWorkspacePattern matches a "workspace:NNN" id at the start of a
// line (after optional indent / dashboard markers like "* "). The
// leading anchor keeps a task title that happens to contain
// "workspace:99" out of the alive set so reaper does not falsely treat
// such a row as live and leak an orphan past the diff.
var listWorkspacePattern = regexp.MustCompile(`(?m)^[\s*]*(workspace:\d+)`)

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
		if workspace.IsBinaryNotFound(err) {
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
	// Collapse real newlines to spaces (requirement.md step 2.e).
	collapsed := newlineCollapser.ReplaceAllString(text, " ")

	_, stderr, err := c.runner.Run(ctx, "cmux", "send", "--workspace", ws.ID, collapsed)
	if err == nil {
		// Claude's paste-detection intercepts large text blocks and waits
		// for an explicit Enter before submitting. Send it separately so
		// the prompt is submitted regardless of whether paste-mode fires.
		if _, _, keyErr := c.runner.Run(ctx, "cmux", "send-key", "--workspace", ws.ID, "enter"); keyErr != nil {
			return fmt.Errorf("send-key enter: %w", keyErr)
		}
		return nil
	}
	// A missing cmux binary is not something `ws-send` can rescue, so
	// surface the typed sentinel immediately rather than retrying.
	if workspace.IsBinaryNotFound(err) {
		return ErrCmuxNotFound
	}
	// Primary failed for some other reason; try the Enter-appending
	// fallback (ws-send is an Enter-appending wrapper per requirement.md
	// step 2.f).
	primaryErr := err
	primaryStderr := strings.TrimSpace(string(stderr))

	// ws-send appends Enter itself (requirement.md step 2.f); no send-key needed.
	_, fbStderr, fbErr := c.runner.Run(ctx, c.fallbackBinary, ws.ID, collapsed)
	if fbErr == nil {
		return nil
	}
	return fmt.Errorf("cmux send failed (%v, stderr=%s); %s fallback also failed: %w (stderr=%s)",
		primaryErr, primaryStderr, c.fallbackBinary, fbErr, strings.TrimSpace(string(fbStderr)))
}

// ListWorkspaces shells out to `cmux list-workspaces` and harvests every
// line-leading "workspace:NNN" token. Returns a non-nil empty slice for
// empty stdout so callers can range without a nil check.
//
// A missing cmux binary surfaces as ErrCmuxNotFound (errors.Is-matchable)
// so PR-32 doctor and PR-44 reaper can branch on the typed sentinel
// rather than substring-checking the wrapped diagnostic. A non-zero exit
// is wrapped with stderr in the message so the operator can see why cmux
// refused without re-running by hand.
func (c *client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	stdout, stderr, err := c.runner.Run(ctx, "cmux", "list-workspaces")
	if err != nil {
		if workspace.IsBinaryNotFound(err) {
			return nil, ErrCmuxNotFound
		}
		return nil, fmt.Errorf("cmux list-workspaces: %w (stderr=%s)",
			err, strings.TrimSpace(string(stderr)))
	}
	matches := listWorkspacePattern.FindAllStringSubmatch(string(stdout), -1)
	out := make([]Workspace, 0, len(matches))
	for _, m := range matches {
		out = append(out, Workspace{ID: m[1]})
	}
	return out, nil
}

// ReadOutput shells out to `cmux read-screen --workspace <ws.ID>` and returns
// the trimmed stdout. Returns ErrInvalidWorkspace for an empty ID so callers
// cannot accidentally read pane text for an unset workspace.
func (c *client) ReadOutput(ctx context.Context, ws Workspace) (string, error) {
	if ws.ID == "" {
		return "", ErrInvalidWorkspace
	}
	stdout, stderr, err := c.runner.Run(ctx, "cmux", "read-screen", "--workspace", ws.ID)
	if err != nil {
		if workspace.IsBinaryNotFound(err) {
			return "", ErrCmuxNotFound
		}
		return "", fmt.Errorf("cmux read-screen: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
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

	// Defend against a misconfigured Option (e.g. WithPollInterval(0)) by
	// falling back to the package defaults: time.NewTicker(0) would panic
	// otherwise, taking the entire dispatcher down with it.
	startupTimeout := c.startupTimeout
	if startupTimeout <= 0 {
		startupTimeout = defaultStartupTimeout
	}
	pollInterval := c.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}

	deadline := c.clock().Add(startupTimeout)
	ticker := time.NewTicker(pollInterval)
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
