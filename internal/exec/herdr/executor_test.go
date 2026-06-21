package herdr_test

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
	execherdr "github.com/haruotsu/marunage/internal/exec/herdr"
)

// readyPane is a `pane read` snapshot that satisfies the Claude readiness
// banner the herdr executor polls for during Start.
const readyPane = "Welcome to Claude Code v1.2.3\n❯ "

// createOK is a plausible `herdr workspace create` JSON response carrying the
// root pane id the executor harvests.
const createOK = `{"result":{"workspace":{"workspace_id":"1"},"tab":{"tab_id":"1"},"root_pane":{"pane_id":"1-1"}}}`

type runCall struct {
	name string
	args []string
}

// herdrFake is a scriptable execherdr.Runner stand-in so these tests never
// shell out to a real herdr. It dispatches canned output per herdr subcommand
// (keyed on the first two args, e.g. "workspace create" / "pane read").
type herdrFake struct {
	mu    sync.Mutex
	calls []runCall

	createOut string
	createErr error

	runErr    error
	runStderr string

	read       []string
	readIdx    int
	readErr    error
	readStderr string

	sendErr error

	listOut string
	listErr error
}

func (f *herdrFake) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, runCall{name: name, args: args})

	key := ""
	if len(args) >= 2 {
		key = args[0] + " " + args[1]
	} else if len(args) == 1 {
		key = args[0]
	}
	switch key {
	case "workspace create":
		return []byte(f.createOut), nil, f.createErr
	case "workspace close":
		return nil, nil, nil
	case "pane run":
		return nil, []byte(f.runStderr), f.runErr
	case "pane send-text", "pane send-keys":
		return nil, nil, f.sendErr
	case "pane read":
		if f.readErr != nil {
			return nil, []byte(f.readStderr), f.readErr
		}
		if len(f.read) == 0 {
			return nil, nil, nil
		}
		i := f.readIdx
		if i >= len(f.read) {
			i = len(f.read) - 1
		} else {
			f.readIdx++
		}
		return []byte(f.read[i]), nil, nil
	case "pane list":
		return []byte(f.listOut), nil, f.listErr
	}
	return nil, nil, nil
}

func (f *herdrFake) callsFor(prefix ...string) []runCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runCall
	for _, c := range f.calls {
		if len(c.args) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c.args[i] != p {
				match = false
				break
			}
		}
		if match {
			out = append(out, c)
		}
	}
	return out
}

func fastOpts(r execherdr.Runner) []execherdr.Option {
	return []execherdr.Option{
		execherdr.WithRunner(r),
		execherdr.WithPollInterval(time.Millisecond),
		execherdr.WithStartupTimeout(200 * time.Millisecond),
	}
}

