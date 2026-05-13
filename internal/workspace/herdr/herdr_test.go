package herdr_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/workspace"
	"github.com/haruotsu/marunage/internal/workspace/herdr"
)

// Test list (matches the cmux package's TDD checklist where the
// herdr CLI has the same operation; differences flagged):
//
//   1.  NewWorkspace shells out to `herdr workspace create` with the
//       documented flags (--cwd, --label, --no-focus) and follows up
//       with `herdr pane run <pane_id> <command>`.
//   2.  NewWorkspace parses the result.root_pane.pane_id from the
//       JSON stdout.
//   3.  NewWorkspace surfaces ErrHerdrNotFound when the runner reports
//       exec.ErrNotFound.
//   4.  NewWorkspace rejects opts that lack CWD / Command / Name with
//       ErrInvalidOptions (workspace.ErrInvalidOptions).
//   5.  NewWorkspace surfaces stdout that is not valid JSON or that
//       lacks pane_id as ErrUnparseableOutput.
//   6.  Send shells out to `herdr pane send-text <pane_id> <text>` and
//       then `herdr pane send-keys <pane_id> Enter` separately.
//   7.  Send rejects empty workspace IDs with ErrInvalidWorkspace.
//   8.  Send maps a missing herdr binary to ErrHerdrNotFound.
//   9.  ListWorkspaces shells out to `herdr pane list` and parses the
//       pane_ids out of the JSON result.
//  10.  ListWorkspaces maps a missing herdr binary to ErrHerdrNotFound.
//  11.  ListWorkspaces returns a non-nil empty slice for "no panes".
//  12.  ReadOutput shells out to `herdr pane read <pane_id> --source
//       recent --lines 1000` and returns the trimmed stdout.
//  13.  ReadOutput returns ErrInvalidWorkspace for empty workspace ID.
//  14.  WaitReady returns nil once the probe reports ready.
//  15.  WaitReady honours the startup timeout and returns ErrTimeout
//       when the probe never goes ready.

type callRecord struct {
	Name string
	Args []string
}

type runResult struct {
	Stdout string
	Stderr string
	Err    error
}

type fakeRunner struct {
	mu      sync.Mutex
	results []runResult
	calls   []callRecord
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

// Helper: a stdout snippet that herdr emits for `workspace create`.
const workspaceCreateOK = `{"id":"cli:workspace:create","result":{"type":"workspace_info","workspace":{"workspace_id":"w1","number":1,"label":"smoke"},"tab":{"tab_id":"w1:1"},"root_pane":{"pane_id":"w1-1","workspace_id":"w1","tab_id":"w1:1"}}}`

// 1 + 2: NewWorkspace shells out with documented flags and parses pane_id.
func TestNewWorkspaceShellsOutAndParsesPaneID(t *testing.T) {
	r := &fakeRunner{}
	r.queue(
		runResult{Stdout: workspaceCreateOK},
		runResult{Stdout: ""}, // pane run returns no JSON on success
	)

	c := herdr.NewClient(herdr.WithRunner(r))
	ws, err := c.NewWorkspace(context.Background(), herdr.NewWorkspaceOptions{
		CWD:     "/tmp/repo",
		Command: "claude --dangerously-skip-permissions",
		Name:    "#7 fix bug",
	})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if ws.ID != "w1-1" {
		t.Errorf("ws.ID = %q; want %q", ws.ID, "w1-1")
	}
	if ws.Name != "#7 fix bug" {
		t.Errorf("ws.Name = %q; want %q", ws.Name, "#7 fix bug")
	}

	calls := r.Calls()
	if len(calls) != 2 {
		t.Fatalf("Calls() len = %d; want 2", len(calls))
	}
	if calls[0].Name != "herdr" {
		t.Errorf("Calls()[0].Name = %q; want %q", calls[0].Name, "herdr")
	}
	got := strings.Join(calls[0].Args, " ")
	wantHas := []string{
		"workspace", "create",
		"--cwd", "/tmp/repo",
		"--label", "#7 fix bug",
		"--no-focus",
	}
	for _, w := range wantHas {
		if !contains(calls[0].Args, w) {
			t.Errorf("Calls()[0].Args missing %q; got %q", w, got)
		}
	}
	// second call: pane run
	if calls[1].Name != "herdr" || len(calls[1].Args) < 4 {
		t.Fatalf("Calls()[1] = %+v; want herdr pane run <pane_id> <cmd>", calls[1])
	}
	if calls[1].Args[0] != "pane" || calls[1].Args[1] != "run" || calls[1].Args[2] != "w1-1" {
		t.Errorf("Calls()[1].Args[:3] = %v; want [pane run w1-1]", calls[1].Args[:3])
	}
	if calls[1].Args[3] != "claude --dangerously-skip-permissions" {
		t.Errorf("Calls()[1].Args[3] = %q; want claude command verbatim", calls[1].Args[3])
	}
}

// 3: NewWorkspace surfaces ErrHerdrNotFound.
func TestNewWorkspaceMapsMissingBinaryToErrHerdrNotFound(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Err: &exec.Error{Name: "herdr", Err: exec.ErrNotFound}})

	c := herdr.NewClient(herdr.WithRunner(r))
	_, err := c.NewWorkspace(context.Background(), herdr.NewWorkspaceOptions{
		CWD:     "/tmp/repo",
		Command: "claude",
		Name:    "x",
	})
	if !errors.Is(err, herdr.ErrHerdrNotFound) {
		t.Fatalf("NewWorkspace error = %v; want ErrHerdrNotFound", err)
	}
	if !errors.Is(err, workspace.ErrBackendNotFound) {
		t.Errorf("err should also satisfy errors.Is(err, workspace.ErrBackendNotFound); got %v", err)
	}
}

