package cmux_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/workspace/cmux"
)

// Test list (t_wada TDD; ticked off as the matching test below goes green):
//
//   1. NewWorkspace shells out to `cmux new-workspace` with the documented
//      flag set (--cwd, --command, --name) — driven by a fakeRunner that
//      records its argv.
//   2. NewWorkspace parses a stdout banner of the form "workspace:42" into
//      Workspace{ID:"workspace:42", Name:opts.Name}.
//   3. NewWorkspace surfaces ErrCmuxNotFound when the runner returns an
//      exec.ErrNotFound-shaped error, so callers can branch via errors.Is.
//   4. NewWorkspace rejects opts that lack the required CWD / Command /
//      Name fields with ErrInvalidOptions.
//   5. NewWorkspace surfaces unparseable stdout as ErrUnparseableOutput so
//      a future cmux release that reshapes its banner fails loudly rather
//      than silently dispatching to "".
//   6. Send shells out to `cmux send <ws> <text>` verbatim when text has
//      no newlines.
//   7. Send replaces newline runs with single spaces before invoking cmux
//      (docs/requirement.md execution dispatch step 2.e).
//   8. Send falls back to `ws-send <ws> <text>` when the primary `cmux
//      send` invocation reports a non-zero exit (requirement.md step 2.f).
//   9. Send rejects empty workspace IDs with ErrInvalidWorkspace so a
//      caller cannot accidentally send to "".
//  10. WaitReady polls the readiness probe and returns nil as soon as it
//      reports ready=true.
//  11. WaitReady honours the configured startup timeout and returns
//      ErrTimeout when the probe never goes ready.
//  12. WaitReady returns ctx.Err() immediately when the parent context is
//      cancelled, even if the timeout has not expired.
//  14. ListWorkspaces shells out to `cmux list-workspaces` (no flags).
//  15. ListWorkspaces parses every line-leading "workspace:NNN" token into
//      the returned []Workspace.
//  16. ListWorkspaces returns an empty (non-nil) slice for empty stdout so
//      callers can range without a nil check.
//  17. ListWorkspaces maps a missing cmux binary to ErrCmuxNotFound.
//  18. ListWorkspaces wraps a non-zero exit with a stderr-bearing diagnostic
//      so the operator can see what cmux complained about.

// callRecord captures one Runner.Run invocation so assertions can inspect
// argv ordering. The struct is exported only to the test file; the fake
// itself returns it via Calls().
type callRecord struct {
	Name string
	Args []string
}

// fakeRunner is the test double for cmux.Runner. Each call consults the
// queued result slice in FIFO order; running out of queued results is a
// programming error in the test, not a runtime fall-through, so the fake
// fails the test loudly rather than returning a zero value.
type fakeRunner struct {
	mu      sync.Mutex
	results []runResult
	calls   []callRecord
}

type runResult struct {
	Stdout string
	Stderr string
	Err    error
}

func (f *fakeRunner) queue(rs ...runResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, rs...)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, callRecord{Name: name, Args: append([]string(nil), args...)})
	if len(f.results) == 0 {
		return nil, nil, errors.New("fakeRunner: no queued result for call")
	}
	r := f.results[0]
	f.results = f.results[1:]
	return []byte(r.Stdout), []byte(r.Stderr), r.Err
}

func (f *fakeRunner) Calls() []callRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]callRecord, len(f.calls))
	copy(out, f.calls)
	return out
}

// 1 + 2: NewWorkspace passes the documented flags and parses the workspace
// banner.
func TestNewWorkspaceShellsOutAndParsesID(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "workspace:42\n"})

	c := cmux.NewClient(cmux.WithRunner(r))
	ws, err := c.NewWorkspace(context.Background(), cmux.NewWorkspaceOptions{
		CWD:     "/tmp/repo",
		Command: "claude --dangerously-skip-permissions",
		Name:    "#7 fix bug",
	})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if ws.ID != "workspace:42" {
		t.Errorf("ws.ID = %q; want %q", ws.ID, "workspace:42")
	}
	if ws.Name != "#7 fix bug" {
		t.Errorf("ws.Name = %q; want %q", ws.Name, "#7 fix bug")
	}

	calls := r.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls() len = %d; want 1", len(calls))
	}
	if calls[0].Name != "cmux" {
		t.Errorf("Calls()[0].Name = %q; want %q", calls[0].Name, "cmux")
	}
	want := []string{
		"new-workspace",
		"--cwd", "/tmp/repo",
		"--command", "claude --dangerously-skip-permissions",
		"--name", "#7 fix bug",
	}
	if !equalArgs(calls[0].Args, want) {
		t.Errorf("Args = %v; want %v", calls[0].Args, want)
	}
}

