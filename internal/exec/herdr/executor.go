// Package herdr adapts the herdr CLI (https://herdr.dev,
// github.com/ogulcancelik/herdr — "tmux for agents") to marunage's
// backend-agnostic exec.Executor contract. It is the third terminal-
// multiplexer backend after cmux and tmux and is selected with
// [execution].executor = "herdr".
//
// The Executor implements the core exec.Executor plus the same capability
// set as cmux/tmux — Attachable (a `herdr pane focus` command), Streamable +
// OutputReader (`herdr pane read`), and Lister (live panes for the reaper).
// It is driven purely through the herdr CLI, sharing nothing with the other
// backends except the public exec interfaces and the shared sentinel
// completion mechanism (exec.AwaitSentinel).
//
// Unlike cmux — which requires marunage itself to run inside a cmux
// terminal — herdr's control socket is reachable from any external process,
// so a marunage daemon launched outside the multiplexer can still create,
// drive, and reap herdr panes.
//
// herdr's exact CLI surface and JSON shapes are still evolving upstream, so
// every external command is funnelled through the Runner interface and the
// JSON is parsed by a deliberately layout-tolerant pane-id harvester
// (collectPaneIDs). When herdr's output shifts, the blast radius is this one
// file rather than the whole backend.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/exec"
)

// Handle is the backend-internal value carried in exec.Session.handle for
// herdr panes. SentinelDir is the marunage control directory whose atomic
// .exit_code file AwaitExit polls; like cmux/tmux it is empty for sessions
// created by Start today — wiring it through is the pipeline PR's (R05) job.
type Handle struct {
	SentinelDir string
}

// Executor drives herdr through a Runner and satisfies exec.Executor + the
// herdr capability set. Construct it with New.
type Executor struct {
	runner         Runner
	pollInterval   time.Duration
	startupTimeout time.Duration
	awaitTimeout   time.Duration
	readLines      int
}

// Option mutates Executor construction, mirroring the functional-option
// shape used across marunage.
type Option func(*Executor)

// WithRunner injects the Runner. Tests pass a fake; production callers use
// the default ExecRunner.
func WithRunner(r Runner) Option { return func(e *Executor) { e.runner = r } }

// WithPollInterval overrides how often Start readiness / Stream / AwaitExit
// poll herdr. Tests squash this to a few milliseconds.
func WithPollInterval(d time.Duration) Option { return func(e *Executor) { e.pollInterval = d } }

// WithStartupTimeout caps how long Start waits for the pane to become ready
// (Claude's banner appears) before returning ErrStartupTimeout.
func WithStartupTimeout(d time.Duration) Option { return func(e *Executor) { e.startupTimeout = d } }

// WithAwaitTimeout caps how long AwaitExit waits for the exit sentinel before
// returning exec.ErrAwaitTimeout. Zero means "no cap".
func WithAwaitTimeout(d time.Duration) Option { return func(e *Executor) { e.awaitTimeout = d } }

// WithReadLines overrides how many scrollback lines `herdr pane read` is
// asked for when reading output. Defaults to defaultReadLines.
func WithReadLines(n int) Option { return func(e *Executor) { e.readLines = n } }

const (
	defaultPollInterval   = 500 * time.Millisecond
	defaultStartupTimeout = 60 * time.Second
	defaultReadLines      = 1000
)

// ErrHerdrNotFound is returned when the herdr binary is missing from PATH.
var ErrHerdrNotFound = errors.New("exec/herdr: herdr binary not found on PATH")

// ErrStartupTimeout is returned by Start when the pane is created but Claude
// never becomes ready before WithStartupTimeout elapses. Distinct from
// context cancellation so a caller can tell "herdr is slow" from "we were
// asked to stop".
var ErrStartupTimeout = errors.New("exec/herdr: pane did not become ready before timeout")

// ErrInvalidSession is returned when a method is handed a Session with an
// empty ID — a sign a previous Start failed and its zero Session was reused.
var ErrInvalidSession = errors.New("exec/herdr: session id is empty")

// ErrUnparseableOutput is returned when a herdr JSON response cannot be
// parsed, or parses but carries no usable pane id (e.g. an empty pane_id),
// so the caller does not proceed against a phantom pane.
var ErrUnparseableOutput = errors.New("exec/herdr: could not parse pane id from herdr output")

