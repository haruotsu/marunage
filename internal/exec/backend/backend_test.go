package backend_test

import (
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/exec"
	"github.com/haruotsu/marunage/internal/exec/backend"
	execcmux "github.com/haruotsu/marunage/internal/exec/cmux"
	execherdr "github.com/haruotsu/marunage/internal/exec/herdr"
	execlocal "github.com/haruotsu/marunage/internal/exec/local"
	exectmux "github.com/haruotsu/marunage/internal/exec/tmux"
)

func TestNewDefaultsToCmux(t *testing.T) {
	for _, name := range []string{"", "cmux"} {
		e, err := backend.New(name)
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if _, ok := e.(*execcmux.Executor); !ok {
			t.Errorf("New(%q) = %T; want *execcmux.Executor", name, e)
		}
	}
}

func TestNewSelectsTmux(t *testing.T) {
	e, err := backend.New("tmux")
	if err != nil {
		t.Fatalf("New(tmux): %v", err)
	}
	if _, ok := e.(*exectmux.Executor); !ok {
		t.Errorf("New(tmux) = %T; want *exectmux.Executor", e)
	}
}

func TestNewSelectsHerdr(t *testing.T) {
	e, err := backend.New("herdr")
	if err != nil {
		t.Fatalf("New(herdr): %v", err)
	}
	if _, ok := e.(*execherdr.Executor); !ok {
		t.Errorf("New(herdr) = %T; want *execherdr.Executor", e)
	}
}

func TestNewSelectsLocal(t *testing.T) {
	e, err := backend.New("local")
	if err != nil {
		t.Fatalf("New(local): %v", err)
	}
	if _, ok := e.(*execlocal.Executor); !ok {
		t.Errorf("New(local) = %T; want *execlocal.Executor", e)
	}
}

func TestNewRejectsUnknown(t *testing.T) {
	_, err := backend.New("podman")
	if !errors.Is(err, backend.ErrUnknownExecutor) {
		t.Errorf("err = %v; want ErrUnknownExecutor", err)
	}
}

// TestNewReturnsCapabilityRichBackends pins that whichever backend the config
// selects, callers still get the optional capabilities they type-assert for
// (the reaper needs Lister, the web stream needs OutputReader). A backend
// that silently dropped a capability would break those consumers at runtime.
func TestNewReturnsCapabilityRichBackends(t *testing.T) {
	for _, name := range []string{"cmux", "tmux", "herdr"} {
		e, err := backend.New(name)
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if _, ok := e.(exec.Lister); !ok {
			t.Errorf("New(%q) does not implement exec.Lister", name)
		}
		if _, ok := e.(exec.Attachable); !ok {
			t.Errorf("New(%q) does not implement exec.Attachable", name)
		}
		if _, ok := e.(exec.OutputReader); !ok {
			t.Errorf("New(%q) does not implement exec.OutputReader", name)
		}
	}
}
