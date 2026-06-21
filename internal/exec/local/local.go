// Package local implements exec.Executor by launching the claude command
// as a direct child process (os/exec) instead of inside a cmux workspace
// or tmux pane. It is the deliberately capability-poor backend that proves
// marunage's "fire-and-forget" loop still works when a backend offers
// nothing beyond the three required Executor methods:
//
//   - Attachable is NOT implemented: a bare child process cannot hand a
//     human an interactive deeplink, so executor.(exec.Attachable) is
//     ok=false and callers fall through to running without attach.
//   - OutputReader IS implemented as the cheap "△" capability from
//     docs/redesign_layering.md §4.2: a point-in-time snapshot of the
//     child's captured stdout/stderr. Streamable (live channel) is left
//     out on purpose to keep this backend minimal.
//
// Limitations (honest §4.2 "△"): the child's stdin is a plain pipe. Send
// folds a multi-line prompt into one logical line and appends a newline,
// mirroring the cmux client, but an interactive TUI such as claude reads
// its input through a pty and may not treat pipe writes identically to
// typed keystrokes. local is best suited to non-interactive or
// prompt-on-argv invocations; for full interactive takeover use the cmux
// or tmux backend. A session is only addressable through the live process
// handle returned by Start: a Session reconstructed from a stored id alone
// (exec.NewSession(id, nil)) has no process and Send/AwaitExit return
// ErrNoProcess.
package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"regexp"
	"strconv"
	"sync"

	"github.com/haruotsu/marunage/internal/exec"
)

// ErrNoProcess is returned by Send / AwaitExit when the Session carries no
// live process handle (it was reconstructed from an id rather than created
// by this Executor's Start). local keeps process state in memory only, so
// such a session cannot be addressed.
var ErrNoProcess = errors.New("exec/local: session has no live process")

// runningProcess abstracts a launched child so tests can inject a fake and
// never spawn a real claude. The os/exec-backed implementation is
// osProcess.
type runningProcess interface {
	pid() int
	write(p []byte) (int, error)
	closeStdin() error
	wait() (int, error)
	kill() error
	snapshot() string
}

// starter abstracts process creation (the os/exec call) behind an
// interface so Start's bookkeeping can be tested without the OS.
type starter interface {
	start(spec exec.SessionSpec) (runningProcess, error)
}

// Executor launches claude as a direct child process. Construct it with
// New. It satisfies exec.Executor and exec.OutputReader, and deliberately
// does not satisfy exec.Attachable.
type Executor struct {
	starter starter
}

// Option mutates Executor construction, mirroring the functional-option
// shape used across marunage.
type Option func(*Executor)

// withStarter swaps the process launcher; tests inject a fake. Unexported
// because the os/exec default is the only launcher production uses.
func withStarter(s starter) Option {
	return func(e *Executor) { e.starter = s }
}