// 3: missing cmux binary surfaces as the typed sentinel.
func TestNewWorkspaceMapsMissingBinaryToErrCmuxNotFound(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Err: &exec.Error{Name: "cmux", Err: exec.ErrNotFound}})

	c := cmux.NewClient(cmux.WithRunner(r))
	_, err := c.NewWorkspace(context.Background(), cmux.NewWorkspaceOptions{
		CWD:     "/tmp",
		Command: "claude",
		Name:    "n",
	})
	if !errors.Is(err, cmux.ErrCmuxNotFound) {
		t.Fatalf("NewWorkspace error = %v; want ErrCmuxNotFound", err)
	}
}

// 4: opts validation.
func TestNewWorkspaceValidatesRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		opts cmux.NewWorkspaceOptions
	}{
		{"missing CWD", cmux.NewWorkspaceOptions{Command: "claude", Name: "n"}},
		{"missing Command", cmux.NewWorkspaceOptions{CWD: "/tmp", Name: "n"}},
		{"missing Name", cmux.NewWorkspaceOptions{CWD: "/tmp", Command: "claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{}
			c := cmux.NewClient(cmux.WithRunner(r))
			_, err := c.NewWorkspace(context.Background(), tc.opts)
			if !errors.Is(err, cmux.ErrInvalidOptions) {
				t.Fatalf("err = %v; want ErrInvalidOptions", err)
			}
			if got := len(r.Calls()); got != 0 {
				t.Errorf("Runner invoked %d times on validation failure; want 0", got)
			}
		})
	}
}

// 5: unparseable stdout fails loudly.
func TestNewWorkspaceUnparseableOutput(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "no workspace banner here\n"})

	c := cmux.NewClient(cmux.WithRunner(r))
	_, err := c.NewWorkspace(context.Background(), cmux.NewWorkspaceOptions{
		CWD: "/tmp", Command: "claude", Name: "n",
	})
	if !errors.Is(err, cmux.ErrUnparseableOutput) {
		t.Fatalf("err = %v; want ErrUnparseableOutput", err)
	}
}

// 6: Send forwards verbatim text to `cmux send`.
func TestSendForwardsArgs(t *testing.T) {
	r := &fakeRunner{}
	// send text, then send-key enter
	r.queue(runResult{}, runResult{})

	c := cmux.NewClient(cmux.WithRunner(r))
	err := c.Send(context.Background(), cmux.Workspace{ID: "workspace:7"}, "hello world")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := r.Calls()
	if len(calls) != 2 {
		t.Fatalf("Calls len = %d; want 2 (send + send-key)", len(calls))
	}
	wantSend := []string{"send", "--workspace", "workspace:7", "hello world"}
	if !equalArgs(calls[0].Args, wantSend) {
		t.Errorf("send Args = %v; want %v", calls[0].Args, wantSend)
	}
	wantKey := []string{"send-key", "--workspace", "workspace:7", "enter"}
	if !equalArgs(calls[1].Args, wantKey) {
		t.Errorf("send-key Args = %v; want %v", calls[1].Args, wantKey)
	}
}

// 7: newline runs collapse to a single space; Enter is sent separately via send-key.
func TestSendReplacesNewlinesWithSpaces(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{}, runResult{})

	c := cmux.NewClient(cmux.WithRunner(r))
	err := c.Send(context.Background(), cmux.Workspace{ID: "workspace:7"}, "line1\nline2\r\nline3")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := r.Calls()
	if len(calls) != 2 {
		t.Fatalf("Calls len = %d; want 2 (send + send-key)", len(calls))
	}
	got := calls[0].Args[len(calls[0].Args)-1]
	want := "line1 line2 line3"
	if got != want {
		t.Errorf("payload = %q; want %q", got, want)
	}
}