// 3b: NewWorkspace tears down the just-created workspace when the
// follow-up `pane run` fails. Without this cleanup, a transient pane
// run failure would leak a herdr workspace and a half-spawned shell
// back to the user.
func TestNewWorkspaceClosesWorkspaceOnPaneRunFailure(t *testing.T) {
	r := &fakeRunner{}
	r.queue(
		runResult{Stdout: workspaceCreateOK},                        // workspace create succeeds
		runResult{Err: errors.New("pane run boom"), Stderr: "boom"}, // pane run fails
		runResult{}, // workspace close (best-effort)
	)

	c := herdr.NewClient(herdr.WithRunner(r))
	_, err := c.NewWorkspace(context.Background(), herdr.NewWorkspaceOptions{
		CWD: "/tmp/repo", Command: "claude", Name: "x",
	})
	if err == nil {
		t.Fatal("expected error; got nil")
	}

	calls := r.Calls()
	if len(calls) != 3 {
		t.Fatalf("Calls() len = %d; want 3 (create + run + close)", len(calls))
	}
	if calls[2].Args[0] != "workspace" || calls[2].Args[1] != "close" || calls[2].Args[2] != "w1" {
		t.Errorf("Calls()[2].Args = %v; want [workspace close w1]", calls[2].Args)
	}
}

// 4: NewWorkspace rejects incomplete options.
func TestNewWorkspaceValidatesRequiredOptions(t *testing.T) {
	cases := []struct {
		name string
		opts herdr.NewWorkspaceOptions
	}{
		{"missing CWD", herdr.NewWorkspaceOptions{Command: "claude", Name: "x"}},
		{"missing Command", herdr.NewWorkspaceOptions{CWD: "/tmp", Name: "x"}},
		{"missing Name", herdr.NewWorkspaceOptions{CWD: "/tmp", Command: "claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{}
			c := herdr.NewClient(herdr.WithRunner(r))
			_, err := c.NewWorkspace(context.Background(), tc.opts)
			if !errors.Is(err, herdr.ErrInvalidOptions) {
				t.Fatalf("err = %v; want ErrInvalidOptions", err)
			}
			if got := len(r.Calls()); got != 0 {
				t.Errorf("Runner invoked %d times on validation failure; want 0", got)
			}
		})
	}
}