func TestStartCreatesWorkspaceAndLaunchesClaude(t *testing.T) {
	fr := &herdrFake{createOut: createOK, read: []string{readyPane}}
	e := execherdr.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{
		Cwd:     "/tmp/work",
		Command: "claude --dangerously-skip-permissions",
		Name:    "#1 buy milk",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.ID != "1-1" {
		t.Errorf("session ID = %q; want 1-1 (root pane id)", sess.ID)
	}
	creates := fr.callsFor("workspace", "create")
	if len(creates) != 1 {
		t.Fatalf("workspace create calls = %d; want 1", len(creates))
	}
	joined := strings.Join(creates[0].args, " ")
	for _, want := range []string{"--no-focus", "--cwd /tmp/work", "--label #1 buy milk"} {
		if !strings.Contains(joined, want) {
			t.Errorf("workspace create args %q missing %q", joined, want)
		}
	}
	runs := fr.callsFor("pane", "run")
	if len(runs) != 1 {
		t.Fatalf("pane run calls = %d; want 1", len(runs))
	}
	runArgs := strings.Join(runs[0].args, " ")
	if !strings.Contains(runArgs, "1-1") || !strings.Contains(runArgs, "claude --dangerously-skip-permissions") {
		t.Errorf("pane run args %q; want the command addressed to pane 1-1", runArgs)
	}
}

func TestStartPrefersRootPaneOverLexicographicallyFirst(t *testing.T) {
	// A create response that carries more than the root pane must still launch
	// Claude in (and address) the root pane, not whichever pane id happens to
	// sort first. herdr documents the root pane at result.root_pane.pane_id.
	fr := &herdrFake{
		createOut: `{"result":{"root_pane":{"pane_id":"2-1"},"panes":[{"pane_id":"1-1"}]}}`,
		read:      []string{readyPane},
	}
	e := execherdr.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "claude", Name: "n"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.ID != "2-1" {
		t.Errorf("session ID = %q; want 2-1 (root pane, not lexicographically-first 1-1)", sess.ID)
	}
	runArgs := strings.Join(fr.callsFor("pane", "run")[0].args, " ")
	if !strings.Contains(runArgs, "2-1") {
		t.Errorf("pane run args %q; want the command addressed to root pane 2-1", runArgs)
	}
}

func TestStartOmitsLabelWhenNameBlank(t *testing.T) {
	fr := &herdrFake{createOut: createOK, read: []string{readyPane}}
	e := execherdr.New(fastOpts(fr)...)
	if _, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "claude"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	joined := strings.Join(fr.callsFor("workspace", "create")[0].args, " ")
	if strings.Contains(joined, "--label") {
		t.Errorf("workspace create args %q should omit --label when Name is blank", joined)
	}
}

func TestStartReturnsZeroSessionWhenCreateFails(t *testing.T) {
	fr := &herdrFake{createErr: errors.New("herdr boom")}
	e := execherdr.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if err == nil {
		t.Fatal("Start err = nil; want create failure")
	}
	if sess.ID != "" {
		t.Errorf("session ID = %q; want empty (nothing created → retryable)", sess.ID)
	}
	if len(fr.callsFor("pane", "run")) != 0 {
		t.Error("pane run called despite create failure")
	}
}

func TestStartRejectsUnparseableCreateOutput(t *testing.T) {
	fr := &herdrFake{createOut: `{"result":{}}`, read: []string{readyPane}}
	e := execherdr.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if !errors.Is(err, execherdr.ErrUnparseableOutput) {
		t.Fatalf("Start err = %v; want ErrUnparseableOutput", err)
	}
	if sess.ID != "" {
		t.Errorf("session ID = %q; want empty (no addressable pane → retryable)", sess.ID)
	}
}

func TestStartClosesWorkspaceWhenRunFails(t *testing.T) {
	fr := &herdrFake{createOut: createOK, runErr: errors.New("pane run boom")}
	e := execherdr.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if err == nil {
		t.Fatal("Start err = nil; want pane run failure")
	}
	if sess.ID != "" {
		t.Errorf("session ID = %q; want empty (workspace cleaned up → retryable)", sess.ID)
	}
	closes := fr.callsFor("workspace", "close")
	if len(closes) != 1 {
		t.Fatalf("workspace close calls = %d; want 1 (leak cleanup)", len(closes))
	}
	if got := closes[0].args[len(closes[0].args)-1]; got != "1" {
		t.Errorf("workspace close ref = %q; want \"1\" (derived from pane 1-1)", got)
	}
}

func TestStartSkipsCloseWhenPaneIDHasNoWorkspaceRef(t *testing.T) {
	// A pane id without a "-" yields no workspace ref, so the leak-cleanup
	// close must be skipped rather than issued against a bogus workspace.
	fr := &herdrFake{
		createOut: `{"result":{"root_pane":{"pane_id":"solo"}}}`,
		runErr:    errors.New("pane run boom"),
	}
	e := execherdr.New(fastOpts(fr)...)

	if _, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"}); err == nil {
		t.Fatal("Start err = nil; want pane run failure")
	}
	if n := len(fr.callsFor("workspace", "close")); n != 0 {
		t.Errorf("workspace close calls = %d; want 0 (no workspace ref derivable from %q)", n, "solo")
	}
}

func TestStartMapsBinaryNotFoundOnPaneRun(t *testing.T) {
	fr := &herdrFake{createOut: createOK, runErr: osexec.ErrNotFound}
	e := execherdr.New(fastOpts(fr)...)
	_, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if !errors.Is(err, execherdr.ErrHerdrNotFound) {
		t.Errorf("Start err = %v; want ErrHerdrNotFound when pane run reports a missing binary", err)
	}
}

func TestStartReturnsPopulatedSessionWhenReadinessFails(t *testing.T) {
	fr := &herdrFake{createOut: createOK, read: []string{"booting..."}}
	e := execherdr.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "task-n"})
	if !errors.Is(err, execherdr.ErrStartupTimeout) {
		t.Fatalf("Start err = %v; want ErrStartupTimeout", err)
	}
	if sess.ID != "1-1" {
		t.Errorf("session ID = %q; want 1-1 (created pane, preserved for reaper)", sess.ID)
	}
}

func TestStartPropagatesFatalProbeError(t *testing.T) {
	fr := &herdrFake{createOut: createOK, readErr: errors.New("read failed"), readStderr: "pane_not_found: 1-1"}
	e := execherdr.New(fastOpts(fr)...)

	sess, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if err == nil {
		t.Fatal("Start err = nil; want fatal probe failure")
	}
	if errors.Is(err, execherdr.ErrStartupTimeout) {
		t.Errorf("Start err = %v; want the fatal probe error, not a timeout", err)
	}
	if sess.ID != "1-1" {
		t.Errorf("session ID = %q; want 1-1 (pane preserved for reaper)", sess.ID)
	}
}