// 7b: send-key failure after a successful send must be propagated.
func TestSendReturnsErrorWhenSendKeyFails(t *testing.T) {
	r := &fakeRunner{}
	r.queue(
		runResult{}, // cmux send succeeds
		runResult{Err: errors.New("send-key: exit 1")}, // send-key fails
	)

	c := cmux.NewClient(cmux.WithRunner(r))
	err := c.Send(context.Background(), cmux.Workspace{ID: "workspace:7"}, "hello")
	if err == nil {
		t.Fatal("Send returned nil; want error when send-key fails")
	}
}

// 8: ws-send fallback on primary failure.
func TestSendFallsBackToWsSendOnFailure(t *testing.T) {
	r := &fakeRunner{}
	// Primary `cmux send` exits non-zero; fallback `ws-send` succeeds.
	r.queue(
		runResult{Err: errors.New("cmux send: exit 1")},
		runResult{},
	)

	c := cmux.NewClient(cmux.WithRunner(r))
	err := c.Send(context.Background(), cmux.Workspace{ID: "workspace:7"}, "msg")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := r.Calls()
	// 2 calls: primary (cmux send) + fallback (ws-send). No send-key because
	// ws-send appends Enter itself (requirement.md step 2.f).
	if len(calls) != 2 {
		t.Fatalf("Calls len = %d; want 2 (primary + fallback)", len(calls))
	}
	if calls[1].Name != "ws-send" {
		t.Errorf("fallback Name = %q; want %q", calls[1].Name, "ws-send")
	}
	wantFallback := []string{"workspace:7", "msg"}
	if !equalArgs(calls[1].Args, wantFallback) {
		t.Errorf("fallback Args = %v; want %v", calls[1].Args, wantFallback)
	}
}

// 8b: when the ws-send fallback is itself missing from PATH, Send must
// surface a diagnostic that names the missing binary so doctor / users
// can fix it. The resulting error wraps both legs so callers can still
// see the primary failure that triggered the fallback.
func TestSendDiagnosesMissingFallbackBinary(t *testing.T) {
	r := &fakeRunner{}
	r.queue(
		runResult{Err: errors.New("cmux send: exit 1")},
		runResult{Err: &exec.Error{Name: "ws-send", Err: exec.ErrNotFound}},
	)

	c := cmux.NewClient(cmux.WithRunner(r))
	err := c.Send(context.Background(), cmux.Workspace{ID: "workspace:7"}, "msg")
	if err == nil {
		t.Fatalf("Send returned nil; want error naming the missing fallback")
	}
	if !strings.Contains(err.Error(), "ws-send") {
		t.Errorf("err = %v; want it to mention the missing fallback binary name", err)
	}
	// The primary failure must still be visible in the chain so the caller
	// can see what triggered the fallback in the first place.
	if !strings.Contains(err.Error(), "cmux send") {
		t.Errorf("err = %v; want it to mention the primary cmux send failure", err)
	}
}

// 9: empty workspace ID is rejected up-front so we never shell out with "".
func TestSendRejectsEmptyWorkspace(t *testing.T) {
	r := &fakeRunner{}
	c := cmux.NewClient(cmux.WithRunner(r))
	err := c.Send(context.Background(), cmux.Workspace{}, "msg")
	if !errors.Is(err, cmux.ErrInvalidWorkspace) {
		t.Fatalf("err = %v; want ErrInvalidWorkspace", err)
	}
	if got := len(r.Calls()); got != 0 {
		t.Errorf("Runner invoked %d times; want 0", got)
	}
}

// scriptedProbe is a ReadinessProbe whose answer is driven by a sequence
// of bool flips, so WaitReady tests can express "not ready, not ready,
// ready" without sleeping.
type scriptedProbe struct {
	mu    sync.Mutex
	ready []bool
	calls int
}

func (p *scriptedProbe) IsReady(_ context.Context, _ cmux.Workspace) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if len(p.ready) == 0 {
		return false, nil
	}
	v := p.ready[0]
	p.ready = p.ready[1:]
	return v, nil
}

