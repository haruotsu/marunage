package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/dispatch"
)

// PR-42 CLI test list (t_wada TDD; ticked off as the matching test below
// goes green):
//
//   E1. `marunage dispatch` builds a Dispatcher from config and invokes
//       Run with MaxParallel sourced from core.max_parallel. The factory
//       seam (dispatcherFactoryHook) lets the test capture RunOptions
//       without spinning up cmux / sqlite.
//   E2. `marunage dispatch <id>` populates RunOptions.ID with the parsed
//       positional. MaxParallel becomes irrelevant for the single-row
//       path but is still passed through so a future logger can record it.
//   E3. `marunage dispatch --max-parallel N` overrides the config value.
//   E4. Dispatcher.Run errors propagate as a non-zero exit (cobra prints
//       the error to stderr).
//   E5. Invalid id positional ("abc", "0") rejects before the factory is
//       even consulted, so a typo never opens the DB.

// fakeDispatcher captures the RunOptions calls made by newDispatchCmd
// and returns the configured error. Tests inject it via withDispatcherFactory.
type fakeDispatcher struct {
	mu       sync.Mutex
	runCalls []dispatch.RunOptions
	runErr   error
}

func (f *fakeDispatcher) Run(_ context.Context, opts dispatch.RunOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCalls = append(f.runCalls, opts)
	return f.runErr
}

func installFakeDispatcher(t *testing.T) *fakeDispatcher {
	t.Helper()
	fd := &fakeDispatcher{}
	withDispatcherFactory(t, func(_ context.Context, _ string) (dispatchRunner, func() error, error) {
		return fd, func() error { return nil }, nil
	})
	return fd
}

// E1: bare `marunage dispatch` calls Run once with MaxParallel from
// core.max_parallel (the default config has max_parallel=3).
func TestDispatch_DefaultsToConfigMaxParallel(t *testing.T) {
	fd := installFakeDispatcher(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"dispatch"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("dispatch exit=%d; stderr=%q", code, stderr.String())
	}
	if len(fd.runCalls) != 1 {
		t.Fatalf("Run calls = %d; want 1", len(fd.runCalls))
	}
	if got := fd.runCalls[0].MaxParallel; got != 3 {
		t.Errorf("MaxParallel = %d; want 3 (from config default)", got)
	}
	if got := fd.runCalls[0].ID; got != 0 {
		t.Errorf("ID = %d; want 0 (no positional)", got)
	}
}

// E2: `marunage dispatch <id>` sets RunOptions.ID to the parsed positional.
func TestDispatch_PositionalIDIsForwarded(t *testing.T) {
	fd := installFakeDispatcher(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"dispatch", "42"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("dispatch 42 exit=%d; stderr=%q", code, stderr.String())
	}
	if len(fd.runCalls) != 1 {
		t.Fatalf("Run calls = %d; want 1", len(fd.runCalls))
	}
	if got := fd.runCalls[0].ID; got != 42 {
		t.Errorf("ID = %d; want 42", got)
	}
}

// E3: --max-parallel overrides the config value.
func TestDispatch_MaxParallelFlagOverridesConfig(t *testing.T) {
	fd := installFakeDispatcher(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"dispatch", "--max-parallel", "7"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("dispatch --max-parallel 7 exit=%d; stderr=%q", code, stderr.String())
	}
	if got := fd.runCalls[0].MaxParallel; got != 7 {
		t.Errorf("MaxParallel = %d; want 7 (flag overrides config)", got)
	}
}

// E4: Run errors propagate as a non-zero exit. The error message lands on
// stderr so the operator sees what went wrong.
func TestDispatch_RunErrorPropagates(t *testing.T) {
	fd := installFakeDispatcher(t)
	fd.runErr = errors.New("boom in dispatcher")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"dispatch"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("dispatch with Run error exit=0; want non-zero; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "boom in dispatcher") {
		t.Errorf("stderr = %q; want it to mention the underlying error", stderr.String())
	}
}

// E5: invalid positional id rejects before any factory is touched.
func TestDispatch_InvalidIDRejected(t *testing.T) {
	fd := installFakeDispatcher(t)

	cases := []string{"abc", "0", "-3"}
	for _, arg := range cases {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute([]string{"dispatch", arg}, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("dispatch %q exit=0; want non-zero", arg)
			}
		})
	}
	if len(fd.runCalls) != 0 {
		t.Errorf("Run was called %d times despite invalid id; want 0", len(fd.runCalls))
	}
}
