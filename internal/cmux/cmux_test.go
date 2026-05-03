package cmux_test

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/cmux"
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
	r.queue(runResult{})

	c := cmux.NewClient(cmux.WithRunner(r))
	err := c.Send(context.Background(), cmux.Workspace{ID: "workspace:7"}, "hello world")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := r.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls len = %d; want 1", len(calls))
	}
	want := []string{"send", "workspace:7", "hello world"}
	if !equalArgs(calls[0].Args, want) {
		t.Errorf("Args = %v; want %v", calls[0].Args, want)
	}
}

// 7: newline runs collapse to a single space before being handed off.
func TestSendReplacesNewlinesWithSpaces(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{})

	c := cmux.NewClient(cmux.WithRunner(r))
	err := c.Send(context.Background(), cmux.Workspace{ID: "workspace:7"}, "line1\nline2\r\nline3")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := r.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls len = %d; want 1", len(calls))
	}
	got := calls[0].Args[len(calls[0].Args)-1]
	want := "line1 line2 line3"
	if got != want {
		t.Errorf("payload = %q; want %q", got, want)
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