// 10: WaitReady returns nil as soon as the probe reports ready.
func TestWaitReadySucceedsWhenProbeFlipsToReady(t *testing.T) {
	probe := &scriptedProbe{ready: []bool{false, false, true}}
	c := cmux.NewClient(
		cmux.WithReadinessProbe(probe),
		cmux.WithStartupTimeout(time.Second),
		cmux.WithPollInterval(time.Millisecond),
	)
	err := c.WaitReady(context.Background(), cmux.Workspace{ID: "workspace:1"})
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if probe.calls < 3 {
		t.Errorf("probe calls = %d; want >= 3 (matches scripted ready slice)", probe.calls)
	}
}

// 11: WaitReady times out cleanly when the probe never flips.
func TestWaitReadyTimesOut(t *testing.T) {
	probe := &scriptedProbe{} // always returns false
	c := cmux.NewClient(
		cmux.WithReadinessProbe(probe),
		cmux.WithStartupTimeout(20*time.Millisecond),
		cmux.WithPollInterval(2*time.Millisecond),
	)
	err := c.WaitReady(context.Background(), cmux.Workspace{ID: "workspace:1"})
	if !errors.Is(err, cmux.ErrTimeout) {
		t.Fatalf("err = %v; want ErrTimeout", err)
	}
}

// 13: zero-valued pollInterval / startupTimeout from a buggy caller must
// not panic the runtime. The library defends with a fall-back to the
// package defaults so a misconfigured Option never crashes dispatch.
func TestWaitReadyRejectsNonPositiveTuning(t *testing.T) {
	t.Run("zero poll interval falls back to default", func(t *testing.T) {
		probe := &scriptedProbe{ready: []bool{true}}
		c := cmux.NewClient(
			cmux.WithReadinessProbe(probe),
			cmux.WithPollInterval(0),
			cmux.WithStartupTimeout(time.Second),
		)
		// Must not panic: NewTicker(0) would panic without the guard.
		if err := c.WaitReady(context.Background(), cmux.Workspace{ID: "workspace:1"}); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	})
	t.Run("zero startup timeout falls back to default", func(t *testing.T) {
		probe := &scriptedProbe{ready: []bool{true}}
		c := cmux.NewClient(
			cmux.WithReadinessProbe(probe),
			cmux.WithStartupTimeout(0),
			cmux.WithPollInterval(time.Millisecond),
		)
		if err := c.WaitReady(context.Background(), cmux.Workspace{ID: "workspace:1"}); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	})
}

// 12: parent ctx cancel short-circuits WaitReady.
func TestWaitReadyHonoursContextCancel(t *testing.T) {
	probe := &scriptedProbe{} // never ready
	c := cmux.NewClient(
		cmux.WithReadinessProbe(probe),
		cmux.WithStartupTimeout(time.Hour),
		cmux.WithPollInterval(time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	err := c.WaitReady(ctx, cmux.Workspace{ID: "workspace:1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
}

// 14 + 15: ListWorkspaces shells out with the documented argv and parses
// every line-leading "workspace:NNN" token from stdout.
func TestListWorkspacesShellsOutAndParsesIDs(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "workspace:101\nworkspace:202\n"})

	c := cmux.NewClient(cmux.WithRunner(r))
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ws) != 2 {
		t.Fatalf("len(ws) = %d; want 2 (got %v)", len(ws), ws)
	}
	if ws[0].ID != "workspace:101" || ws[1].ID != "workspace:202" {
		t.Errorf("ws = %v; want IDs [workspace:101 workspace:202]", ws)
	}
	calls := r.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls len = %d; want 1", len(calls))
	}
	if calls[0].Name != "cmux" {
		t.Errorf("Calls()[0].Name = %q; want %q", calls[0].Name, "cmux")
	}
	want := []string{"list-workspaces"}
	if !equalArgs(calls[0].Args, want) {
		t.Errorf("Args = %v; want %v", calls[0].Args, want)
	}
}

// 15b: a workspace banner that follows dashboard markers ("* workspace:7")
// or indentation must still be recognised. The leading-anchor regex is
// shared with the CLI clean path so both layers see the same set.
func TestListWorkspacesParsesIndentedBanners(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "* workspace:7 my-task\n  workspace:9 other\n"})

	c := cmux.NewClient(cmux.WithRunner(r))
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ws) != 2 || ws[0].ID != "workspace:7" || ws[1].ID != "workspace:9" {
		t.Errorf("ws = %v; want [workspace:7 workspace:9]", ws)
	}
}

