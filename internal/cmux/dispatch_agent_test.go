package cmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeResult holds a canned response for a specific subcommand.
type fakeResult struct {
	resp string
	err  error
}

// fakeAgentRunner records calls and returns configured results.
// perSubcmd maps the first arg (subcommand) to a specific result;
// resp/err are the fallback for subcommands not in perSubcmd.
type fakeAgentRunner struct {
	calls     [][]string
	resp      string
	err       error
	perSubcmd map[string]fakeResult
}

func (f *fakeAgentRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	if r, ok := f.perSubcmd[sub]; ok {
		if r.err != nil {
			return nil, []byte(r.err.Error()), r.err
		}
		return []byte(r.resp), nil, nil
	}
	if f.err != nil {
		return nil, []byte(f.err.Error()), f.err
	}
	return []byte(f.resp), nil, nil
}

func TestDispatchAgent_Enqueue_WritesFile(t *testing.T) {
	dir := t.TempDir()
	agent := &DispatchAgent{queueDir: dir}
	if err := agent.Enqueue(42); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("want 1 file in queue, got %d", len(entries))
	}
	if entries[0].Name() != "42.dispatch" {
		t.Errorf("file name = %q; want 42.dispatch", entries[0].Name())
	}
}

func TestDispatchAgent_Enqueue_CreatesQueueDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "queue")
	agent := &DispatchAgent{queueDir: dir}
	if err := agent.Enqueue(7); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("queue dir not created: %v", err)
	}
}

func TestDispatchAgent_Start_ErrNoCmux(t *testing.T) {
	fr := &fakeAgentRunner{err: errors.New("Broken pipe")}
	agent := &DispatchAgent{
		queueDir: t.TempDir(),
		wsFile:   filepath.Join(t.TempDir(), "agent.ws"),
		runner:   fr,
		exePath:  "/usr/local/bin/marunage",
		cfgPath:  "/etc/marunage/config.toml",
	}
	err := agent.Start(context.Background())
	if !errors.Is(err, ErrNoCmuxSession) {
		t.Fatalf("Start: err = %v; want ErrNoCmuxSession", err)
	}
}

// D4: Start calls new-workspace when no previous workspace file exists.
func TestDispatchAgent_Start_CallsNewWorkspace(t *testing.T) {
	wsDir := t.TempDir()
	fr := &fakeAgentRunner{
		perSubcmd: map[string]fakeResult{
			"list-workspaces": {resp: ""},
			"new-workspace":   {resp: "OK workspace:99\n"},
		},
	}
	agent := &DispatchAgent{
		queueDir: t.TempDir(),
		wsFile:   filepath.Join(wsDir, "agent.ws"),
		runner:   fr,
		exePath:  "/usr/bin/marunage",
		cfgPath:  "/home/user/.marunage/config.toml",
	}
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	found := false
	for _, call := range fr.calls {
		if len(call) > 1 && call[1] == "new-workspace" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("new-workspace not called; calls = %v", fr.calls)
	}
	data, err := os.ReadFile(agent.wsFile)
	if err != nil {
		t.Fatalf("wsFile not written: %v", err)
	}
	if string(data) != "workspace:99" {
		t.Errorf("wsFile = %q; want workspace:99", string(data))
	}
}

// D4b: Start skips new-workspace when the recorded workspace is still alive.
func TestDispatchAgent_Start_ReusesAliveWorkspace(t *testing.T) {
	wsDir := t.TempDir()
	wsFile := filepath.Join(wsDir, "agent.ws")
	if err := os.WriteFile(wsFile, []byte("workspace:42"), 0o600); err != nil {
		t.Fatalf("setup wsFile: %v", err)
	}
	fr := &fakeAgentRunner{
		perSubcmd: map[string]fakeResult{
			"list-workspaces": {resp: "workspace:42\n"},
			// new-workspace intentionally absent: if called, returns "" which
			// triggers a parse error and causes the test to fail.
		},
	}
	agent := &DispatchAgent{
		queueDir: t.TempDir(),
		wsFile:   wsFile,
		runner:   fr,
		exePath:  "/usr/local/bin/marunage",
		cfgPath:  "/etc/marunage/config.toml",
	}
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, call := range fr.calls {
		if len(call) > 1 && call[1] == "new-workspace" {
			t.Errorf("new-workspace called unexpectedly when workspace is alive; calls=%v", fr.calls)
		}
	}
	data, _ := os.ReadFile(wsFile)
	if string(data) != "workspace:42" {
		t.Errorf("wsFile changed to %q; want workspace:42 (should not overwrite alive workspace)", string(data))
	}
}

// D5b: Start returns ErrCmuxNotFound when the cmux binary is missing from PATH.
func TestDispatchAgent_Start_ErrCmuxNotFound_WhenBinaryMissing(t *testing.T) {
	fr := &fakeAgentRunner{err: &exec.Error{Name: "cmux", Err: exec.ErrNotFound}}
	agent := &DispatchAgent{
		queueDir: t.TempDir(),
		wsFile:   filepath.Join(t.TempDir(), "agent.ws"),
		runner:   fr,
		exePath:  "/usr/local/bin/marunage",
		cfgPath:  "/etc/marunage/config.toml",
	}
	err := agent.Start(context.Background())
	if !errors.Is(err, ErrCmuxNotFound) {
		t.Fatalf("Start: err = %v; want ErrCmuxNotFound", err)
	}
}

func TestDispatchAgent_Dispatch_WritesToQueue(t *testing.T) {
	dir := t.TempDir()
	agent := &DispatchAgent{queueDir: dir}
	if err := agent.Dispatch(context.Background(), 55); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// Verify the file was written
	name := filepath.Join(dir, strconv.FormatInt(55, 10)+".dispatch")
	if _, err := os.Stat(name); err != nil {
		t.Errorf("dispatch file not created: %v", err)
	}
}
