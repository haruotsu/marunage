package local

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/exec"
)

// fakeProcess is an in-memory runningProcess so the local Executor's
// Start/Send/AwaitExit logic can be exercised without launching a real
// child process.
type fakeProcess struct {
	mu          sync.Mutex
	id          int
	written     []byte // bytes received on stdin via write()
	output      string // simulated captured stdout/stderr, read by snapshot()
	exitCode    int
	waitErr     error
	killed      bool
	stdinClosed bool
	waitGate    chan struct{}
}

func (p *fakeProcess) pid() int { return p.id }

func (p *fakeProcess) closeStdin() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stdinClosed = true
	return nil
}

func (p *fakeProcess) write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, b...)
	return len(b), nil
}

func (p *fakeProcess) wait() (int, error) {
	p.mu.Lock()
	gate := p.waitGate
	p.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return p.exitCode, p.waitErr
}

func (p *fakeProcess) kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.killed {
		return nil
	}
	p.killed = true
	if p.waitGate != nil {
		close(p.waitGate)
	}
	return nil
}

func (p *fakeProcess) snapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.output
}

func (p *fakeProcess) sent() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.written)
}

// fakeStarter records the spec it was handed and returns a preset process
// (or error).
type fakeStarter struct {
	proc    *fakeProcess
	err     error
	gotSpec exec.SessionSpec
}

func (s *fakeStarter) start(spec exec.SessionSpec) (runningProcess, error) {
	s.gotSpec = spec
	if s.err != nil {
		return nil, s.err
	}
	return s.proc, nil
}

func TestStartReturnsSessionWithPidID(t *testing.T) {
	starter := &fakeStarter{proc: &fakeProcess{id: 4321}}
	e := New(withStarter(starter))

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}
	if want := strconv.Itoa(4321); sess.ID != want {
		t.Errorf("session ID = %q, want %q", sess.ID, want)
	}
}

func TestStartForwardsSpec(t *testing.T) {
	starter := &fakeStarter{proc: &fakeProcess{id: 1}}
	e := New(withStarter(starter))

	spec := exec.SessionSpec{
		Cwd:     "/tmp/work",
		Command: "claude --foo",
		Name:    "task-7",
		Env:     map[string]string{"K": "V"},
	}
	if _, err := e.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if starter.gotSpec.Cwd != spec.Cwd ||
		starter.gotSpec.Command != spec.Command ||
		starter.gotSpec.Name != spec.Name ||
		starter.gotSpec.Env["K"] != "V" {
		t.Errorf("spec not forwarded verbatim: got %+v", starter.gotSpec)
	}
}

func TestStartFailureReturnsZeroSession(t *testing.T) {
	starter := &fakeStarter{err: errors.New("boom")}
	e := New(withStarter(starter))

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "claude"})
	if err == nil {
		t.Fatal("Start: expected error, got nil")
	}
	if sess.ID != "" {
		t.Errorf("failed Start returned non-empty Session ID %q; want zero Session (retryable)", sess.ID)
	}
}

func TestSendWritesFoldedPromptToStdin(t *testing.T) {
	proc := &fakeProcess{id: 9}
	e := New(withStarter(&fakeStarter{proc: proc}))

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := e.Send(context.Background(), sess, "line1\nline2\r\nline3"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got, want := proc.sent(), "line1 line2 line3\n"; got != want {
		t.Errorf("stdin = %q, want %q", got, want)
	}
}

func TestSendWithoutLiveProcessErrors(t *testing.T) {
	e := New(withStarter(&fakeStarter{proc: &fakeProcess{id: 1}}))

	// A session reconstructed from a stored id alone carries no live
	// process handle, which a local backend cannot address.
	err := e.Send(context.Background(), exec.NewSession("1", nil), "hi")
	if !errors.Is(err, ErrNoProcess) {
		t.Errorf("Send error = %v, want ErrNoProcess", err)
	}
}

func TestAwaitExitReturnsExitCode(t *testing.T) {
	proc := &fakeProcess{id: 2, exitCode: 7}
	e := New(withStarter(&fakeStarter{proc: proc}))

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	code, err := e.AwaitExit(context.Background(), sess)
	if err != nil {
		t.Fatalf("AwaitExit: unexpected error: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

func TestAwaitExitCancelledKillsProcess(t *testing.T) {
	proc := &fakeProcess{id: 3, waitGate: make(chan struct{})}
	e := New(withStarter(&fakeStarter{proc: proc}))

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.AwaitExit(ctx, sess); !errors.Is(err, context.Canceled) {
		t.Errorf("AwaitExit error = %v, want context.Canceled", err)
	}
	proc.mu.Lock()
	killed := proc.killed
	proc.mu.Unlock()
	if !killed {
		t.Error("AwaitExit did not kill the process on cancellation")
	}
}

func TestAwaitExitPropagatesWaitError(t *testing.T) {
	proc := &fakeProcess{id: 2, waitErr: errors.New("wait blew up")}
	e := New(withStarter(&fakeStarter{proc: proc}))

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.AwaitExit(context.Background(), sess); err == nil {
		t.Error("AwaitExit: expected the underlying wait error, got nil")
	}
}

func TestAwaitExitClosesStdin(t *testing.T) {
	proc := &fakeProcess{id: 2}
	e := New(withStarter(&fakeStarter{proc: proc}))

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.AwaitExit(context.Background(), sess); err != nil {
		t.Fatalf("AwaitExit: %v", err)
	}
	proc.mu.Lock()
	closed := proc.stdinClosed
	proc.mu.Unlock()
	if !closed {
		t.Error("AwaitExit did not close stdin (no EOF signalled to the child)")
	}
}

func TestAwaitExitWithoutLiveProcessErrors(t *testing.T) {
	e := New(withStarter(&fakeStarter{proc: &fakeProcess{id: 1}}))

	_, err := e.AwaitExit(context.Background(), exec.NewSession("1", nil))
	if !errors.Is(err, ErrNoProcess) {
		t.Errorf("AwaitExit error = %v, want ErrNoProcess", err)
	}
}

func TestReadOutputSnapshot(t *testing.T) {
	proc := &fakeProcess{id: 5, output: "hello world"}
	e := New(withStarter(&fakeStarter{proc: proc}))

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "claude"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	out, err := e.ReadOutput(context.Background(), sess)
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if out != "hello world" {
		t.Errorf("ReadOutput = %q, want %q", out, "hello world")
	}
}
