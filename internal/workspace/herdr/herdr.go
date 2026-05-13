// Package herdr is marunage's wrapper around the external `herdr` CLI
// (ogulcancelik/herdr — "tmux for agents"). It implements the Client
// interface defined in internal/workspace so the dispatcher can drive a
// herdr-managed Claude session per task without knowing it isn't cmux.
//
// The public surface mirrors internal/workspace/cmux: tests inject a
// fake Runner so the package never spawns a real herdr during `go
// test`, and functional options keep construction terse at call sites.
//
// Mapping to herdr CLI / socket API:
//
//   - NewWorkspace   → `herdr workspace create --cwd … --label …`
//     then `herdr pane run <pane_id> <command>`
//   - Send           → `herdr pane send-text <pane_id> <text>`
//     then `herdr pane send-keys <pane_id> Enter`
//     (mirrors the cmux paste-detection workaround)
//   - ListWorkspaces → `herdr pane list` (every pane is a separate
//     Claude session handle; marunage's Workspace.ID
//     stores the pane_id, not the workspace_id)
//   - ReadOutput     → `herdr pane read <pane_id> --source recent`
//   - WaitReady      → polls the injected ReadinessProbe until ready,
//     timeout, or ctx cancellation.
package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/workspace"
)

// Re-exports of backend-neutral types so call sites can write
// `herdr.Client`, `herdr.Workspace`, ... symmetrically with the cmux
// package even though the canonical definitions live in
// internal/workspace.
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

// Typed sentinel errors. Backend-neutral conditions reuse the
// workspace package's sentinels so callers that switch on `errors.Is`
// don't have to know which backend produced the error.
var (
	// ErrHerdrNotFound is returned when the herdr binary is missing
	// from PATH. doctor already checks this at startup; this sentinel
	// is the dispatcher's fallback for the race where herdr disappears
	// between `marunage doctor` and the first NewWorkspace call. It
	// wraps workspace.ErrBackendNotFound so callers that hold a Client
	// interface can errors.Is against the backend-neutral sentinel.
	ErrHerdrNotFound = fmt.Errorf("herdr: binary not found on PATH: %w", workspace.ErrBackendNotFound)

	ErrInvalidOptions    = workspace.ErrInvalidOptions
	ErrUnparseableOutput = workspace.ErrUnparseableOutput
	ErrInvalidWorkspace  = workspace.ErrInvalidWorkspace
	ErrTimeout           = workspace.ErrTimeout
)

// Defaults match docs/requirement.md execution dispatcher details. The
// 60s startup timeout is the requirement; the 500ms poll interval is
// the implementation choice — same balance as the cmux backend.
const (
	defaultStartupTimeout = 60 * time.Second
	defaultPollInterval   = 500 * time.Millisecond
)

type client struct {
	runner         workspace.Runner
	probe          ReadinessProbe
	startupTimeout time.Duration
	pollInterval   time.Duration
	clock          func() time.Time
}

// Option mutates client construction. Mirrors the cmux backend's
// functional-option shape so call sites read identically regardless of
// which backend they build.
type Option func(*client)

// WithRunner injects a Runner. Tests pass a fake; production code can
// also pass a wrapping Runner that adds tracing / retry / metrics.
func WithRunner(r workspace.Runner) Option { return func(c *client) { c.runner = r } }

// WithReadinessProbe injects the probe WaitReady polls.
func WithReadinessProbe(p ReadinessProbe) Option { return func(c *client) { c.probe = p } }

// WithStartupTimeout overrides the 60s default. Tests squash this to a
// few milliseconds so timeout-path tests run in under a second.
func WithStartupTimeout(d time.Duration) Option { return func(c *client) { c.startupTimeout = d } }

// WithPollInterval overrides the 500ms default poll cadence.
func WithPollInterval(d time.Duration) Option { return func(c *client) { c.pollInterval = d } }

// WithClock injects a deterministic clock used to compute the
// WaitReady deadline.
func WithClock(now func() time.Time) Option { return func(c *client) { c.clock = now } }

