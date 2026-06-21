// Package tmux adapts the system tmux CLI to marunage's backend-agnostic
// exec.Executor contract. It is the proof that the Executor abstraction is
// genuinely cmux-independent (docs/redesign_layering.md §8 PR-R07): tmux is
// driven purely through `tmux new-session / send-keys / capture-pane /
// list-sessions`, sharing nothing with the cmux backend except the public
// exec interfaces and the shared sentinel completion mechanism.
//
// The Executor implements the core exec.Executor plus the same capability
// set as cmux — Attachable (a `tmux attach` command), Streamable +
// OutputReader (capture-pane), and Lister (live sessions for the reaper).
package tmux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/exec"
)

// Handle is the backend-internal value carried in exec.Session.handle for
// tmux sessions. SentinelDir is the marunage control directory whose atomic
// .exit_code file AwaitExit polls; like cmux it is empty for sessions
// created by Start today — wiring it through is the pipeline PR's (R05) job.
type Handle struct {
	SentinelDir string
}

// Executor drives tmux through a Runner and satisfies exec.Executor + the
// tmux capability set. Construct it with New.
type Executor struct {
	runner         Runner
	pollInterval   time.Duration
	startupTimeout time.Duration
	awaitTimeout   time.Duration
}

// Option mutates Executor construction, mirroring the functional-option
// shape used across marunage.
type Option func(*Executor)

// WithRunner injects the Runner. Tests pass a fake; production callers use
// the default ExecRunner.
func WithRunner(r Runner) Option { return func(e *Executor) { e.runner = r } }

// WithPollInterval overrides how often Start readiness / Stream / AwaitExit
// poll tmux. Tests squash this to a few milliseconds.
func WithPollInterval(d time.Duration) Option { return func(e *Executor) { e.pollInterval = d } }

// WithStartupTimeout caps how long Start waits for the session to become
// ready (Claude's prompt appears) before returning ErrStartupTimeout.
func WithStartupTimeout(d time.Duration) Option { return func(e *Executor) { e.startupTimeout = d } }

// WithAwaitTimeout caps how long AwaitExit waits for the exit sentinel
// before returning exec.ErrAwaitTimeout. Zero means "no cap".
func WithAwaitTimeout(d time.Duration) Option { return func(e *Executor) { e.awaitTimeout = d } }

const (
	defaultPollInterval   = 500 * time.Millisecond
	defaultStartupTimeout = 60 * time.Second
)

// ErrTmuxNotFound is returned when the tmux binary is missing from PATH.
var ErrTmuxNotFound = errors.New("exec/tmux: tmux binary not found on PATH")

// ErrStartupTimeout is returned by Start when the session is created but
// Claude never becomes ready before WithStartupTimeout elapses. Distinct
// from context cancellation so a caller can tell "tmux is slow" from "we
// were asked to stop".
var ErrStartupTimeout = errors.New("exec/tmux: session did not become ready before timeout")

// ErrInvalidSession is returned when a method is handed a Session with an
// empty ID — a sign a previous Start failed and its zero Session was reused.
var ErrInvalidSession = errors.New("exec/tmux: session id is empty")

// ErrInvalidEnvKey is returned by Start when spec.Env carries a key that is
// not a valid shell identifier. Rejecting it keeps a malformed key from
// smuggling an extra `-e`/flag token into the tmux command line.
var ErrInvalidEnvKey = errors.New("exec/tmux: invalid environment variable name")

// envKeyPattern is the POSIX shell-identifier shape every forwarded env key
// must match before it reaches `tmux new-session -e`.
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validEnvKey(k string) bool { return envKeyPattern.MatchString(k) }

