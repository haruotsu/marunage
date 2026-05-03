package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// fakeMirror records every hook the CLI fires so tests can pin which
// transition triggered which mirror call. The PR-21 default in production
// is noopMirror; test code installs a fakeMirror via withMirrorFactory to
// observe the call.
type fakeMirror struct {
	doneCalls   []store.Task
	deleteCalls []store.Task
	reopenCalls []store.Task

	// errOn lets a test request a specific hook to fail so the CLI's
	// error-propagation path stays under test.
	errOn map[string]error
}

func newFakeMirror() *fakeMirror { return &fakeMirror{errOn: map[string]error{}} }

func (m *fakeMirror) OnDone(_ context.Context, t store.Task) error {
	m.doneCalls = append(m.doneCalls, t)
	return m.errOn["done"]
}

func (m *fakeMirror) OnDelete(_ context.Context, t store.Task) error {
	m.deleteCalls = append(m.deleteCalls, t)
	return m.errOn["delete"]
}

func (m *fakeMirror) OnReopen(_ context.Context, t store.Task) error {
	m.reopenCalls = append(m.reopenCalls, t)
	return m.errOn["reopen"]
}

// installFakeMirror is the standard test harness for swapping in a fake
// mirror. Mirrors installFakeRepo's shape so the two compose naturally.
func installFakeMirror(t *testing.T) *fakeMirror {
	t.Helper()
	m := newFakeMirror()
	withMirrorFactory(t, func(_ context.Context, _ string) (Mirror, error) {
		return m, nil
	})
	return m
}

// The default mirror is a no-op so production code without a configured
// mirror plugin never panics. A bare call must succeed and not record.
func TestMirror_DefaultIsNoop(t *testing.T) {
	m := noopMirror{}
	if err := m.OnDone(context.Background(), store.Task{}); err != nil {
		t.Errorf("noop OnDone: %v", err)
	}
	if err := m.OnDelete(context.Background(), store.Task{}); err != nil {
		t.Errorf("noop OnDelete: %v", err)
	}
	if err := m.OnReopen(context.Background(), store.Task{}); err != nil {
		t.Errorf("noop OnReopen: %v", err)
	}
}

// activeMirrorFactory falls through to the production factory when no
// hook is installed. The production factory must hand back something
// non-nil so the CLI can call hooks unconditionally.
func TestMirror_ProductionFactoryReturnsNonNil(t *testing.T) {
	m, err := productionMirrorFactory(context.Background(), "")
	if err != nil {
		t.Fatalf("productionMirrorFactory: %v", err)
	}
	if m == nil {
		t.Fatal("productionMirrorFactory returned nil mirror")
	}
}

// installFakeMirror's hook must be respected by activeMirrorFactory, and
// the factory must restore the previous hook after the test.
func TestMirror_TestHookIsRespected(t *testing.T) {
	want := newFakeMirror()
	withMirrorFactory(t, func(_ context.Context, _ string) (Mirror, error) {
		return want, nil
	})
	got, err := activeMirrorFactory()(context.Background(), "")
	if err != nil {
		t.Fatalf("activeMirrorFactory: %v", err)
	}
	if got != want {
		t.Errorf("activeMirrorFactory returned %v; want injected fake", got)
	}
}

// The fakeMirror captures hook payloads so PR-21 CLI tests can assert the
// hook fired with the right task.
func TestFakeMirror_RecordsCalls(t *testing.T) {
	m := newFakeMirror()
	task := store.Task{ID: 1, Title: "x"}
	if err := m.OnDone(context.Background(), task); err != nil {
		t.Fatalf("OnDone: %v", err)
	}
	if len(m.doneCalls) != 1 || m.doneCalls[0].ID != 1 {
		t.Errorf("doneCalls = %+v; want one call with id=1", m.doneCalls)
	}
}

// errOn lets a test arrange for a hook to fail.
func TestFakeMirror_ErrOnPropagates(t *testing.T) {
	m := newFakeMirror()
	m.errOn["done"] = errors.New("boom")
	if err := m.OnDone(context.Background(), store.Task{}); err == nil {
		t.Errorf("OnDone with errOn['done'] should fail")
	}
}
