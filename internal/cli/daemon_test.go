package cli

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// PR-71 daemon CLI test list — drives `marunage daemon {start|stop|status}`
// through the daemonControlHook seam so the test suite never spawns a
// real `marunage loop` subprocess (which would race against the test
// runner and potentially leak across test cases).

// fakeDaemon implements daemonControl. Each method records its call so
// tests can assert "Start was called once with these args"; the per-
// method err / status fields let cases inject errors or specific state
// without redefining the type per test.
type fakeDaemon struct {
	mu            sync.Mutex
	startCalls    [][]string
	stopCalls     []time.Duration
	statusCalls   int
	startResult   int
	startErr      error
	stopResult    int
	stopErr       error
	statusResp    daemonStatus
	statusErr     error
	logPathResult string
}

func (f *fakeDaemon) Start(args []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]string(nil), args...)
	f.startCalls = append(f.startCalls, cp)
	return f.startResult, f.startErr
}

func (f *fakeDaemon) Stop(timeout time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, timeout)
	return f.stopResult, f.stopErr
}

func (f *fakeDaemon) Status() (daemonStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return f.statusResp, f.statusErr
}

func (f *fakeDaemon) LogPath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logPathResult
}

func installFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	fd := &fakeDaemon{statusResp: daemonStatus{Path: "/tmp/test/daemon.pid"}}
	withDaemonControl(t, func(string) (daemonControl, error) {
		return fd, nil
	})
	return fd
}

// CLI4: `daemon start` calls Start with no extra args, prints the pid,
// exits 0.
func TestDaemonStart_PrintsPID(t *testing.T) {
	fd := installFakeDaemon(t)
	fd.startResult = 1234

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "start"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon start exit=%d; stderr=%q", code, stderr.String())
	}
	if len(fd.startCalls) != 1 {
		t.Fatalf("Start calls = %d; want 1", len(fd.startCalls))
	}
	if len(fd.startCalls[0]) != 0 {
		t.Errorf("Start args = %v; want empty (no --interval)", fd.startCalls[0])
	}
	if !strings.Contains(stdout.String(), "1234") {
		t.Errorf("stdout = %q; want it to mention pid 1234", stdout.String())
	}
}

// CLI4b: `daemon start --interval 30s` forwards the flag to the loop
// subcommand the daemon launches.
func TestDaemonStart_ForwardsInterval(t *testing.T) {
	fd := installFakeDaemon(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "start", "--interval", "30s"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon start --interval exit=%d; stderr=%q", code, stderr.String())
	}
	if len(fd.startCalls) != 1 {
		t.Fatalf("Start calls = %d; want 1", len(fd.startCalls))
	}
	args := fd.startCalls[0]
	if len(args) != 2 || args[0] != "--interval" || args[1] != "30s" {
		t.Errorf("Start args = %v; want [--interval 30s]", args)
	}
}

// CLI7: `daemon start` against an already-live daemon returns non-zero
// and the message names the existing pid so the operator knows what to
// stop.
func TestDaemonStart_RefusesWhenAlreadyRunning(t *testing.T) {
	fd := installFakeDaemon(t)
	fd.startErr = errors.New("daemon already running (pid=999, pidfile=/tmp/test/daemon.pid)")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "start"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("daemon start with already-running exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "already running") {
		t.Errorf("stderr = %q; want hint about already running", stderr.String())
	}
}

// CLI5: `daemon status` prints "running" + pid when the control reports
// Running=true.
func TestDaemonStatus_Running(t *testing.T) {
	fd := installFakeDaemon(t)
	fd.statusResp = daemonStatus{Running: true, PID: 7777, Path: "/tmp/test/daemon.pid"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon status exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "running") || !strings.Contains(out, "7777") {
		t.Errorf("stdout = %q; want running + pid 7777", out)
	}
}

// CLI5b: `daemon status` with no pidfile prints "stopped (no pidfile…)"
// so the operator can distinguish "never started" from "stale state".
func TestDaemonStatus_NoPidfile(t *testing.T) {
	installFakeDaemon(t) // statusResp left zero -> not running, pid 0

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon status (no pidfile) exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "stopped") {
		t.Errorf("stdout = %q; want it to mention 'stopped'", stdout.String())
	}
}

// CLI5c: `daemon status` with stale pidfile (pid present but not running)
// labels the state explicitly so the operator knows to remove the file.
func TestDaemonStatus_StalePidfile(t *testing.T) {
	fd := installFakeDaemon(t)
	fd.statusResp = daemonStatus{Running: false, PID: 4242, Path: "/tmp/test/daemon.pid"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon status (stale) exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "stale") || !strings.Contains(out, "4242") {
		t.Errorf("stdout = %q; want 'stale' + pid 4242", out)
	}
}

// CLI6: `daemon stop` calls Stop(timeout=daemonStopTimeout) and prints
// the stopped pid.
func TestDaemonStop_CallsStop(t *testing.T) {
	fd := installFakeDaemon(t)
	fd.stopResult = 7777

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "stop"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon stop exit=%d; stderr=%q", code, stderr.String())
	}
	if len(fd.stopCalls) != 1 {
		t.Fatalf("Stop calls = %d; want 1", len(fd.stopCalls))
	}
	if fd.stopCalls[0] != daemonStopTimeout {
		t.Errorf("Stop timeout = %v; want %v", fd.stopCalls[0], daemonStopTimeout)
	}
	if !strings.Contains(stdout.String(), "7777") {
		t.Errorf("stdout = %q; want it to mention pid 7777", stdout.String())
	}
}

// CLI6b: `daemon stop` with no live daemon surfaces the underlying
// error verbatim so the operator sees what state the file system was
// in.
func TestDaemonStop_PropagatesError(t *testing.T) {
	fd := installFakeDaemon(t)
	fd.stopErr = errors.New("daemon: no pidfile at /tmp/test/daemon.pid")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "stop"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("daemon stop with error exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "no pidfile") {
		t.Errorf("stderr = %q; want it to mention the underlying error", stderr.String())
	}
}
