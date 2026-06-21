package tmux_test

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/exec"
	exectmux "github.com/haruotsu/marunage/internal/exec/tmux"
)

// readyPane is a capture-pane snapshot that satisfies the Claude readiness
// banner the tmux executor polls for during Start.
const readyPane = "Welcome to Claude Code v1.2.3\n❯ "

type runCall struct {
	name string
	args []string
}

// fakeRunner is a scriptable exectmux.Runner stand-in so these tests never
// shell out to a real tmux. It dispatches canned output per tmux subcommand.
type fakeRunner struct {
	mu    sync.Mutex
	calls []runCall

	newSessionOut string
	newSessionErr error

	capture    []string
	captureIdx int
	captureErr error

	sendErr error

	listOut string
	listErr error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, runCall{name: name, args: args})

	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "new-session":
		return []byte(f.newSessionOut), nil, f.newSessionErr
	case "capture-pane":
		if f.captureErr != nil {
			return nil, nil, f.captureErr
		}
		if len(f.capture) == 0 {
			return nil, nil, nil
		}
		i := f.captureIdx
		if i >= len(f.capture) {
			i = len(f.capture) - 1
		} else {
			f.captureIdx++
		}
		return []byte(f.capture[i]), nil, nil
	case "send-keys":
		return nil, nil, f.sendErr
	case "list-sessions":
		return []byte(f.listOut), nil, f.listErr
	}
	return nil, nil, nil
}

func (f *fakeRunner) callsFor(sub string) []runCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runCall
	for _, c := range f.calls {
		if len(c.args) > 0 && c.args[0] == sub {
			out = append(out, c)
		}
	}
	return out
}

func fastOpts(r exectmux.Runner) []exectmux.Option {
	return []exectmux.Option{
		exectmux.WithRunner(r),
		exectmux.WithPollInterval(time.Millisecond),
		exectmux.WithStartupTimeout(200 * time.Millisecond),
	}
}

func TestStartMapsSpecToNewSession(t *testing.T) {
	fr := &fakeRunner{newSessionOut: "marunage-1-buy-milk\n", capture: []string{readyPane}}
	e := exectmux.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{
		Cwd:     "/tmp/work",
		Command: "claude --dangerously-skip-permissions",
		Name:    "#1 buy milk",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.ID != "marunage-1-buy-milk" {
		t.Errorf("session ID = %q; want marunage-1-buy-milk", sess.ID)
	}
	news := fr.callsFor("new-session")
	if len(news) != 1 {
		t.Fatalf("new-session calls = %d; want 1", len(news))
	}
	joined := strings.Join(news[0].args, " ")
	for _, want := range []string{"-d", "/tmp/work", "claude --dangerously-skip-permissions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("new-session args %q missing %q", joined, want)
		}
	}
}

func TestStartForwardsEnv(t *testing.T) {
	fr := &fakeRunner{newSessionOut: "marunage-x\n", capture: []string{readyPane}}
	e := exectmux.New(fastOpts(fr)...)

	_, err := e.Start(context.Background(), exec.SessionSpec{
		Cwd:     "/tmp",
		Command: "claude",
		Name:    "x",
		Env:     map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	joined := strings.Join(fr.callsFor("new-session")[0].args, " ")
	if !strings.Contains(joined, "FOO=bar") {
		t.Errorf("new-session args %q missing FOO=bar", joined)
	}
}

func TestStartReturnsZeroSessionWhenCreateFails(t *testing.T) {
	fr := &fakeRunner{newSessionErr: errors.New("tmux boom")}
	e := exectmux.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if err == nil {
		t.Fatal("Start err = nil; want create failure")
	}
	if sess.ID != "" {
		t.Errorf("session ID = %q; want empty (nothing created → retryable)", sess.ID)
	}
}

func TestStartReturnsPopulatedSessionWhenReadinessFails(t *testing.T) {
	fr := &fakeRunner{capture: []string{"booting..."}}
	e := exectmux.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "task-n"})
	if !errors.Is(err, exectmux.ErrStartupTimeout) {
		t.Fatalf("Start err = %v; want ErrStartupTimeout", err)
	}
	// The created session must be preserved (non-empty, the name we asked tmux
	// to create) so the dispatcher fails the row instead of leaking on retry.
	if sess.ID != "marunage-task-n" {
		t.Errorf("session ID = %q; want marunage-task-n (created session, preserved for reaper)", sess.ID)
	}
}

