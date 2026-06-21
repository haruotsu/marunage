package cmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"strconv"

	cmuxclient "github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/exec"
	execcmux "github.com/haruotsu/marunage/internal/exec/cmux"
)

// fakeClient is a scriptable cmuxclient.Client stand-in so these tests
// never spawn a real cmux.
type fakeClient struct {
	mu sync.Mutex

	newWorkspaceOpts []cmuxclient.NewWorkspaceOptions
	newWorkspaceHook func(cmuxclient.NewWorkspaceOptions) (cmuxclient.Workspace, error)

	waitReadyErr  error
	waitReadyHook func(cmuxclient.Workspace) error

	sendCalls []sendCall
	sendErr   error

	listResp []cmuxclient.Workspace
	listErr  error

	readOutputs []string
	readErr     error
	readIdx     int

	nextID int
}

type sendCall struct {
	ws   cmuxclient.Workspace
	text string
}

func (f *fakeClient) NewWorkspace(_ context.Context, opts cmuxclient.NewWorkspaceOptions) (cmuxclient.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newWorkspaceOpts = append(f.newWorkspaceOpts, opts)
	if f.newWorkspaceHook != nil {
		return f.newWorkspaceHook(opts)
	}
	f.nextID++
	return cmuxclient.Workspace{ID: workspaceID(f.nextID), Name: opts.Name}, nil
}

func workspaceID(n int) string { return "workspace:" + strconv.Itoa(n) }

func (f *fakeClient) WaitReady(_ context.Context, ws cmuxclient.Workspace) error {
	if f.waitReadyHook != nil {
		return f.waitReadyHook(ws)
	}
	return f.waitReadyErr
}

func (f *fakeClient) Send(_ context.Context, ws cmuxclient.Workspace, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls = append(f.sendCalls, sendCall{ws: ws, text: text})
	return f.sendErr
}

func (f *fakeClient) ListWorkspaces(_ context.Context) ([]cmuxclient.Workspace, error) {
	return f.listResp, f.listErr
}

func (f *fakeClient) ReadOutput(_ context.Context, _ cmuxclient.Workspace) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return "", f.readErr
	}
	if f.readIdx >= len(f.readOutputs) {
		if len(f.readOutputs) == 0 {
			return "", nil
		}
		return f.readOutputs[len(f.readOutputs)-1], nil
	}
	out := f.readOutputs[f.readIdx]
	f.readIdx++
	return out, nil
}

func TestStartMapsSpecAndReturnsSession(t *testing.T) {
	fc := &fakeClient{}
	e := execcmux.New(fc)

	sess, err := e.Start(context.Background(), exec.SessionSpec{
		Cwd:     "/tmp/work",
		Command: "claude --dangerously-skip-permissions",
		Name:    "#1 buy milk",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.ID != "workspace:1" {
		t.Errorf("session ID = %q; want workspace:1", sess.ID)
	}
	if len(fc.newWorkspaceOpts) != 1 {
		t.Fatalf("NewWorkspace calls = %d; want 1", len(fc.newWorkspaceOpts))
	}
	got := fc.newWorkspaceOpts[0]
	if got.CWD != "/tmp/work" || got.Command != "claude --dangerously-skip-permissions" || got.Name != "#1 buy milk" {
		t.Errorf("NewWorkspaceOptions = %+v; want spec fields mapped through", got)
	}
}

func TestStartReturnsEmptySessionWhenCreateFails(t *testing.T) {
	fc := &fakeClient{
		newWorkspaceHook: func(cmuxclient.NewWorkspaceOptions) (cmuxclient.Workspace, error) {
			return cmuxclient.Workspace{}, errors.New("cmux boom")
		},
	}
	e := execcmux.New(fc)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if err == nil {
		t.Fatal("Start err = nil; want create failure")
	}
	if sess.ID != "" {
		t.Errorf("session ID = %q; want empty (nothing created → retryable)", sess.ID)
	}
}

func TestStartReturnsPopulatedSessionWhenReadinessFails(t *testing.T) {
	fc := &fakeClient{waitReadyErr: cmuxclient.ErrTimeout}
	e := execcmux.New(fc)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if err == nil {
		t.Fatal("Start err = nil; want readiness failure")
	}
	// Must be the real workspace id NewWorkspace minted, not just any
	// non-empty string: the dispatcher persists this so the reaper can
	// reclaim the leaked session.
	if sess.ID != "workspace:1" {
		t.Errorf("session ID = %q; want workspace:1 (the created ws, preserved for reaper)", sess.ID)
	}
}

func TestSendAddressesWorkspaceByID(t *testing.T) {
	fc := &fakeClient{}
	e := execcmux.New(fc)

	if err := e.Send(context.Background(), exec.NewSession("workspace:9", nil), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fc.sendCalls) != 1 {
		t.Fatalf("Send calls = %d; want 1", len(fc.sendCalls))
	}
	if fc.sendCalls[0].ws.ID != "workspace:9" || fc.sendCalls[0].text != "hello" {
		t.Errorf("Send call = %+v; want ws workspace:9 text hello", fc.sendCalls[0])
	}
}

func TestListSessionsMapsWorkspaceIDs(t *testing.T) {
	fc := &fakeClient{listResp: []cmuxclient.Workspace{{ID: "workspace:1"}, {ID: "workspace:2"}}}
	e := execcmux.New(fc)

	sessions, err := e.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != "workspace:1" || sessions[1].ID != "workspace:2" {
		t.Errorf("sessions = %+v; want ids workspace:1, workspace:2", sessions)
	}
}

func TestAttachReturnsDeeplink(t *testing.T) {
	e := execcmux.New(&fakeClient{})
	link, err := e.Attach(context.Background(), exec.NewSession("workspace:5", nil))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if link != "cmux://workspace:5" {
		t.Errorf("deeplink = %q; want cmux://workspace:5", link)
	}
}

func TestAwaitExitReadsSentinel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".exit_code"), []byte("0\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	e := execcmux.New(&fakeClient{}, execcmux.WithPollInterval(time.Millisecond))

	code, err := e.AwaitExit(context.Background(), exec.NewSession("workspace:1", execcmux.Handle{SentinelDir: dir}))
	if err != nil {
		t.Fatalf("AwaitExit: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d; want 0", code)
	}
}

func TestAwaitExitReturnsNonZeroCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".exit_code"), []byte("127"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	e := execcmux.New(&fakeClient{}, execcmux.WithPollInterval(time.Millisecond))

	code, err := e.AwaitExit(context.Background(), exec.NewSession("workspace:1", execcmux.Handle{SentinelDir: dir}))
	if err != nil {
		t.Fatalf("AwaitExit: %v", err)
	}
	if code != 127 {
		t.Errorf("exit code = %d; want 127", code)
	}
}