// New builds an Executor wired to the production ExecRunner and the default
// cadence/timeout knobs, then applies opts.
func New(opts ...Option) *Executor {
	e := &Executor{
		runner:         ExecRunner{},
		pollInterval:   defaultPollInterval,
		startupTimeout: defaultStartupTimeout,
		readLines:      defaultReadLines,
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.pollInterval <= 0 {
		e.pollInterval = defaultPollInterval
	}
	if e.startupTimeout <= 0 {
		e.startupTimeout = defaultStartupTimeout
	}
	if e.readLines <= 0 {
		e.readLines = defaultReadLines
	}
	return e
}

// Start creates a herdr workspace in spec.Cwd, launches spec.Command in its
// root pane, then waits for Claude to become ready. Per the exec.Executor
// contract it returns a Session whose ID (the pane id) is populated even when
// readiness fails — so the dispatcher preserves the reference and fails the
// row rather than leaking a second pane on retry — and the zero Session when
// nothing usable was left behind.
func (e *Executor) Start(ctx context.Context, spec exec.SessionSpec) (exec.Session, error) {
	args := []string{"workspace", "create", "--no-focus"}
	if spec.Cwd != "" {
		args = append(args, "--cwd", spec.Cwd)
	}
	if label := strings.TrimSpace(spec.Name); label != "" {
		args = append(args, "--label", label)
	}

	stdout, stderr, err := e.runner.Run(ctx, "herdr", args...)
	if err != nil {
		if isBinaryNotFound(err) {
			return exec.Session{}, ErrHerdrNotFound
		}
		// Nothing was created: zero Session signals "retryable" upstream.
		return exec.Session{}, fmt.Errorf("herdr workspace create: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}

	paneID, err := firstPaneID(stdout)
	if err != nil {
		// herdr accepted the create but we cannot address the pane; treat it
		// as "nothing usable created" and let the caller retry.
		return exec.Session{}, err
	}

	session := exec.NewSession(paneID, Handle{})

	if _, stderr, err := e.runner.Run(ctx, "herdr", "pane", "run", paneID, spec.Command); err != nil {
		// The workspace exists but Claude never launched. Best-effort close it
		// so a half-built workspace does not leak, then report "nothing usable
		// left" with the zero Session so the caller retries cleanly.
		e.closeWorkspace(ctx, paneID)
		if isBinaryNotFound(err) {
			return exec.Session{}, ErrHerdrNotFound
		}
		return exec.Session{}, fmt.Errorf("herdr pane run: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}

	if err := e.waitReady(ctx, session); err != nil {
		// Created but not ready: return the populated Session so the caller
		// keeps the reference and fails the row instead of retrying.
		return session, err
	}
	return session, nil
}

// readyBanner is Claude's startup banner; the "❯" prompt alone is not enough
// because the shell prints "❯ claude" before Claude itself boots.
const readyBanner = "Claude Code v"

// waitReady polls `herdr pane read` until Claude's banner is visible, the
// startup timeout elapses (-> ErrStartupTimeout), or ctx is cancelled. A
// fatal read failure (the pane vanished, the server died) is propagated
// immediately; a transient read failure is suppressed so a pane that is not
// yet emitting output does not abort the boot. It probes once before the
// first sleep so a fast boot is noticed without paying a pollInterval.
func (e *Executor) waitReady(ctx context.Context, s exec.Session) error {
	deadline := time.Now().Add(e.startupTimeout)
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()
	for {
		stdout, stderr, err := e.readPaneRaw(ctx, s)
		switch {
		case err == nil:
			out := string(stdout)
			if strings.Contains(out, readyBanner) && strings.Contains(out, "❯") {
				return nil
			}
		case isBinaryNotFound(err):
			return ErrHerdrNotFound
		case isFatalProbe(stderr):
			// The pane is gone / the server is down: no amount of polling will
			// recover, so surface it now with the populated session preserved.
			return fmt.Errorf("herdr pane read: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
		default:
			// Transient read failure (pane not emitting yet): keep polling.
		}
		if !time.Now().Before(deadline) {
			return ErrStartupTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// newlineCollapser replaces any run of CR/LF with a single space so a multi-
// line prompt is submitted as one logical line — herdr's send-keys Enter
// would otherwise treat an embedded newline as a premature submit (the same
// paste hazard the cmux/tmux backends fold around).
var newlineCollapser = regexp.MustCompile(`[\r\n]+`)

// Send types prompt into the pane and then submits it with a separate Enter
// keystroke. The two-step `pane send-text` + `pane send-keys Enter` mirrors
// cmux/tmux: Claude's paste-detection waits for an explicit Enter before
// submitting a large block, so the text and the submit must be separate.
func (e *Executor) Send(ctx context.Context, s exec.Session, prompt string) error {
	if s.ID == "" {
		return ErrInvalidSession
	}
	collapsed := newlineCollapser.ReplaceAllString(prompt, " ")
	if _, stderr, err := e.runner.Run(ctx, "herdr", "pane", "send-text", s.ID, collapsed); err != nil {
		return e.sendErr("pane send-text", stderr, err)
	}
	if _, stderr, err := e.runner.Run(ctx, "herdr", "pane", "send-keys", s.ID, "Enter"); err != nil {
		return e.sendErr("pane send-keys Enter", stderr, err)
	}
	return nil
}

func (e *Executor) sendErr(what string, stderr []byte, err error) error {
	if isBinaryNotFound(err) {
		return ErrHerdrNotFound
	}
	return fmt.Errorf("herdr %s: %w (stderr=%s)", what, err, strings.TrimSpace(string(stderr)))
}

// Attach returns the command a human runs to surface the pane. herdr has no
// URI scheme, so like tmux's `tmux attach` this is the runnable
// `herdr pane focus <id>` command string (§4.2 table: herdr = pane focus). A
// consumer that renders the Attachable result must treat it as opaque text,
// not assume a clickable URI.
func (e *Executor) Attach(_ context.Context, s exec.Session) (string, error) {
	if s.ID == "" {
		return "", ErrInvalidSession
	}
	return "herdr pane focus " + s.ID, nil
}

// ReadOutput returns the current visible pane text for s, trimmed.
func (e *Executor) ReadOutput(ctx context.Context, s exec.Session) (string, error) {
	if s.ID == "" {
		return "", ErrInvalidSession
	}
	stdout, stderr, err := e.readPaneRaw(ctx, s)
	if err != nil {
		if isBinaryNotFound(err) {
			return "", ErrHerdrNotFound
		}
		return "", fmt.Errorf("herdr pane read: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// readPaneRaw runs `herdr pane read <id> --source recent --lines N` and
// returns its raw stdout/stderr/err. `pane read` returns text, not JSON, so
// callers trim and use it directly. `recent` includes scrollback so a banner
// that has scrolled off the viewport is still seen.
func (e *Executor) readPaneRaw(ctx context.Context, s exec.Session) ([]byte, []byte, error) {
	return e.runner.Run(ctx, "herdr", "pane", "read", s.ID, "--source", "recent", "--lines", strconv.Itoa(e.readLines))
}

// closeWorkspace best-effort closes the workspace owning paneID. herdr pane
// ids look like "1-1" (workspace 1, pane 1), so the workspace ref is the
// prefix before the first "-". Errors are ignored: this only runs on a
// cleanup path where the caller is already returning a failure.
func (e *Executor) closeWorkspace(ctx context.Context, paneID string) {
	ws := workspaceRef(paneID)
	if ws == "" {
		return
	}
	_, _, _ = e.runner.Run(ctx, "herdr", "workspace", "close", ws)
}

// workspaceRef extracts the workspace id from a herdr pane id. herdr documents
// pane ids as "<workspace>-<pane>" (e.g. "1-1", "2-3"), so the workspace ref
// is everything before the first "-". Returns "" when paneID carries no "-".
func workspaceRef(paneID string) string {
	if i := strings.IndexByte(paneID, '-'); i > 0 {
		return paneID[:i]
	}
	return ""
}

// fatalProbeMarkers are the substrings in herdr's stderr that mean a read
// failure is permanent (the pane vanished, the server is down) rather than a
// transient hiccup worth retrying during readiness polling.
var fatalProbeMarkers = []string{
	"pane_not_found",
	"pane not found",
	"server not running",
	"connection refused",
}

func isFatalProbe(stderr []byte) bool {
	s := strings.ToLower(string(stderr))
	for _, m := range fatalProbeMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// ListSessions enumerates the panes herdr currently considers live by parsing
// `herdr pane list`. When the command errors (no herdr server running) it is
// reported as an empty live set rather than an error, so the reaper does not
// mistake "nothing dispatched yet" for a backend failure (matching tmux).
func (e *Executor) ListSessions(ctx context.Context) ([]exec.Session, error) {
	stdout, _, err := e.runner.Run(ctx, "herdr", "pane", "list")
	if err != nil {
		if isBinaryNotFound(err) {
			return nil, ErrHerdrNotFound
		}
		// A non-zero exit here means "no server / no panes" — an empty live
		// set, not a failure the reaper should act on.
		return []exec.Session{}, nil
	}
	ids, err := collectPaneIDs(stdout)
	if err != nil {
		return nil, err
	}
	out := make([]exec.Session, 0, len(ids))
	for _, id := range ids {
		out = append(out, exec.NewSession(id, Handle{}))
	}
	return out, nil
}

// Stream polls the pane's visible output and emits a chunk on the returned
// channel whenever the text changes. The channel closes when ctx is cancelled
// or a read fails (pane gone). The first non-empty read is emitted so a
// subscriber sees the current state immediately. This mirrors the cmux/tmux
// Stream loop exactly — all three are pure poll-and-diff over ReadOutput.
func (e *Executor) Stream(ctx context.Context, s exec.Session) (<-chan []byte, error) {
	if s.ID == "" {
		return nil, ErrInvalidSession
	}
	ch := make(chan []byte)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(e.pollInterval)
		defer ticker.Stop()
		var last string
		for {
			out, err := e.ReadOutput(ctx, s)
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

// AwaitExit blocks until the session's .exit_code sentinel appears, ctx is
// cancelled, or WithAwaitTimeout elapses. The completion mechanism is the
// same atomic sentinel cmux/tmux use (docs §4.3); the shared
// exec.AwaitSentinel owns the hardened polling so every backend agrees
// byte-for-byte.
func (e *Executor) AwaitExit(ctx context.Context, s exec.Session) (int, error) {
	return exec.AwaitSentinel(ctx, e.sentinelDir(s), e.pollInterval, e.awaitTimeout)
}

func (e *Executor) sentinelDir(s exec.Session) string {
	if h, ok := s.Handle().(Handle); ok {
		return h.SentinelDir
	}
	return ""
}

// firstPaneID parses a `herdr workspace create` JSON response and returns the
// id of the (single) pane it created. herdr documents the root pane at
// result.root_pane.pane_id; collectPaneIDs is layout-tolerant so a future
// reshuffle of the response still resolves the pane id.
func firstPaneID(data []byte) (string, error) {
	ids, err := collectPaneIDs(data)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("%w: no pane_id in workspace create response", ErrUnparseableOutput)
	}
	return ids[0], nil
}

// collectPaneIDs walks an arbitrary herdr JSON response and returns every
// "pane_id" string value it finds, sorted for deterministic output. herdr's
// exact response layout is unstable across versions, so this recurses rather
// than binding to one fixed shape (result.root_pane.pane_id /
// result.panes[].pane_id / ...). A present-but-empty pane_id is treated as
// corruption and promoted to ErrUnparseableOutput so a phantom pane never
// reaches a caller. A response that simply contains no panes returns an empty
// slice (no error) — the reaper's "nothing live" case.
func collectPaneIDs(data []byte) ([]string, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnparseableOutput, err)
	}
	var ids []string
	var sawEmpty bool
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			for k, v := range t {
				if k == "pane_id" {
					if s, ok := v.(string); ok {
						if s == "" {
							sawEmpty = true
						} else {
							ids = append(ids, s)
						}
					}
				}
				walk(v)
			}
		case []any:
			for _, v := range t {
				walk(v)
			}
		}
	}
	walk(root)
	if sawEmpty {
		return nil, fmt.Errorf("%w: empty pane_id in herdr response", ErrUnparseableOutput)
	}
	sort.Strings(ids)
	return ids, nil
}

// Compile-time proof the Executor honours every interface it advertises.
var (
	_ exec.Executor     = (*Executor)(nil)
	_ exec.Attachable   = (*Executor)(nil)
	_ exec.Streamable   = (*Executor)(nil)
	_ exec.OutputReader = (*Executor)(nil)
	_ exec.Lister       = (*Executor)(nil)
)