// 5: NewWorkspace flags non-JSON / missing pane_id.
func TestNewWorkspaceFailsOnUnparseableStdout(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
	}{
		{"non-JSON", "boom: workspace create failed"},
		{"missing pane_id", `{"result":{"root_pane":{}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{}
			r.queue(runResult{Stdout: tc.stdout})
			c := herdr.NewClient(herdr.WithRunner(r))
			_, err := c.NewWorkspace(context.Background(), herdr.NewWorkspaceOptions{
				CWD: "/tmp/repo", Command: "claude", Name: "x",
			})
			if !errors.Is(err, herdr.ErrUnparseableOutput) {
				t.Fatalf("err = %v; want ErrUnparseableOutput", err)
			}
		})
	}
}

// 6: Send shells out to send-text then send-keys Enter.
func TestSendSplitsTextAndEnter(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{}, runResult{}) // send-text + send-keys both OK

	c := herdr.NewClient(herdr.WithRunner(r))
	if err := c.Send(context.Background(), herdr.Workspace{ID: "w1-1"}, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := r.Calls()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d; want 2", len(calls))
	}
	if calls[0].Args[0] != "pane" || calls[0].Args[1] != "send-text" || calls[0].Args[2] != "w1-1" || calls[0].Args[3] != "hello" {
		t.Errorf("Calls()[0].Args = %v; want [pane send-text w1-1 hello]", calls[0].Args)
	}
	if calls[1].Args[0] != "pane" || calls[1].Args[1] != "send-keys" || calls[1].Args[2] != "w1-1" || calls[1].Args[3] != "Enter" {
		t.Errorf("Calls()[1].Args = %v; want [pane send-keys w1-1 Enter]", calls[1].Args)
	}
}

// 7: Send rejects empty workspace IDs.
func TestSendRejectsEmptyWorkspaceID(t *testing.T) {
	r := &fakeRunner{}
	c := herdr.NewClient(herdr.WithRunner(r))
	if err := c.Send(context.Background(), herdr.Workspace{}, "x"); !errors.Is(err, herdr.ErrInvalidWorkspace) {
		t.Fatalf("err = %v; want ErrInvalidWorkspace", err)
	}
	if got := len(r.Calls()); got != 0 {
		t.Errorf("Runner invoked %d times; want 0", got)
	}
}

// 8a: Send maps missing binary on the first call (send-text).
func TestSendMapsMissingBinaryOnFirstCall(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Err: &exec.Error{Name: "herdr", Err: exec.ErrNotFound}})
	c := herdr.NewClient(herdr.WithRunner(r))
	err := c.Send(context.Background(), herdr.Workspace{ID: "w1-1"}, "x")
	if !errors.Is(err, herdr.ErrHerdrNotFound) {
		t.Fatalf("Send err = %v; want ErrHerdrNotFound", err)
	}
}

// 8b: Send maps missing binary on the second call (send-keys Enter).
// The first call succeeds; the second is where the binary suddenly
// disappears.
func TestSendMapsMissingBinaryOnSecondCall(t *testing.T) {
	r := &fakeRunner{}
	r.queue(
		runResult{}, // send-text succeeds
		runResult{Err: &exec.Error{Name: "herdr", Err: exec.ErrNotFound}}, // send-keys Enter fails
	)
	c := herdr.NewClient(herdr.WithRunner(r))
	err := c.Send(context.Background(), herdr.Workspace{ID: "w1-1"}, "x")
	if !errors.Is(err, herdr.ErrHerdrNotFound) {
		t.Fatalf("Send err = %v; want ErrHerdrNotFound", err)
	}
}

// 9 + 11: ListWorkspaces shells out and parses pane_ids; empty stdout panes returns non-nil slice.
func TestListWorkspacesParsesPaneIDs(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{
		Stdout: `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"w1-1"},{"pane_id":"w2-1"}]}}`,
	})
	c := herdr.NewClient(herdr.WithRunner(r))
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ws) != 2 || ws[0].ID != "w1-1" || ws[1].ID != "w2-1" {
		t.Errorf("ws = %+v; want two panes [w1-1 w2-1]", ws)
	}

	r2 := &fakeRunner{}
	r2.queue(runResult{Stdout: `{"result":{"panes":[]}}`})
	c2 := herdr.NewClient(herdr.WithRunner(r2))
	ws2, err := c2.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces (empty): %v", err)
	}
	if ws2 == nil {
		t.Fatalf("empty list returned nil slice; want non-nil empty")
	}
	if len(ws2) != 0 {
		t.Errorf("len(ws2) = %d; want 0", len(ws2))
	}
}

// 9b: ListWorkspaces rejects pane_list entries with an empty pane_id
// as ErrUnparseableOutput so a regression in herdr's JSON banner
// surfaces here rather than silently corrupting the reaper's orphan
// diff (an empty ID would short-circuit reaper into treating the row
// as alive).
func TestListWorkspacesRejectsEmptyPaneID(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{
		Stdout: `{"result":{"panes":[{"pane_id":"w1-1"},{"pane_id":""}]}}`,
	})
	c := herdr.NewClient(herdr.WithRunner(r))
	_, err := c.ListWorkspaces(context.Background())
	if !errors.Is(err, herdr.ErrUnparseableOutput) {
		t.Fatalf("err = %v; want ErrUnparseableOutput", err)
	}
}

// 10: ListWorkspaces missing binary.
func TestListWorkspacesMapsMissingBinaryToErrHerdrNotFound(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Err: &exec.Error{Name: "herdr", Err: exec.ErrNotFound}})
	c := herdr.NewClient(herdr.WithRunner(r))
	_, err := c.ListWorkspaces(context.Background())
	if !errors.Is(err, herdr.ErrHerdrNotFound) {
		t.Fatalf("err = %v; want ErrHerdrNotFound", err)
	}
}

// 12: ReadOutput shells out and returns trimmed stdout.
func TestReadOutputShellsOutWithDocumentedFlags(t *testing.T) {
	r := &fakeRunner{}
	r.queue(runResult{Stdout: "  hello\nworld  \n"})
	c := herdr.NewClient(herdr.WithRunner(r))
	got, err := c.ReadOutput(context.Background(), herdr.Workspace{ID: "w1-1"})
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if got != "hello\nworld" {
		t.Errorf("got = %q; want %q", got, "hello\nworld")
	}
	calls := r.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "pane" || calls[0].Args[1] != "read" || calls[0].Args[2] != "w1-1" {
		t.Fatalf("Calls()[0] = %+v; want herdr pane read w1-1 ...", calls[0])
	}
	if !contains(calls[0].Args, "recent") || !contains(calls[0].Args, "1000") {
		t.Errorf("Calls()[0].Args = %v; want --source recent --lines 1000", calls[0].Args)
	}
}

// 13: ReadOutput rejects empty workspace ID.
func TestReadOutputRejectsEmptyID(t *testing.T) {
	r := &fakeRunner{}
	c := herdr.NewClient(herdr.WithRunner(r))
	_, err := c.ReadOutput(context.Background(), herdr.Workspace{})
	if !errors.Is(err, herdr.ErrInvalidWorkspace) {
		t.Fatalf("err = %v; want ErrInvalidWorkspace", err)
	}
}

// 14: WaitReady returns nil once the probe is ready.
func TestWaitReadyReturnsWhenProbeReports(t *testing.T) {
	probe := workspace.ReadinessProbeFunc(func(_ context.Context, _ workspace.Workspace) (bool, error) {
		return true, nil
	})
	c := herdr.NewClient(
		herdr.WithRunner(&fakeRunner{}),
		herdr.WithReadinessProbe(probe),
		herdr.WithStartupTimeout(50*time.Millisecond),
		herdr.WithPollInterval(5*time.Millisecond),
	)
	if err := c.WaitReady(context.Background(), herdr.Workspace{ID: "w1-1"}); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

// 15: WaitReady times out when the probe never reports ready.
func TestWaitReadyTimesOut(t *testing.T) {
	probe := workspace.ReadinessProbeFunc(func(_ context.Context, _ workspace.Workspace) (bool, error) {
		return false, nil
	})
	c := herdr.NewClient(
		herdr.WithRunner(&fakeRunner{}),
		herdr.WithReadinessProbe(probe),
		herdr.WithStartupTimeout(20*time.Millisecond),
		herdr.WithPollInterval(5*time.Millisecond),
	)
	err := c.WaitReady(context.Background(), herdr.Workspace{ID: "w1-1"})
	if !errors.Is(err, herdr.ErrTimeout) {
		t.Fatalf("err = %v; want ErrTimeout", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