func TestAwaitExitTimesOut(t *testing.T) {
	dir := t.TempDir() // no sentinel ever written
	e := execcmux.New(&fakeClient{},
		execcmux.WithPollInterval(time.Millisecond),
		execcmux.WithAwaitTimeout(20*time.Millisecond),
	)

	_, err := e.AwaitExit(context.Background(), exec.NewSession("workspace:1", execcmux.Handle{SentinelDir: dir}))
	if !errors.Is(err, execcmux.ErrAwaitTimeout) {
		t.Errorf("err = %v; want ErrAwaitTimeout", err)
	}
}

func TestAwaitExitWithoutSentinelDir(t *testing.T) {
	e := execcmux.New(&fakeClient{})
	_, err := e.AwaitExit(context.Background(), exec.NewSession("workspace:1", nil))
	if !errors.Is(err, execcmux.ErrNoSentinelDir) {
		t.Errorf("err = %v; want ErrNoSentinelDir", err)
	}
}

// TestAwaitExitRejectsSymlink pins the O_NOFOLLOW defence: a sentinel that
// is a symlink (a prompt-injected Claude swapping the file) must surface an
// error rather than following the link off the workspace dir.
func TestAwaitExitRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real_target")
	if err := os.WriteFile(target, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".exit_code")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	e := execcmux.New(&fakeClient{}, execcmux.WithPollInterval(time.Millisecond))

	_, err := e.AwaitExit(context.Background(), exec.NewSession("workspace:1", execcmux.Handle{SentinelDir: dir}))
	if err == nil {
		t.Fatal("AwaitExit err = nil; want a symlink-refused error")
	}
	if errors.Is(err, execcmux.ErrAwaitTimeout) || errors.Is(err, execcmux.ErrNoSentinelDir) {
		t.Errorf("err = %v; want a symlink rejection, not timeout/no-dir", err)
	}
}

// TestAwaitExitRejectsUnparseableSentinel pins that a non-numeric sentinel
// body is a hard error (not silently treated as exit 0).
func TestAwaitExitRejectsUnparseableSentinel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".exit_code"), []byte("not-a-number"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	e := execcmux.New(&fakeClient{}, execcmux.WithPollInterval(time.Millisecond))

	_, err := e.AwaitExit(context.Background(), exec.NewSession("workspace:1", execcmux.Handle{SentinelDir: dir}))
	if err == nil {
		t.Fatal("AwaitExit err = nil; want a parse error")
	}
	if errors.Is(err, execcmux.ErrAwaitTimeout) {
		t.Errorf("err = %v; want a parse error, not timeout", err)
	}
}

func TestStreamEmitsOnChange(t *testing.T) {
	fc := &fakeClient{readOutputs: []string{"line1", "line1", "line1\nline2"}}
	e := execcmux.New(fc, execcmux.WithPollInterval(time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := e.Stream(ctx, exec.NewSession("workspace:1", nil))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	first := <-ch
	if string(first) != "line1" {
		t.Errorf("first chunk = %q; want line1", first)
	}
	second := <-ch
	if string(second) != "line1\nline2" {
		t.Errorf("second chunk = %q; want line1\\nline2", second)
	}
}

// TestStreamClosesOnCancel pins that cancelling ctx terminates the polling
// goroutine and closes the channel (no leak).
func TestStreamClosesOnCancel(t *testing.T) {
	fc := &fakeClient{readOutputs: []string{"x"}}
	e := execcmux.New(fc, execcmux.WithPollInterval(time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := e.Stream(ctx, exec.NewSession("workspace:1", nil))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	cancel()

	// Drain until the channel is closed; a leaked goroutine would never
	// close it and the test would hang (caught by the test timeout).
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed — success
			}
		case <-deadline:
			t.Fatal("channel not closed after cancel; Stream goroutine leaked")
		}
	}
}

// TestStreamClosesOnReadError pins that a backend read failure (workspace
// gone) terminates the stream rather than spinning.
func TestStreamClosesOnReadError(t *testing.T) {
	fc := &fakeClient{readErr: errors.New("workspace gone")}
	e := execcmux.New(fc, execcmux.WithPollInterval(time.Millisecond))

	ch, err := e.Stream(context.Background(), exec.NewSession("workspace:1", nil))
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
