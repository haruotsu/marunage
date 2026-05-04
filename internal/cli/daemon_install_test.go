package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test list for PR-300 daemon full implementation (install/uninstall/restart/logs).
//
//  CLI-R1. daemon restart calls Stop then Start in that order
//  CLI-R2. daemon restart aborts (does not call Start) when Stop fails
//  CLI-R3. daemon restart prints the stopped pid and the new pid
//  CLI-I1. daemon install calls installer.Install and prints "installed"
//  CLI-I2. daemon install propagates installer error to stderr, returns non-zero
//  CLI-U1. daemon uninstall calls installer.Uninstall and prints "uninstalled"
//  CLI-U2. daemon uninstall propagates installer error to stderr, returns non-zero
//  CLI-L1. daemon logs reads the log file and prints its content
//  CLI-L2. daemon logs returns non-zero when the log file does not exist
//  CLI-E1. ErrAlreadyRunning is wrapped in the "already running" error so
//           callers can use errors.Is(err, ErrAlreadyRunning)

// --- fake installer ---

type installCall struct {
	exePath, configPath, logPath string
}

type fakeInstaller struct {
	mu             sync.Mutex
	installCalls   []installCall
	uninstallCalls int
	installErr     error
	uninstallErr   error
}

func (f *fakeInstaller) Install(exe, cfg, log string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installCalls = append(f.installCalls, installCall{exe, cfg, log})
	return f.installErr
}

func (f *fakeInstaller) Uninstall() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uninstallCalls++
	return f.uninstallErr
}

func installFakeInstaller(t *testing.T) *fakeInstaller {
	t.Helper()
	fi := &fakeInstaller{}
	withDaemonInstaller(t, func(string) (daemonInstaller, error) {
		return fi, nil
	})
	return fi
}

// --- CLI-R1 ---

func TestDaemonRestart_StopsThenStarts(t *testing.T) {

	fd := installFakeDaemon(t)
	fd.stopResult = 111
	fd.startResult = 222

	var order []string
	origStop := fd.stopCalls
	_ = origStop

	fd2 := &orderedFakeDaemon{fakeDaemon: fd, order: &order}
	withDaemonControl(t, func(string) (daemonControl, error) {
		return fd2, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "restart"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon restart exit=%d; stderr=%q", code, stderr.String())
	}
	if len(order) < 2 || order[0] != "stop" || order[1] != "start" {
		t.Errorf("call order = %v; want [stop start]", order)
	}
}

// orderedFakeDaemon wraps fakeDaemon and records call order.
type orderedFakeDaemon struct {
	*fakeDaemon
	order *[]string
}

func (o *orderedFakeDaemon) Stop(d time.Duration) (int, error) {
	*o.order = append(*o.order, "stop")
	return o.fakeDaemon.Stop(d)
}

func (o *orderedFakeDaemon) Start(args []string) (int, error) {
	*o.order = append(*o.order, "start")
	return o.fakeDaemon.Start(args)
}

// --- CLI-R2 ---

func TestDaemonRestart_AbortOnStopError(t *testing.T) {

	fd := installFakeDaemon(t)
	fd.stopErr = errors.New("no pidfile")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "restart"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("daemon restart with stop error exit=0; want non-zero")
	}
	if len(fd.startCalls) != 0 {
		t.Errorf("Start called %d times after Stop error; want 0", len(fd.startCalls))
	}
}

// --- CLI-R3 ---

func TestDaemonRestart_PrintsOldAndNewPID(t *testing.T) {

	fd := installFakeDaemon(t)
	fd.stopResult = 4321
	fd.startResult = 5678

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "restart"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon restart exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "4321") {
		t.Errorf("stdout = %q; want old pid 4321", out)
	}
	if !strings.Contains(out, "5678") {
		t.Errorf("stdout = %q; want new pid 5678", out)
	}
}

// --- CLI-I1 ---

func TestDaemonInstall_CallsInstaller(t *testing.T) {

	fd := installFakeDaemon(t)
	fd.logPathResult = "/tmp/test/daemon.log"
	fi := installFakeInstaller(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "install"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon install exit=%d; stderr=%q", code, stderr.String())
	}
	if len(fi.installCalls) != 1 {
		t.Fatalf("Install calls = %d; want 1", len(fi.installCalls))
	}
	if fi.installCalls[0].logPath != "/tmp/test/daemon.log" {
		t.Errorf("logPath = %q; want /tmp/test/daemon.log", fi.installCalls[0].logPath)
	}
	if !strings.Contains(stdout.String(), "installed") {
		t.Errorf("stdout = %q; want 'installed'", stdout.String())
	}
}

// --- CLI-I2 ---

func TestDaemonInstall_PropagatesError(t *testing.T) {

	fd := installFakeDaemon(t)
	fd.logPathResult = "/tmp/test/daemon.log"
	fi := installFakeInstaller(t)
	fi.installErr = errors.New("permission denied")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "install"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("daemon install with error exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "permission denied") {
		t.Errorf("stderr = %q; want 'permission denied'", stderr.String())
	}
}

// --- CLI-U1 ---

func TestDaemonUninstall_CallsUninstaller(t *testing.T) {

	installFakeDaemon(t)
	fi := installFakeInstaller(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "uninstall"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon uninstall exit=%d; stderr=%q", code, stderr.String())
	}
	if fi.uninstallCalls != 1 {
		t.Fatalf("Uninstall calls = %d; want 1", fi.uninstallCalls)
	}
	if !strings.Contains(stdout.String(), "uninstalled") {
		t.Errorf("stdout = %q; want 'uninstalled'", stdout.String())
	}
}

// --- CLI-U2 ---

func TestDaemonUninstall_PropagatesError(t *testing.T) {

	installFakeDaemon(t)
	fi := installFakeInstaller(t)
	fi.uninstallErr = errors.New("not found")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "uninstall"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("daemon uninstall with error exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("stderr = %q; want 'not found'", stderr.String())
	}
}

// --- CLI-L1 ---

func TestDaemonLogs_PrintsContent(t *testing.T) {

	fd := installFakeDaemon(t)

	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(logPath, []byte("line1\nline2\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	fd.logPathResult = logPath

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "logs"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("daemon logs exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "line1") || !strings.Contains(stdout.String(), "line2") {
		t.Errorf("stdout = %q; want log content", stdout.String())
	}
}

// --- CLI-L2 ---

func TestDaemonLogs_ErrorWhenFileMissing(t *testing.T) {

	fd := installFakeDaemon(t)
	fd.logPathResult = filepath.Join(t.TempDir(), "nonexistent.log")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"daemon", "logs"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("daemon logs (missing file) exit=0; want non-zero")
	}
}

// --- CLI-E1 ---

func TestErrAlreadyRunning_WrappedInStartError(t *testing.T) {

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	logPath := filepath.Join(dir, "daemon.log")

	d := &fileBackedDaemon{
		pidPath: pidPath,
		logPath: logPath,
		exe:     "/nonexistent/exe",
	}

	// Write the current process's PID so Status returns Running=true.
	if err := writePID(pidPath, os.Getpid()); err != nil {
		t.Fatalf("writePID: %v", err)
	}

	_, err := d.Start(nil)
	if err == nil {
		t.Fatal("Start returned nil; want ErrAlreadyRunning")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("err = %v; want errors.Is(err, ErrAlreadyRunning)", err)
	}
}
