package source

import (
	"context"
	"errors"
	"testing"
)

func makeManifest(t *testing.T, adapterVersion string, caps []Capability) *Manifest {
	t.Helper()
	all := []Capability{CapList, CapSetup, CapAuthStatus}
	all = append(all, caps...)
	return &Manifest{
		Name:           "testplugin",
		Version:        "1.0.0",
		SyncMode:       SyncModeBidirectional,
		AdapterVersion: adapterVersion,
		Capabilities:   all,
	}
}

func TestRunnerSupportsUpdateV2WithCapability(t *testing.T) {
	t.Parallel()

	m := makeManifest(t, "v2", []Capability{CapUpdate})
	r := NewRunner(m, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	})
	if !r.SupportsUpdate() {
		t.Error("SupportsUpdate() = false, want true for v2 manifest with CapUpdate")
	}
}

func TestRunnerSupportsUpdateFalseForV1(t *testing.T) {
	t.Parallel()

	m := makeManifest(t, "v1", []Capability{CapUpdate})
	r := NewRunner(m, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	})
	if r.SupportsUpdate() {
		t.Error("SupportsUpdate() = true, want false for v1 manifest")
	}
}

func TestRunnerSupportsUpdateFalseWithoutCapability(t *testing.T) {
	t.Parallel()

	m := makeManifest(t, "v2", nil)
	r := NewRunner(m, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	})
	if r.SupportsUpdate() {
		t.Error("SupportsUpdate() = true, want false for v2 manifest without CapUpdate")
	}
}

func TestRunnerUpdateReturnsErrCapabilityNotSupportedForV1(t *testing.T) {
	t.Parallel()

	m := makeManifest(t, "v1", nil)
	r := NewRunner(m, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	})
	err := r.Update(context.Background(), "task-1", "title", "new title")
	if !errors.Is(err, ErrCapabilityNotSupported) {
		t.Errorf("Update() error = %v, want ErrCapabilityNotSupported", err)
	}
}

func TestRunnerUpdateSucceedsForV2(t *testing.T) {
	t.Parallel()

	var gotArgs []string
	m := makeManifest(t, "v2", []Capability{CapUpdate})
	r := NewRunner(m, func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"ok":true}`), nil
	})
	if err := r.Update(context.Background(), "task-1", "title", "new title"); err != nil {
		t.Fatalf("Update() unexpected error: %v", err)
	}
	if len(gotArgs) != 4 || gotArgs[0] != "update" || gotArgs[1] != "task-1" || gotArgs[2] != "title" || gotArgs[3] != "new title" {
		t.Errorf("plugin called with args %v, want [update task-1 title new title]", gotArgs)
	}
}

func TestRunnerUpdatePropagatesPluginError(t *testing.T) {
	t.Parallel()

	m := makeManifest(t, "v2", []Capability{CapUpdate})
	r := NewRunner(m, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"ok":false,"error":"field not found"}`), nil
	})
	err := r.Update(context.Background(), "task-1", "bogus", "x")
	if err == nil {
		t.Fatal("Update() expected error from plugin error response, got nil")
	}
}