// New builds an Executor wired to the production ExecRunner and the default
// cadence/timeout knobs, then applies opts.
func New(opts ...Option) *Executor {
	e := &Executor{
		runner:         ExecRunner{},
		pollInterval:   defaultPollInterval,
		startupTimeout: defaultStartupTimeout,
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
	return e
}

// Start launches a detached tmux session in spec.Cwd running spec.Command,
// then waits for Claude to become ready. Per the exec.Executor contract it
// returns a Session whose ID is populated even when readiness fails (so the
// dispatcher preserves the session reference and marks the row failed rather
// than leaking a second session on retry), and the zero Session when nothing
// was created.
func (e *Executor) Start(ctx context.Context, spec exec.SessionSpec) (exec.Session, error) {
	name := sessionName(spec.Name)
	args := []string{"new-session", "-d", "-s", name}
	if spec.Cwd != "" {
		args = append(args, "-c", spec.Cwd)
	}
	// tmux honours one -e KEY=VALUE per extra environment entry (tmux ≥ 3.0).
	// Sorted so the emitted command is deterministic for tests and logs. This
	// is a capability cmux lacks — proof the abstraction does not flatten a
	// backend down to cmux's feature set. Keys are validated so a malformed
	// entry cannot smuggle an extra `-e`/flag arg into the command.
	for _, k := range sortedKeys(spec.Env) {
		if !validEnvKey(k) {
			return exec.Session{}, fmt.Errorf("%w: %q", ErrInvalidEnvKey, k)
		}
		args = append(args, "-e", k+"="+spec.Env[k])
	}
	// spec.Command and spec.Env values must be trusted-internal: tmux runs the
	// command through the user's shell and exports the env into the session.
	// They are passed as os/exec args (no shell at the spawn layer), so this is
	// the dispatcher's contract, not an injection sink here.
	args = append(args, spec.Command)

	_, stderr, err := e.runner.Run(ctx, "tmux", args...)
	if err != nil {
		if isBinaryNotFound(err) {
			return exec.Session{}, ErrTmuxNotFound
		}
		// Nothing was created: zero Session signals "retryable" upstream.
		return exec.Session{}, fmt.Errorf("tmux new-session: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}

	// tmux creates the session under exactly the -s name (or fails the run on a
	// duplicate, handled above), so the requested name is the authoritative id.
	session := exec.NewSession(name, Handle{})

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

// waitReady polls capture-pane until Claude's prompt is visible, the startup
// timeout elapses (-> ErrStartupTimeout), or ctx is cancelled. It probes
// once before the first sleep so a fast boot is noticed without paying a
// pollInterval.
func (e *Executor) waitReady(ctx context.Context, s exec.Session) error {
	deadline := time.Now().Add(e.startupTimeout)
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()
	for {
		out, err := e.capturePane(ctx, s)
		if err == nil && strings.Contains(out, readyBanner) && strings.Contains(out, "❯") {
			return nil
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

// newlineCollapser replaces any run of CR/LF with a single space so a
// multi-line prompt is submitted as one logical line — tmux send-keys would
// otherwise treat an embedded newline as a premature Enter (the same paste
// hazard the cmux backend folds around).
var newlineCollapser = regexp.MustCompile(`[\r\n]+`)

// Send types prompt into the session and then submits it with a separate
// Enter keystroke. The text is sent with -l (literal) so tmux does not
// interpret words like "Enter" inside the prompt as key names.
func (e *Executor) Send(ctx context.Context, s exec.Session, prompt string) error {
	if s.ID == "" {
		return ErrInvalidSession
	}
	collapsed := newlineCollapser.ReplaceAllString(prompt, " ")
	if _, stderr, err := e.runner.Run(ctx, "tmux", "send-keys", "-t", s.ID, "-l", collapsed); err != nil {
		return e.sendErr("send-keys", stderr, err)
	}
	// Submit separately: Claude's paste-detection waits for an explicit Enter
	// before submitting a large block, so the keystroke must not be literal.
	if _, stderr, err := e.runner.Run(ctx, "tmux", "send-keys", "-t", s.ID, "Enter"); err != nil {
		return e.sendErr("send-keys Enter", stderr, err)
	}
	return nil
}

func (e *Executor) sendErr(what string, stderr []byte, err error) error {
	if isBinaryNotFound(err) {
		return ErrTmuxNotFound
	}
	return fmt.Errorf("tmux %s: %w (stderr=%s)", what, err, strings.TrimSpace(string(stderr)))
}

// Attach returns the command a human runs to take over the session. tmux has
// no URI scheme, so unlike cmux's deeplink this is the runnable
// `tmux attach -t <id>` command string (§4.2 table: tmux = pane attach). A
// consumer that renders the Attachable result must treat it as opaque text,
// not assume a clickable URI.
func (e *Executor) Attach(_ context.Context, s exec.Session) (string, error) {
	if s.ID == "" {
		return "", ErrInvalidSession
	}
	return "tmux attach -t " + s.ID, nil
}

// ReadOutput returns the current visible pane text for s, trimmed.
func (e *Executor) ReadOutput(ctx context.Context, s exec.Session) (string, error) {
	return e.capturePane(ctx, s)
}

// capturePane shells out to `tmux capture-pane -p -t <id>` and returns the
// trimmed pane text.
func (e *Executor) capturePane(ctx context.Context, s exec.Session) (string, error) {
	if s.ID == "" {
		return "", ErrInvalidSession
	}
	stdout, stderr, err := e.runner.Run(ctx, "tmux", "capture-pane", "-p", "-t", s.ID)
	if err != nil {
		if isBinaryNotFound(err) {
			return "", ErrTmuxNotFound
		}
		return "", fmt.Errorf("tmux capture-pane: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// ListSessions enumerates the sessions tmux currently considers live. When no
// tmux server is running tmux exits non-zero with "no server running"; that
// is reported as an empty live set rather than an error so the reaper does
// not mistake "nothing dispatched yet" for a backend failure.
func (e *Executor) ListSessions(ctx context.Context) ([]exec.Session, error) {
	stdout, _, err := e.runner.Run(ctx, "tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if isBinaryNotFound(err) {
			return nil, ErrTmuxNotFound
		}
		// A non-zero exit here means "no server / no sessions" — an empty
		// live set, not a failure the reaper should act on.
		return []exec.Session{}, nil
	}
	out := make([]exec.Session, 0)
	for _, line := range strings.Split(string(stdout), "\n") {
		// tmux session names carry no whitespace, so a trimmed non-blank line
		// is exactly one session name.
		if name := strings.TrimSpace(line); name != "" {
			out = append(out, exec.NewSession(name, Handle{}))
		}
	}
	return out, nil
}

// Stream polls the session's visible output and emits a chunk on the returned
// channel whenever the text changes. The channel closes when ctx is cancelled
// or a read fails (session gone). The first non-empty read is emitted so a
// subscriber sees the current state immediately. This mirrors the cmux
// Stream loop exactly — both are pure poll-and-diff over their ReadOutput.
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
			out, err := e.capturePane(ctx, s)
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
// same atomic sentinel cmux uses (docs §4.3); the shared exec.AwaitSentinel
// owns the hardened polling so both backends agree byte-for-byte.
func (e *Executor) AwaitExit(ctx context.Context, s exec.Session) (int, error) {
	return exec.AwaitSentinel(ctx, e.sentinelDir(s), e.pollInterval, e.awaitTimeout)
}

func (e *Executor) sentinelDir(s exec.Session) string {
	if h, ok := s.Handle().(Handle); ok {
		return h.SentinelDir
	}
	return ""
}

// sessionName turns a human task label into a tmux-safe session name. tmux
// rejects "." and ":" in names and chokes on whitespace, so every character
// outside [A-Za-z0-9_-] becomes "-", runs of "-" collapse, and the result is
// prefixed "marunage-" so marunage's sessions are greppable in a shared tmux
// server.
func sessionName(label string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range label {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	clean := strings.Trim(b.String(), "-")
	if clean == "" {
		clean = "session"
	}
	return "marunage-" + clean
}

// sortedKeys returns m's keys in deterministic order so env forwarding emits
// a stable command line.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Compile-time proof the Executor honours every interface it advertises.
var (
	_ exec.Executor     = (*Executor)(nil)
	_ exec.Attachable   = (*Executor)(nil)
	_ exec.Streamable   = (*Executor)(nil)
	_ exec.OutputReader = (*Executor)(nil)
	_ exec.Lister       = (*Executor)(nil)
)
