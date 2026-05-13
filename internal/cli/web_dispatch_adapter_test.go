package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/web"
)

// TestWebDispatchAdapter_NoSession_ReturnsErrNoActiveSession verifies
// that Dispatch returns ErrNoActiveSession when the cmux backend is
// configured (requireCmuxSession=true) but the server is not running
// inside an active cmux pane (CMUX_WORKSPACE_ID unset), so the caller
// gets a clear 503 rather than a wrapped "Access denied" cmux error.
func TestWebDispatchAdapter_NoSession_ReturnsErrNoActiveSession(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	fd := &fakeDispatcher{}
	adapter := &webDispatchAdapter{runner: fd, requireCmuxSession: true}
	err := adapter.Dispatch(context.Background(), 42)
	if !errors.Is(err, web.ErrNoActiveSession) {
		t.Fatalf("Dispatch with no session: err = %v; want web.ErrNoActiveSession", err)
	}
}

// TestWebDispatchAdapter_HerdrBackend_SkipsCmuxSessionGate verifies that
// the herdr backend (requireCmuxSession=false) lets Dispatch proceed
// even without CMUX_WORKSPACE_ID. herdr's socket is reachable from any
// process on the host, so the cmux-specific gate is the wrong check.
func TestWebDispatchAdapter_HerdrBackend_SkipsCmuxSessionGate(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "")
	fd := &fakeDispatcher{}
	adapter := &webDispatchAdapter{runner: fd, requireCmuxSession: false}
	if err := adapter.Dispatch(context.Background(), 7); err != nil {
		t.Fatalf("Dispatch with herdr backend: unexpected error %v", err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.runCalls) != 1 {
		t.Fatalf("runCalls = %d; want 1 (dispatcher should have been invoked)", len(fd.runCalls))
	}
}

func TestWebDispatchAdapter_Success(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "workspace:1")
	fd := &fakeDispatcher{runErr: nil}
	adapter := &webDispatchAdapter{runner: fd, requireCmuxSession: true}
	if err := adapter.Dispatch(context.Background(), 42); err != nil {
		t.Fatalf("Dispatch: unexpected error %v", err)
	}
}

func TestWebDispatchAdapter_NotFound(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "workspace:1")
	fd := &fakeDispatcher{runErr: store.ErrNotFound}
	adapter := &webDispatchAdapter{runner: fd, requireCmuxSession: true}
	err := adapter.Dispatch(context.Background(), 42)
	if !errors.Is(err, web.ErrTaskNotFound) {
		t.Fatalf("Dispatch: err = %v; want web.ErrTaskNotFound", err)
	}
}

func TestWebDispatchAdapter_NotPending(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "workspace:1")
	fd := &fakeDispatcher{runErr: fmt.Errorf("wrapped: %w", dispatch.ErrNotPending)}
	adapter := &webDispatchAdapter{runner: fd, requireCmuxSession: true}
	err := adapter.Dispatch(context.Background(), 42)
	if !errors.Is(err, web.ErrTaskInvalidTransition) {
		t.Fatalf("Dispatch: err = %v; want web.ErrTaskInvalidTransition", err)
	}
}

func TestWebDispatchAdapter_UnknownError(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "workspace:1")
	sentinel := errors.New("db blew up")
	fd := &fakeDispatcher{runErr: sentinel}
	adapter := &webDispatchAdapter{runner: fd, requireCmuxSession: true}
	err := adapter.Dispatch(context.Background(), 42)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Dispatch: err = %v; want original error", err)
	}
}

func TestWebDispatchAdapter_PassesID(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "workspace:1")
	fd := &fakeDispatcher{}
	adapter := &webDispatchAdapter{runner: fd, requireCmuxSession: true}
	_ = adapter.Dispatch(context.Background(), 99)
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.runCalls) != 1 {
		t.Fatalf("runCalls = %d; want 1", len(fd.runCalls))
	}
	if fd.runCalls[0].ID != 99 {
		t.Fatalf("RunOptions.ID = %d; want 99", fd.runCalls[0].ID)
	}
}