// NewClient returns a Client wired to ExecRunner / a never-ready probe
// by default. Callers in production can override the Runner and probe;
// tests override Runner + probe + timing.
func NewClient(opts ...Option) Client {
	c := &client{
		runner:         workspace.ExecRunner{},
		probe:          neverReadyProbe{},
		startupTimeout: defaultStartupTimeout,
		pollInterval:   defaultPollInterval,
		clock:          time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// workspaceCreateResp matches the JSON shape `herdr workspace create`
// prints to stdout. Only the fields we need are declared; herdr also
// returns result.workspace and result.tab which we ignore.
type workspaceCreateResp struct {
	Result struct {
		RootPane struct {
			PaneID      string `json:"pane_id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"root_pane"`
	} `json:"result"`
}

// paneListResp matches `herdr pane list` stdout JSON. We harvest just
// the pane_ids since marunage's Workspace.ID is the pane_id handle.
type paneListResp struct {
	Result struct {
		Panes []struct {
			PaneID string `json:"pane_id"`
		} `json:"panes"`
	} `json:"result"`
}

func (c *client) NewWorkspace(ctx context.Context, opts NewWorkspaceOptions) (Workspace, error) {
	if opts.CWD == "" || opts.Command == "" || opts.Name == "" {
		return Workspace{}, fmt.Errorf("%w: CWD=%q Command=%q Name=%q",
			ErrInvalidOptions, opts.CWD, opts.Command, opts.Name)
	}

	// herdr's `workspace create` opens a workspace but does NOT start
	// the user-supplied command — we have to follow up with `pane run`
	// against the returned root pane. Keeping focus off the new
	// workspace (--no-focus) avoids stealing the user's foreground
	// pane every time marunage dispatches a task.
	stdout, stderr, err := c.runner.Run(ctx, "herdr",
		"workspace", "create",
		"--cwd", opts.CWD,
		"--label", opts.Name,
		"--no-focus",
	)
	if err != nil {
		if workspace.IsBinaryNotFound(err) {
			return Workspace{}, ErrHerdrNotFound
		}
		return Workspace{}, fmt.Errorf("herdr workspace create: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}

	var resp workspaceCreateResp
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return Workspace{}, fmt.Errorf("%w: %v stdout=%q", ErrUnparseableOutput, err, strings.TrimSpace(string(stdout)))
	}
	paneID := resp.Result.RootPane.PaneID
	if paneID == "" {
		return Workspace{}, fmt.Errorf("%w: empty pane_id, stdout=%q", ErrUnparseableOutput, strings.TrimSpace(string(stdout)))
	}
	wsID := resp.Result.RootPane.WorkspaceID

	// `herdr pane run` is a convenience for send_input + Enter, the
	// shape we want for kicking off `claude ...` inside the new pane.
	// If it fails we tear the just-created workspace down so a partial
	// failure does not leak orphan workspaces back to the user. The
	// cleanup itself is best-effort — surfacing the original pane-run
	// failure is more useful than the cleanup outcome.
	_, runStderr, runErr := c.runner.Run(ctx, "herdr", "pane", "run", paneID, opts.Command)
	if runErr != nil {
		c.bestEffortCloseWorkspace(ctx, wsID)
		if workspace.IsBinaryNotFound(runErr) {
			return Workspace{}, ErrHerdrNotFound
		}
		return Workspace{}, fmt.Errorf("herdr pane run: %w (stderr=%s)", runErr, strings.TrimSpace(string(runStderr)))
	}

	return Workspace{ID: paneID, Name: opts.Name}, nil
}

// bestEffortCloseWorkspace attempts to tear down a half-created
// workspace after NewWorkspace's `pane run` step fails. Any error is
// swallowed because the caller already has the more informative
// pane-run failure to return; logging would only get in the way of the
// dispatcher's structured audit trail.
func (c *client) bestEffortCloseWorkspace(ctx context.Context, wsID string) {
	if wsID == "" {
		return
	}
	_, _, _ = c.runner.Run(ctx, "herdr", "workspace", "close", wsID)
}

func (c *client) Send(ctx context.Context, ws Workspace, text string) error {
	if ws.ID == "" {
		return ErrInvalidWorkspace
	}

	// Claude's paste-detection intercepts large text blocks and waits
	// for an explicit Enter before submitting. Mirror the cmux
	// backend's defensive pattern: send the text first, then Enter as
	// a separate keystroke so the prompt is submitted regardless of
	// whether paste-mode fires.
	_, stderr, err := c.runner.Run(ctx, "herdr", "pane", "send-text", ws.ID, text)
	if err != nil {
		if workspace.IsBinaryNotFound(err) {
			return ErrHerdrNotFound
		}
		return fmt.Errorf("herdr pane send-text: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}

	if _, keyStderr, keyErr := c.runner.Run(ctx, "herdr", "pane", "send-keys", ws.ID, "Enter"); keyErr != nil {
		if workspace.IsBinaryNotFound(keyErr) {
			return ErrHerdrNotFound
		}
		return fmt.Errorf("herdr pane send-keys Enter: %w (stderr=%s)", keyErr, strings.TrimSpace(string(keyStderr)))
	}
	return nil
}

// ListWorkspaces returns every herdr pane the daemon currently
// considers live. Each pane is treated as one logical workspace
// because marunage stores pane_ids (not workspace_ids) as the per-task
// handle. The reaper diffs this against tasks.ws to detect rows whose
// pane has disappeared.
func (c *client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	stdout, stderr, err := c.runner.Run(ctx, "herdr", "pane", "list")
	if err != nil {
		if workspace.IsBinaryNotFound(err) {
			return nil, ErrHerdrNotFound
		}
		return nil, fmt.Errorf("herdr pane list: %w (stderr=%s)",
			err, strings.TrimSpace(string(stderr)))
	}
	var resp paneListResp
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return nil, fmt.Errorf("herdr pane list: %w (stdout=%q)", err, strings.TrimSpace(string(stdout)))
	}
	out := make([]Workspace, 0, len(resp.Result.Panes))
	for _, p := range resp.Result.Panes {
		// An empty pane_id would short-circuit reaper into treating
		// the row as a wildcard live ID; fail loud instead so a future
		// herdr release that reshapes its JSON banner surfaces here
		// rather than silently corrupting the orphan diff.
		if p.PaneID == "" {
			return nil, fmt.Errorf("%w: pane_list entry missing pane_id, stdout=%q",
				ErrUnparseableOutput, strings.TrimSpace(string(stdout)))
		}
		out = append(out, Workspace{ID: p.PaneID})
	}
	return out, nil
}

// ReadOutput captures the recent (scrollback) terminal content of the
// pane identified by ws.ID. `herdr pane read` prints text (not JSON)
// per the documented CLI behaviour, so we just trim and return.
func (c *client) ReadOutput(ctx context.Context, ws Workspace) (string, error) {
	if ws.ID == "" {
		return "", ErrInvalidWorkspace
	}
	stdout, stderr, err := c.runner.Run(ctx, "herdr", "pane", "read", ws.ID,
		"--source", "recent",
		"--lines", "1000",
	)
	if err != nil {
		if workspace.IsBinaryNotFound(err) {
			return "", ErrHerdrNotFound
		}
		return "", fmt.Errorf("herdr pane read: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// WaitReady blocks until the readiness probe reports ws is ready, the
// startup timeout elapses (-> ErrTimeout), or the parent context is
// cancelled (-> ctx.Err()). It probes once before the first sleep so
// a pane that came up during NewWorkspace's exec round-trip does not
// pay an extra pollInterval before being noticed.
func (c *client) WaitReady(ctx context.Context, ws Workspace) error {
	if ws.ID == "" {
		return ErrInvalidWorkspace
	}

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
		return fmt.Errorf("herdr readiness probe: %w", err)
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
				return fmt.Errorf("herdr readiness probe: %w", err)
			}
			if ready {
				return nil
			}
		}
	}
}
