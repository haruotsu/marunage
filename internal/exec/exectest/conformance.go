// Package exectest holds the backend-agnostic conformance suite every
// exec.Executor implementation must pass. It lives in its own package (not
// the production exec package) so the `testing` dependency never leaks into
// a shipped binary, yet any backend's _test file can import it.
//
// A backend supplies a Harness — four scenario constructors — and
// RunConformance drives the exec.Executor contract against them. cmux and
// tmux run the same suite, which is the point of PR-R07: pin that the
// abstraction holds identically across two unrelated backends.
package exectest

import (
	"context"
	"testing"

	"github.com/haruotsu/marunage/internal/exec"
)

// Harness lets RunConformance drive a backend without knowing its concrete
// type. Each method builds a freshly-configured Executor exhibiting exactly
// one scenario; the fakes behind them keep the suite from touching a real
// cmux or tmux.
type Harness interface {
	// Healthy returns an Executor whose Start succeeds, becomes ready, and
	// accepts a subsequent Send without error. (The suite asserts Send does
	// not error after a ready Start; whether a backend gates Send on
	// readiness is backend-specific and pinned by each backend's own tests.)
	Healthy(t *testing.T) exec.Executor

	// CreateFails returns an Executor whose Start fails before creating any
	// session — the contract's "nothing created, retryable" case.
	CreateFails(t *testing.T) exec.Executor

	// ReadinessFails returns an Executor whose Start creates a session that
	// never becomes ready — the contract's "leaked, mark failed" case.
	ReadinessFails(t *testing.T) exec.Executor

	// Completed returns an Executor plus a Session that AwaitExit will see
	// finish with exitCode. The backend arranges the completion artefact
	// (e.g. writing the exit sentinel into the session's control dir).
	Completed(t *testing.T, exitCode int) (exec.Executor, exec.Session)
}

// CanonicalSpec is the launch request the suite feeds every backend's Start.
// Backends are fake-backed, so the values only need to be plausibly valid.
var CanonicalSpec = exec.SessionSpec{
	Cwd:     "/tmp/marunage-conformance",
	Command: "claude --dangerously-skip-permissions",
	Name:    "#1 conformance task",
}

// RunConformance asserts that h's backend honours the exec.Executor contract.
// Every backend is expected to pass every subtest; a divergence here means
// the abstraction has sprung a backend-specific leak.
func RunConformance(t *testing.T, h Harness) {
	t.Helper()

	t.Run("StartCreateFailureReturnsZeroSession", func(t *testing.T) {
		sess, err := h.CreateFails(t).Start(context.Background(), CanonicalSpec)
		if err == nil {
			t.Fatal("Start err = nil; want create failure")
		}
		if sess.ID != "" {
			t.Errorf("session ID = %q; want empty (nothing created → retryable)", sess.ID)
		}
	})

	t.Run("StartReadinessFailureReturnsPopulatedSession", func(t *testing.T) {
		sess, err := h.ReadinessFails(t).Start(context.Background(), CanonicalSpec)
		if err == nil {
			t.Fatal("Start err = nil; want readiness failure")
		}
		if sess.ID == "" {
			t.Error("session ID empty; want the created session preserved for the reaper")
		}
	})

	t.Run("StartSuccessReadySessionAcceptsSend", func(t *testing.T) {
		e := h.Healthy(t)
		sess, err := e.Start(context.Background(), CanonicalSpec)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if sess.ID == "" {
			t.Fatal("session ID empty after a successful Start")
		}
		if err := e.Send(context.Background(), sess, "do the thing"); err != nil {
			t.Errorf("Send to ready session: %v", err)
		}
	})

	t.Run("AwaitExitReturnsExitCode", func(t *testing.T) {
		for _, code := range []int{0, 127} {
			e, sess := h.Completed(t, code)
			got, err := e.AwaitExit(context.Background(), sess)
			if err != nil {
				t.Fatalf("AwaitExit(code=%d): %v", code, err)
			}
			if got != code {
				t.Errorf("AwaitExit = %d; want %d", got, code)
			}
		}
	})
}