func TestStartHonoursContextCancelDuringReadiness(t *testing.T) {
	fr := &fakeRunner{capture: []string{"booting..."}}
	e := exectmux.New(fastOpts(fr)...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.Start(ctx, exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Start err = %v; want context.Canceled", err)
	}
}

func TestStartMapsBinaryNotFound(t *testing.T) {
	fr := &fakeRunner{newSessionErr: osexec.ErrNotFound}
	e := exectmux.New(fastOpts(fr)...)
	_, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if !errors.Is(err, exectmux.ErrTmuxNotFound) {
		t.Errorf("Start err = %v; want ErrTmuxNotFound", err)
	}
}

func TestStartForwardsEnvInSortedOrder(t *testing.T) {
	fr := &fakeRunner{newSessionOut: "marunage-x\n", capture: []string{readyPane}}
	e := exectmux.New(fastOpts(fr)...)
	_, err := e.Start(context.Background(), exec.SessionSpec{
		Cwd: "/tmp", Command: "claude", Name: "x",
		Env: map[string]string{"ZED": "1", "ABC": "2"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	joined := strings.Join(fr.callsFor("new-session")[0].args, " ")
	iABC, iZED := strings.Index(joined, "ABC=2"), strings.Index(joined, "ZED=1")
	if iABC < 0 || iZED < 0 {
		t.Fatalf("new-session args %q missing forwarded env", joined)
	}
	if iABC > iZED {
		t.Errorf("env not sorted in %q; ABC should precede ZED", joined)
	}
}

func TestStartRejectsInvalidEnvKey(t *testing.T) {
	fr := &fakeRunner{newSessionOut: "marunage-x\n", capture: []string{readyPane}}
	e := exectmux.New(fastOpts(fr)...)
	_, err := e.Start(context.Background(), exec.SessionSpec{
		Cwd: "/tmp", Command: "claude", Name: "x",
		Env: map[string]string{"BAD=KEY": "x"},
	})
	if !errors.Is(err, exectmux.ErrInvalidEnvKey) {
		t.Errorf("Start err = %v; want ErrInvalidEnvKey", err)
	}
	if len(fr.callsFor("new-session")) != 0 {
		t.Error("new-session called despite invalid env key; want rejection before spawn")
	}
}

func TestSendTypesAndSubmits(t *testing.T) {
	fr := &fakeRunner{}
	e := exectmux.New(exectmux.WithRunner(fr))

	if err := e.Send(context.Background(), exec.NewSession("marunage-1", nil), "line1\nline2"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sends := fr.callsFor("send-keys")
	if len(sends) != 2 {
		t.Fatalf("send-keys calls = %d; want 2 (text + Enter)", len(sends))
	}
	textArgs := strings.Join(sends[0].args, " ")
	if !strings.Contains(textArgs, "marunage-1") || !strings.Contains(textArgs, "line1 line2") {
		t.Errorf("first send-keys = %q; want folded text addressed to marunage-1", textArgs)
	}
	if strings.Contains(textArgs, "line1\nline2") {
		t.Errorf("first send-keys = %q; newlines not folded", textArgs)
	}
	if !strings.Contains(strings.Join(sends[1].args, " "), "Enter") {
		t.Errorf("second send-keys = %v; want Enter submit", sends[1].args)
	}
}

func TestSendRejectsEmptySession(t *testing.T) {
	fr := &fakeRunner{}
	e := exectmux.New(exectmux.WithRunner(fr))
	if err := e.Send(context.Background(), exec.NewSession("", nil), "hi"); err == nil {
		t.Error("Send err = nil; want rejection of empty session id")
	}
	if len(fr.callsFor("send-keys")) != 0 {
		t.Error("send-keys called for empty session; want none")
	}
}

func TestSendMapsBinaryNotFound(t *testing.T) {
	fr := &fakeRunner{sendErr: osexec.ErrNotFound}
	e := exectmux.New(exectmux.WithRunner(fr))
	err := e.Send(context.Background(), exec.NewSession("marunage-1", nil), "hi")
	if !errors.Is(err, exectmux.ErrTmuxNotFound) {
		t.Errorf("Send err = %v; want ErrTmuxNotFound", err)
	}
}

func TestAttachReturnsCommand(t *testing.T) {
	e := exectmux.New(exectmux.WithRunner(&fakeRunner{}))
	link, err := e.Attach(context.Background(), exec.NewSession("marunage-5", nil))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !strings.Contains(link, "marunage-5") || !strings.Contains(link, "attach") {
		t.Errorf("attach = %q; want a tmux attach command for marunage-5", link)
	}
}

func TestAttachRejectsEmptySession(t *testing.T) {
	e := exectmux.New(exectmux.WithRunner(&fakeRunner{}))
	if _, err := e.Attach(context.Background(), exec.NewSession("", nil)); err == nil {
		t.Error("Attach err = nil; want rejection of empty session id")
	}
}

func TestReadOutputCapturesPane(t *testing.T) {
	fr := &fakeRunner{capture: []string{"  hello pane  "}}
	e := exectmux.New(exectmux.WithRunner(fr))
	out, err := e.ReadOutput(context.Background(), exec.NewSession("marunage-1", nil))
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if out != "hello pane" {
		t.Errorf("ReadOutput = %q; want trimmed \"hello pane\"", out)
	}
}

func TestListSessionsParsesNames(t *testing.T) {
	fr := &fakeRunner{listOut: "marunage-1\nmarunage-2\n"}
	e := exectmux.New(exectmux.WithRunner(fr))
	sessions, err := e.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != "marunage-1" || sessions[1].ID != "marunage-2" {
		t.Errorf("sessions = %+v; want ids marunage-1, marunage-2", sessions)
	}
}

func TestListSessionsEmptyWhenNoServer(t *testing.T) {
	// `tmux list-sessions` exits non-zero with "no server running" when no
	// sessions exist; the reaper must see an empty live set, not an error.
	fr := &fakeRunner{listErr: errors.New("no server running"), listOut: "no server running on /tmp/tmux"}
	e := exectmux.New(exectmux.WithRunner(fr))
	sessions, err := e.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %+v; want empty for no-server", sessions)
	}
}

func TestAwaitExitReadsSentinel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exec.SentinelFile), []byte("3\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	e := exectmux.New(exectmux.WithRunner(&fakeRunner{}), exectmux.WithPollInterval(time.Millisecond))
	code, err := e.AwaitExit(context.Background(), exec.NewSession("marunage-1", exectmux.Handle{SentinelDir: dir}))
	if err != nil {
		t.Fatalf("AwaitExit: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d; want 3", code)
	}
}

func TestAwaitExitWithoutSentinelDir(t *testing.T) {
	e := exectmux.New(exectmux.WithRunner(&fakeRunner{}))
	_, err := e.AwaitExit(context.Background(), exec.NewSession("marunage-1", nil))
	if !errors.Is(err, exec.ErrNoSentinelDir) {
		t.Errorf("err = %v; want ErrNoSentinelDir", err)
	}
}

func TestStreamEmitsOnChange(t *testing.T) {
	fr := &fakeRunner{capture: []string{"a", "a", "a\nb"}}
	e := exectmux.New(exectmux.WithRunner(fr), exectmux.WithPollInterval(time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := e.Stream(ctx, exec.NewSession("marunage-1", nil))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := string(<-ch); got != "a" {
		t.Errorf("first chunk = %q; want a", got)
	}
	if got := string(<-ch); got != "a\nb" {
		t.Errorf("second chunk = %q; want a\\nb", got)
	}
}

func TestStreamClosesOnCancel(t *testing.T) {
	fr := &fakeRunner{capture: []string{"x"}}
	e := exectmux.New(exectmux.WithRunner(fr), exectmux.WithPollInterval(time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := e.Stream(ctx, exec.NewSession("marunage-1", nil))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel not closed after cancel; Stream goroutine leaked")
		}
	}
}

func TestStreamClosesOnReadError(t *testing.T) {
	fr := &fakeRunner{captureErr: errors.New("session gone")}
	e := exectmux.New(exectmux.WithRunner(fr), exectmux.WithPollInterval(time.Millisecond))
	ch, err := e.Stream(context.Background(), exec.NewSession("marunage-1", nil))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("got a chunk; want the channel closed on read error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after read error")
	}
}