func TestStartHonoursContextCancelDuringReadiness(t *testing.T) {
	fr := &herdrFake{createOut: createOK, read: []string{"booting..."}}
	e := execherdr.New(fastOpts(fr)...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.Start(ctx, exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Start err = %v; want context.Canceled", err)
	}
}

func TestStartMapsBinaryNotFound(t *testing.T) {
	fr := &herdrFake{createErr: osexec.ErrNotFound}
	e := execherdr.New(fastOpts(fr)...)
	_, err := e.Start(context.Background(), exec.SessionSpec{Cwd: "/tmp", Command: "c", Name: "n"})
	if !errors.Is(err, execherdr.ErrHerdrNotFound) {
		t.Errorf("Start err = %v; want ErrHerdrNotFound", err)
	}
}

func TestSendTypesAndSubmits(t *testing.T) {
	fr := &herdrFake{}
	e := execherdr.New(execherdr.WithRunner(fr))

	if err := e.Send(context.Background(), exec.NewSession("1-1", nil), "line1\nline2"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	texts := fr.callsFor("pane", "send-text")
	if len(texts) != 1 {
		t.Fatalf("pane send-text calls = %d; want 1", len(texts))
	}
	textArgs := strings.Join(texts[0].args, " ")
	if !strings.Contains(textArgs, "1-1") || !strings.Contains(textArgs, "line1 line2") {
		t.Errorf("send-text = %q; want folded text addressed to pane 1-1", textArgs)
	}
	if strings.Contains(textArgs, "line1\nline2") {
		t.Errorf("send-text = %q; newlines not folded", textArgs)
	}
	keys := fr.callsFor("pane", "send-keys")
	if len(keys) != 1 || !strings.Contains(strings.Join(keys[0].args, " "), "Enter") {
		t.Errorf("send-keys = %v; want a single Enter submit", keys)
	}
}

func TestSendRejectsEmptySession(t *testing.T) {
	fr := &herdrFake{}
	e := execherdr.New(execherdr.WithRunner(fr))
	if err := e.Send(context.Background(), exec.NewSession("", nil), "hi"); err == nil {
		t.Error("Send err = nil; want rejection of empty session id")
	}
	if len(fr.callsFor("pane", "send-text")) != 0 {
		t.Error("send-text called for empty session; want none")
	}
}

func TestSendMapsBinaryNotFound(t *testing.T) {
	fr := &herdrFake{sendErr: osexec.ErrNotFound}
	e := execherdr.New(execherdr.WithRunner(fr))
	err := e.Send(context.Background(), exec.NewSession("1-1", nil), "hi")
	if !errors.Is(err, execherdr.ErrHerdrNotFound) {
		t.Errorf("Send err = %v; want ErrHerdrNotFound", err)
	}
}

func TestAttachReturnsFocusCommand(t *testing.T) {
	e := execherdr.New(execherdr.WithRunner(&herdrFake{}))
	link, err := e.Attach(context.Background(), exec.NewSession("2-3", nil))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !strings.Contains(link, "2-3") || !strings.Contains(link, "focus") {
		t.Errorf("attach = %q; want a herdr pane focus command for 2-3", link)
	}
}

func TestAttachRejectsEmptySession(t *testing.T) {
	e := execherdr.New(execherdr.WithRunner(&herdrFake{}))
	if _, err := e.Attach(context.Background(), exec.NewSession("", nil)); err == nil {
		t.Error("Attach err = nil; want rejection of empty session id")
	}
}

func TestReadOutputReadsPane(t *testing.T) {
	fr := &herdrFake{read: []string{"  hello pane  "}}
	e := execherdr.New(execherdr.WithRunner(fr))
	out, err := e.ReadOutput(context.Background(), exec.NewSession("1-1", nil))
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if out != "hello pane" {
		t.Errorf("ReadOutput = %q; want trimmed \"hello pane\"", out)
	}
	reads := fr.callsFor("pane", "read")
	if len(reads) != 1 {
		t.Fatalf("pane read calls = %d; want 1", len(reads))
	}
	joined := strings.Join(reads[0].args, " ")
	if !strings.Contains(joined, "--source recent") {
		t.Errorf("pane read args %q; want --source recent", joined)
	}
	if !strings.Contains(joined, "--lines 1000") {
		t.Errorf("pane read args %q; want the default --lines 1000", joined)
	}
}

func TestReadOutputRejectsEmptySession(t *testing.T) {
	e := execherdr.New(execherdr.WithRunner(&herdrFake{}))
	if _, err := e.ReadOutput(context.Background(), exec.NewSession("", nil)); err == nil {
		t.Error("ReadOutput err = nil; want rejection of empty session id")
	}
}

func TestListSessionsParsesPaneIDs(t *testing.T) {
	fr := &herdrFake{listOut: `{"result":{"panes":[{"pane_id":"1-1"},{"pane_id":"2-1"}]}}`}
	e := execherdr.New(execherdr.WithRunner(fr))
	sessions, err := e.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != "1-1" || sessions[1].ID != "2-1" {
		t.Errorf("sessions = %+v; want ids 1-1, 2-1", sessions)
	}
}

func TestListSessionsSortsPaneIDs(t *testing.T) {
	// Feed the ids in reverse so the test fails if the harvester ever stops
	// sorting — the reaper diffs this set and wants a deterministic order.
	fr := &herdrFake{listOut: `{"result":{"panes":[{"pane_id":"2-1"},{"pane_id":"1-1"}]}}`}
	e := execherdr.New(execherdr.WithRunner(fr))
	sessions, err := e.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != "1-1" || sessions[1].ID != "2-1" {
		t.Errorf("sessions = %+v; want sorted ids 1-1, 2-1", sessions)
	}
}

func TestListSessionsTolerantOfNestedLayout(t *testing.T) {
	// pane_id buried under an unexpected nesting must still be harvested; this
	// pins the layout-tolerant recursion rather than a fixed top-level path.
	fr := &herdrFake{listOut: `{"result":{"workspaces":[{"tabs":[{"panes":[{"pane_id":"3-7"}]}]}]}}`}
	e := execherdr.New(execherdr.WithRunner(fr))
	sessions, err := e.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "3-7" {
		t.Errorf("sessions = %+v; want the deeply-nested pane id 3-7", sessions)
	}
}

func TestListSessionsEmptyWhenNoServer(t *testing.T) {
	fr := &herdrFake{listErr: errors.New("server not running")}
	e := execherdr.New(execherdr.WithRunner(fr))
	sessions, err := e.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %+v; want empty for no-server", sessions)
	}
}

func TestListSessionsEmptyWhenNoPanes(t *testing.T) {
	fr := &herdrFake{listOut: `{"result":{"panes":[]}}`}
	e := execherdr.New(execherdr.WithRunner(fr))
	sessions, err := e.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %+v; want empty for no panes", sessions)
	}
}

