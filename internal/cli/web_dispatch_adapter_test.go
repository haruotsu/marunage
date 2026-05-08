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

func TestWebDispatchAdapter_Success(t *testing.T) {
	fd := &fakeDispatcher{runErr: nil}
	adapter := &webDispatchAdapter{runner: fd}
	if err := adapter.Dispatch(context.Background(), 42); err != nil {
		t.Fatalf("Dispatch: unexpected error %v", err)
	}
}

func TestWebDispatchAdapter_NotFound(t *testing.T) {
	fd := &fakeDispatcher{runErr: store.ErrNotFound}
	adapter := &webDispatchAdapter{runner: fd}
	err := adapter.Dispatch(context.Background(), 42)
	if !errors.Is(err, web.ErrTaskNotFound) {
		t.Fatalf("Dispatch: err = %v; want web.ErrTaskNotFound", err)
	}
}

func TestWebDispatchAdapter_NotPending(t *testing.T) {
	fd := &fakeDispatcher{runErr: fmt.Errorf("wrapped: %w", dispatch.ErrNotPending)}
	adapter := &webDispatchAdapter{runner: fd}
	err := adapter.Dispatch(context.Background(), 42)
	if !errors.Is(err, web.ErrTaskInvalidTransition) {
		t.Fatalf("Dispatch: err = %v; want web.ErrTaskInvalidTransition", err)
	}
}

func TestWebDispatchAdapter_UnknownError(t *testing.T) {
	sentinel := errors.New("db blew up")
	fd := &fakeDispatcher{runErr: sentinel}
	adapter := &webDispatchAdapter{runner: fd}
	err := adapter.Dispatch(context.Background(), 42)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Dispatch: err = %v; want original error", err)
	}
}

func TestWebDispatchAdapter_PassesID(t *testing.T) {
	fd := &fakeDispatcher{}
	adapter := &webDispatchAdapter{runner: fd}
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
