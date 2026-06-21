package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/exec"
	"github.com/haruotsu/marunage/internal/exec/local"
)

// procTestTimeout bounds the real-process tests' polling. SIGKILL is
// effectively immediate, so this is generous slack for a loaded CI runner
// rather than an expected wait.
const procTestTimeout = 5 * time.Second

// TestNotAttachable is the central proof of PR-R08: a capability-poor
// backend still satisfies the core Executor contract, and a caller that
// type-asserts for an unsupported capability simply gets ok=false instead
// of a broken run.
func TestNotAttachable(t *testing.T) {
	var e exec.Executor = local.New()
	if _, ok := e.(exec.Attachable); ok {
		t.Error("localExecutor must not implement exec.Attachable")
	}
}

func TestImplementsExecutor(t *testing.T) {
	// Compile-time assertion lives in the package; this guards intent.
	var _ exec.Executor = local.New()
}

// TestRoundTripWithEcho exercises Start -> AwaitExit against a real,
// short-lived child process (no real claude) to confirm the os/exec path
// actually launches and reaps a process and returns its exit code.
func TestRoundTripWithEcho(t *testing.T) {
	e := local.New()

	sess, err := e.Start(context.Background(), exec.SessionSpec{
		Command: "echo hello",
		Name:    "echo-test",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("Start returned empty session ID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := e.AwaitExit(ctx, sess)
	if err != nil {
		t.Fatalf("AwaitExit: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestRoundTripNonZeroExit(t *testing.T) {
	e := local.New()

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "exit 3"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := e.AwaitExit(ctx, sess)
	if err != nil {
		t.Fatalf("AwaitExit returned error for non-zero exit (should report code, not error): %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

// TestAwaitExitCancelKillsProcessTree proves ctx cancellation tears down
// the whole process group, not just the sh -c parent. The command
// backgrounds a long sleep (a grandchild) and records its pid; after
// cancellation that grandchild must be gone, otherwise it was orphaned.
func TestAwaitExitCancelKillsProcessTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	e := local.New()
	sess, err := e.Start(context.Background(), exec.SessionSpec{
		Command: "sleep 30 & echo $! > " + pidFile + "; wait",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	grandchild := waitForPid(t, pidFile)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	awaitDone := make(chan error, 1)
	go func() {
		_, err := e.AwaitExit(ctx, sess)
		awaitDone <- err
	}()

	// Poll for the grandchild's death independently of AwaitExit returning:
	// if only the sh parent is killed, the backgrounded sleep is orphaned and
	// survives well past this deadline. ESRCH means the pid is gone; signal 0
	// probes existence without delivering anything. (A pid could in theory be
	// reused within this tiny window, but on a test host that is negligible.)
	deadline := time.Now().Add(procTestTimeout)
	for !errors.Is(syscall.Kill(grandchild, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d survived cancellation (process group not killed)", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := <-awaitDone; err != context.Canceled {
		t.Fatalf("AwaitExit error = %v, want context.Canceled", err)
	}
}

func waitForPid(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(procTestTimeout)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild never recorded its pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEnvForwarded confirms spec.Env reaches the child, demonstrating the
// per-session environment knob cmux could not honour.
func TestEnvForwarded(t *testing.T) {
	e := local.New()

	sess, err := e.Start(context.Background(), exec.SessionSpec{
		Command: `test "$MARUNAGE_TEST" = "ok"`,
		Env:     map[string]string{"MARUNAGE_TEST": "ok"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := e.AwaitExit(ctx, sess)
	if err != nil {
		t.Fatalf("AwaitExit: %v", err)
	}
	if code != 0 {
		t.Errorf("env not forwarded: child saw MARUNAGE_TEST != ok (exit %d)", code)
	}
}

// TestSendThenAwaitWithCat proves stdin delivery end-to-end: cat echoes
// the folded prompt to stdout, and after stdin closes (AwaitExit) cat
// exits 0 and the snapshot shows the prompt.
func TestSendThenAwaitWithCat(t *testing.T) {
	var e exec.Executor = local.New()

	sess, err := e.Start(context.Background(), exec.SessionSpec{Command: "cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := e.Send(context.Background(), sess, "ping"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := e.AwaitExit(ctx, sess)
	if err != nil {
		t.Fatalf("AwaitExit: %v", err)
	}
	if code != 0 {
		t.Errorf("cat exit code = %d, want 0", code)
	}

	r, ok := e.(exec.OutputReader)
	if !ok {
		t.Fatal("localExecutor must implement exec.OutputReader")
	}
	out, err := r.ReadOutput(context.Background(), sess)
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if !strings.Contains(out, "ping") {
		t.Errorf("stdout snapshot = %q, want it to contain %q", out, "ping")
	}
}
