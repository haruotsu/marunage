package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// PR-71 CLI test list — drives `marunage loop` wiring through the
// loopFactoryHook seam so tests do not have to spin up cmux / sqlite /
// the source registry. The hook returns a fakeLoopRunner whose RunOnce /
// Run capture every call so we can assert "exactly one tick" or "loop
// honours --interval until cancelled".

// fakeLoopRunner satisfies loopRunner. RunOnce captures call count;
// Run blocks until ctx is cancelled or RunUntilStops is set, mirroring
// the production loop.Loop semantics so tests for --once / --interval
// behave the same way under the production wiring.
type fakeLoopRunner struct {
	mu             sync.Mutex
	runOnceCalls   int
	runOnceErr     error
	runIntervals   []time.Duration
	runErr         error
	runOnceHook    func()
	runHook        func(ctx context.Context, interval time.Duration)
	runReturnsCtx  bool // when true Run blocks on ctx; when false Run returns runErr immediately
}

func (f *fakeLoopRunner) RunOnce(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runOnceCalls++
	if f.runOnceHook != nil {
		f.runOnceHook()
	}
	return f.runOnceErr
}

func (f *fakeLoopRunner) Run(ctx context.Context, interval time.Duration) error {
	f.mu.Lock()
	f.runIntervals = append(f.runIntervals, interval)
	hook := f.runHook
	blockOnCtx := f.runReturnsCtx
	err := f.runErr
	f.mu.Unlock()
	if hook != nil {
		hook(ctx, interval)
	}
	if blockOnCtx {
		<-ctx.Done()
		return nil
	}
	return err
}

func (f *fakeLoopRunner) onceCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runOnceCalls
}

func (f *fakeLoopRunner) intervals() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.runIntervals))
	copy(out, f.runIntervals)
	return out
}

func installFakeLoop(t *testing.T) *fakeLoopRunner {
	t.Helper()
	fl := &fakeLoopRunner{}
	withLoopFactory(t, func(_ context.Context, _ string) (loopRunner, func() error, error) {
		return fl, func() error { return nil }, nil
	})
	return fl
}

// CLI1: `marunage loop --once` runs RunOnce exactly once and exits 0.
func TestLoop_OnceCallsRunOnce(t *testing.T) {
	fl := installFakeLoop(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"loop", "--once"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("loop --once exit=%d; stderr=%q", code, stderr.String())
	}
	if got := fl.onceCalls(); got != 1 {
		t.Errorf("RunOnce calls = %d; want 1", got)
	}
	if got := len(fl.intervals()); got != 0 {
		t.Errorf("Run was called %d times for --once; want 0", got)
	}
}

// CLI2: `marunage loop --interval 100ms` calls Run with the parsed
// duration. We use runReturnsCtx so the test does not actually wait.
func TestLoop_IntervalForwardsParsedDuration(t *testing.T) {
	fl := installFakeLoop(t)
	fl.runReturnsCtx = true

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so Run returns at once

	var stdout, stderr bytes.Buffer
	code := executeWithCtx(ctx, []string{"loop", "--interval", "250ms"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("loop --interval exit=%d; stderr=%q", code, stderr.String())
	}
	gotIntervals := fl.intervals()
	if len(gotIntervals) != 1 {
		t.Fatalf("Run called %d times; want 1", len(gotIntervals))
	}
	if gotIntervals[0] != 250*time.Millisecond {
		t.Errorf("interval = %v; want 250ms", gotIntervals[0])
	}
}

// CLI3: bare `marunage loop` (no --once, no --interval) defaults to
// discovery.interval from config (10m by default).
func TestLoop_DefaultsToDiscoveryInterval(t *testing.T) {
	fl := installFakeLoop(t)
	fl.runReturnsCtx = true

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr bytes.Buffer
	code := executeWithCtx(ctx, []string{"loop"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("loop (default) exit=%d; stderr=%q", code, stderr.String())
	}
	gotIntervals := fl.intervals()
	if len(gotIntervals) != 1 {
		t.Fatalf("Run called %d times; want 1", len(gotIntervals))
	}
	if gotIntervals[0] != 10*time.Minute {
		t.Errorf("default interval = %v; want 10m (discovery.interval default)", gotIntervals[0])
	}
}

// CLI: --once and --interval together is a config error (mutually
// exclusive). This keeps the operator from accidentally setting both
// and being unsure which one the binary obeyed.
func TestLoop_OnceAndIntervalAreMutuallyExclusive(t *testing.T) {
	installFakeLoop(t)
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"loop", "--once", "--interval", "1s"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("loop --once --interval exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "exclusive") && !strings.Contains(stderr.String(), "--interval") {
		t.Errorf("stderr = %q; want hint about mutual exclusion", stderr.String())
	}
}

// CLI: RunOnce errors propagate on the --once path.
func TestLoop_OnceErrorPropagates(t *testing.T) {
	fl := installFakeLoop(t)
	fl.runOnceErr = errors.New("loop boom")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"loop", "--once"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("loop --once with RunOnce error exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "loop boom") {
		t.Errorf("stderr = %q; want it to mention the underlying error", stderr.String())
	}
}

// executeWithCtx is a tiny helper that runs Execute with a ctx the test
// can cancel. The CLI bridge to ctx is via cobra's SetContext.
func executeWithCtx(ctx context.Context, args []string, stdout, stderr *bytes.Buffer) int {
	return executeForTest(ctx, args, stdout, stderr)
}