// New builds a local Executor. With no options it launches real child
// processes via os/exec.
func New(opts ...Option) *Executor {
	e := &Executor{starter: &osStarter{}}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// handle is the backend-internal value carried in exec.Session.handle. It
// holds the live process so Send / AwaitExit / ReadOutput can reach it.
type handle struct {
	proc runningProcess
}

// Start launches spec.Command as a child process in spec.Cwd with spec.Env
// applied. Readiness is "the process started": once the launch succeeds the
// session is ready for Send, so Start returns immediately with a Session
// whose ID is the child's pid. A launch failure created nothing, so it
// returns the zero Session (signalling "retryable" per the exec.Executor
// contract) alongside the error.
func (e *Executor) Start(_ context.Context, spec exec.SessionSpec) (exec.Session, error) {
	proc, err := e.starter.start(spec)
	if err != nil {
		return exec.Session{}, err
	}
	return exec.NewSession(strconv.Itoa(proc.pid()), handle{proc: proc}), nil
}

// promptNewlines collapses runs of CR/LF, matching the cmux client so a
// multi-line prompt is submitted as one logical line.
var promptNewlines = regexp.MustCompile(`[\r\n]+`)

// Send folds prompt into a single line and writes it (plus a trailing
// newline to submit) to the child's stdin. See the package doc for the
// pipe-vs-pty caveat.
func (e *Executor) Send(_ context.Context, s exec.Session, prompt string) error {
	proc, err := procOf(s)
	if err != nil {
		return err
	}
	folded := promptNewlines.ReplaceAllString(prompt, " ")
	_, err = proc.write([]byte(folded + "\n"))
	return err
}

// AwaitExit closes the child's stdin (signalling no further input), then
// blocks until the process exits and returns its exit code. A non-zero
// exit is reported as a code with a nil error; a non-nil error is reserved
// for a genuine wait failure or ctx cancellation. On cancellation the child
// is killed and reaped before returning ctx.Err().
func (e *Executor) AwaitExit(ctx context.Context, s exec.Session) (int, error) {
	proc, err := procOf(s)
	if err != nil {
		return 0, err
	}
	// Closing stdin lets a well-behaved child reading to EOF terminate; the
	// error is ignored because the child may have already exited and closed
	// its end.
	_ = proc.closeStdin()

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := proc.wait()
		done <- result{code, err}
	}()

	select {
	case <-ctx.Done():
		_ = proc.kill()
		<-done // reap the killed process so no goroutine leaks
		return 0, ctx.Err()
	case r := <-done:
		return r.code, r.err
	}
}

// ReadOutput returns a point-in-time snapshot of the child's captured
// stdout/stderr. This is the optional "△" capability for local; it lets the
// web live-stream endpoint poll a local session even though local cannot
// attach.
func (e *Executor) ReadOutput(_ context.Context, s exec.Session) (string, error) {
	proc, err := procOf(s)
	if err != nil {
		return "", err
	}
	return proc.snapshot(), nil
}

// procOf extracts the live process from a Session, or ErrNoProcess if the
// session was not created by this Executor's Start.
func procOf(s exec.Session) (runningProcess, error) {
	h, ok := s.Handle().(handle)
	if !ok || h.proc == nil {
		return nil, ErrNoProcess
	}
	return h.proc, nil
}

// --- os/exec-backed implementation ---

type osStarter struct{}

func (osStarter) start(spec exec.SessionSpec) (runningProcess, error) {
	// spec.Command is a literal command line (e.g. "claude --foo"); run it
	// through sh -c so quoting/args behave the same as the cmux backend,
	// which also hands the command line to its launcher verbatim.
	cmd := osexec.Command("sh", "-c", spec.Command)
	cmd.Dir = spec.Cwd
	cmd.Env = mergeEnv(spec.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	buf := &syncBuffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	return &osProcess{cmd: cmd, stdin: stdin, buf: buf}, nil
}

// mergeEnv returns the launcher's environment with extra applied on top, or
// nil when extra is empty (nil cmd.Env means "inherit unchanged", matching
// the documented SessionSpec.Env semantics).
func mergeEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return nil
	}
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

type osProcess struct {
	cmd       *osexec.Cmd
	stdin     io.WriteCloser
	buf       *syncBuffer
	closeOnce sync.Once
}

func (p *osProcess) pid() int { return p.cmd.Process.Pid }

func (p *osProcess) write(b []byte) (int, error) { return p.stdin.Write(b) }

func (p *osProcess) closeStdin() error {
	var err error
	p.closeOnce.Do(func() { err = p.stdin.Close() })
	return err
}

func (p *osProcess) wait() (int, error) {
	err := p.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	// A non-zero exit is the program's result, not a failure of AwaitExit:
	// surface the code with a nil error so callers branch on the code.
	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

func (p *osProcess) kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *osProcess) snapshot() string { return p.buf.String() }

// syncBuffer is a goroutine-safe bytes.Buffer: os/exec's copy goroutine
// writes the child's output while ReadOutput may snapshot it concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Compile-time proof of the capability set: Executor + OutputReader, and
// (by omission) NOT Attachable / Streamable.
var (
	_ exec.Executor     = (*Executor)(nil)
	_ exec.OutputReader = (*Executor)(nil)
)
