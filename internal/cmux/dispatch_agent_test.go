package cmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeAgentRunner records calls and returns configured results.
type fakeAgentRunner struct {
	calls [][]string
	resp  string
	err   error
}

func (f *fakeAgentRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return nil, []byte(f.err.Error()), f.err
	}
	return []byte(f.resp), nil, nil
}

// D1
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

// D2
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

// D3
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

// D4
func TestDispatchAgent_Start_CallsNewWorkspace(t *testing.T) {
	wsDir := t.TempDir()
	fr := &fakeAgentRunner{resp: "OK workspace:99\n"}
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
	if len(fr.calls) == 0 {
		t.Fatal("no cmux calls made")
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
	// workspace ID should be written to wsFile
	data, err := os.ReadFile(agent.wsFile)
	if err != nil {
		t.Fatalf("wsFile not written: %v", err)
	}
	if string(data) != "workspace:99" {
		t.Errorf("wsFile = %q; want workspace:99", string(data))
	}
}

// D5
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