// 16: empty stdout returns an empty (non-nil) slice so callers can
// `for _, w := range ws` without a nil check.
func TestListWorkspacesEmptyStdoutReturnsEmptySlice(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: ""})

	c := cmux.NewClient(cmux.WithRunner(r))
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if ws == nil {
		t.Fatalf("ws = nil; want non-nil empty slice")
	}
	if len(ws) != 0 {
		t.Errorf("len(ws) = %d; want 0", len(ws))
	}
}

// 17: missing cmux binary surfaces as the typed sentinel.
func TestListWorkspacesMapsMissingBinaryToErrCmuxNotFound(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Err: &exec.Error{Name: "cmux", Err: exec.ErrNotFound}})

	c := cmux.NewClient(cmux.WithRunner(r))
	_, err := c.ListWorkspaces(context.Background())
	if !errors.Is(err, cmux.ErrCmuxNotFound) {
		t.Fatalf("ListWorkspaces error = %v; want ErrCmuxNotFound", err)
	}
}

// 18b: cmux output containing duplicate "workspace:NNN" lines (an
// artifact of dashboard rendering or a future cmux variant) must not
// crash; the reaper turns the slice into a set anyway, but pinning
// "duplicates pass through verbatim" keeps the contract explicit so
// later set-construction logic stays opt-in.
func TestListWorkspacesPreservesDuplicates(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "workspace:7\nworkspace:7\nworkspace:9\n"})

	c := cmux.NewClient(cmux.WithRunner(r))
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ws) != 3 {
		t.Fatalf("len(ws) = %d; want 3 (duplicates preserved)", len(ws))
	}
}

// 18: a non-zero exit surfaces with stderr included in the message so
// the operator can diagnose without re-running cmux by hand.
func TestListWorkspacesWrapsExitErrorWithStderr(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stderr: "permission denied", Err: errors.New("exit 1")})

	c := cmux.NewClient(cmux.WithRunner(r))
	_, err := c.ListWorkspaces(context.Background())
	if err == nil {
		t.Fatalf("ListWorkspaces err = nil; want error wrapping stderr")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v; want it to mention stderr", err)
	}
}

// 19 + 20: ReadOutput shells out to `cmux read-screen --workspace <ws>` and returns trimmed stdout.
func TestReadOutputShellsOutAndReturnsTrimmedOutput(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "terminal output here\n"})

	c := cmux.NewClient(cmux.WithRunner(r))
	out, err := c.ReadOutput(context.Background(), cmux.Workspace{ID: "workspace:7"})
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if out != "terminal output here" {
		t.Errorf("out = %q; want %q", out, "terminal output here")
	}
	calls := r.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls len = %d; want 1", len(calls))
	}
	if calls[0].Name != "cmux" {
		t.Errorf("Calls()[0].Name = %q; want %q", calls[0].Name, "cmux")
	}
	want := []string{"read-screen", "--workspace", "workspace:7"}
	if !equalArgs(calls[0].Args, want) {
		t.Errorf("Args = %v; want %v", calls[0].Args, want)
	}
}

// 21: ReadOutput returns ErrInvalidWorkspace for empty workspace ID.
func TestReadOutputRejectsEmptyWorkspace(t *testing.T) {
	r := &fakeRunner{}
	c := cmux.NewClient(cmux.WithRunner(r))
	_, err := c.ReadOutput(context.Background(), cmux.Workspace{})
	if !errors.Is(err, cmux.ErrInvalidWorkspace) {
		t.Fatalf("err = %v; want ErrInvalidWorkspace", err)
	}
	if len(r.Calls()) != 0 {
		t.Errorf("Runner invoked %d times; want 0", len(r.Calls()))
	}
}

// 22: ReadOutput maps a missing binary to ErrCmuxNotFound.
func TestReadOutputMapsMissingBinaryToErrCmuxNotFound(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Err: &exec.Error{Name: "cmux", Err: exec.ErrNotFound}})

	c := cmux.NewClient(cmux.WithRunner(r))
	_, err := c.ReadOutput(context.Background(), cmux.Workspace{ID: "workspace:7"})
	if !errors.Is(err, cmux.ErrCmuxNotFound) {
		t.Fatalf("err = %v; want ErrCmuxNotFound", err)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