func TestListSessionsRejectsEmptyPaneID(t *testing.T) {
	fr := &herdrFake{listOut: `{"result":{"panes":[{"pane_id":""}]}}`}
	e := execherdr.New(execherdr.WithRunner(fr))
	_, err := e.ListSessions(context.Background())
	if !errors.Is(err, execherdr.ErrUnparseableOutput) {
		t.Errorf("ListSessions err = %v; want ErrUnparseableOutput for empty pane_id", err)
	}
}

func TestListSessionsRejectsMalformedJSON(t *testing.T) {
	fr := &herdrFake{listOut: `not json`}
	e := execherdr.New(execherdr.WithRunner(fr))
	_, err := e.ListSessions(context.Background())
	if !errors.Is(err, execherdr.ErrUnparseableOutput) {
		t.Errorf("ListSessions err = %v; want ErrUnparseableOutput for malformed JSON", err)
	}
}

func TestAwaitExitReadsSentinel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exec.SentinelFile), []byte("3\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	e := execherdr.New(execherdr.WithRunner(&herdrFake{}), execherdr.WithPollInterval(time.Millisecond))
	code, err := e.AwaitExit(context.Background(), exec.NewSession("1-1", execherdr.Handle{SentinelDir: dir}))
	if err != nil {
		t.Fatalf("AwaitExit: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d; want 3", code)
	}
}

func TestAwaitExitWithoutSentinelDir(t *testing.T) {
	e := execherdr.New(execherdr.WithRunner(&herdrFake{}))
	_, err := e.AwaitExit(context.Background(), exec.NewSession("1-1", nil))
	if !errors.Is(err, exec.ErrNoSentinelDir) {
		t.Errorf("err = %v; want ErrNoSentinelDir", err)
	}
}

func TestStreamEmitsOnChange(t *testing.T) {
	fr := &herdrFake{read: []string{"a", "a", "a\nb"}}
	e := execherdr.New(execherdr.WithRunner(fr), execherdr.WithPollInterval(time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := e.Stream(ctx, exec.NewSession("1-1", nil))
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
	fr := &herdrFake{read: []string{"x"}}
	e := execherdr.New(execherdr.WithRunner(fr), execherdr.WithPollInterval(time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := e.Stream(ctx, exec.NewSession("1-1", nil))
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
	fr := &herdrFake{readErr: errors.New("pane gone")}
	e := execherdr.New(execherdr.WithRunner(fr), execherdr.WithPollInterval(time.Millisecond))
	ch, err := e.Stream(context.Background(), exec.NewSession("1-1", nil))
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
