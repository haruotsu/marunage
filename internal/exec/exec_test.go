package exec_test

import (
	"testing"

	"github.com/haruotsu/marunage/internal/exec"
)

func TestNewSessionRoundTripsIDAndHandle(t *testing.T) {
	type cmuxHandle struct{ name string }
	h := cmuxHandle{name: "ws-label"}

	s := exec.NewSession("workspace:7", h)

	if s.ID != "workspace:7" {
		t.Errorf("ID = %q; want %q", s.ID, "workspace:7")
	}
	got, ok := s.Handle().(cmuxHandle)
	if !ok {
		t.Fatalf("Handle() type = %T; want cmuxHandle", s.Handle())
	}
	if got.name != "ws-label" {
		t.Errorf("Handle().name = %q; want %q", got.name, "ws-label")
	}
}

func TestNewSessionNilHandle(t *testing.T) {
	s := exec.NewSession("workspace:1", nil)
	if s.Handle() != nil {
		t.Errorf("Handle() = %v; want nil", s.Handle())
	}
	if s.ID != "workspace:1" {
		t.Errorf("ID = %q; want %q", s.ID, "workspace:1")
	}
}
