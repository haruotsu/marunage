package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// ErrCapabilityNotSupported is returned when Update is called on a plugin
// that does not meet the v2 + CapUpdate requirement.
var ErrCapabilityNotSupported = errors.New("source: capability not supported")

// execFunc is the injectable execution function for Runner.
type execFunc func(ctx context.Context, args ...string) ([]byte, error)

// Runner wraps a plugin Manifest and an execution function, providing
// SupportsUpdate / Update operations for v2 adapter plugins.
type Runner struct {
	manifest *Manifest
	exec     execFunc
}

// NewRunner returns a Runner backed by the given manifest and exec function.
// In production, exec shells out to the plugin binary; in tests, a stub
// returns canned JSON.
func NewRunner(manifest *Manifest, exec execFunc) *Runner {
	return &Runner{manifest: manifest, exec: exec}
}

// NewRunnerFromPath returns a Runner that shells out to the plugin binary at
// binPath. Use this in production; use NewRunner with a stub in tests.
func NewRunnerFromPath(manifest *Manifest, binPath string) *Runner {
	return NewRunner(manifest, func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, binPath, args...).Output()
	})
}

// SupportsUpdate reports whether the plugin declares adapter_version = "v2"
// and the "update" capability.
func (r *Runner) SupportsUpdate() bool {
	return r.manifest.AdapterVersion == "v2" && r.manifest.HasCapability(CapUpdate)
}

// Update invokes the plugin with "update <taskID> <field> <value>" and
// returns any error the plugin reports. Returns ErrCapabilityNotSupported
// when SupportsUpdate is false.
func (r *Runner) Update(ctx context.Context, taskID, field, value string) error {
	if !r.SupportsUpdate() {
		return ErrCapabilityNotSupported
	}
	out, err := r.exec(ctx, "update", taskID, field, value)
	if err != nil {
		return fmt.Errorf("runner update %s: %w", taskID, err)
	}
	var resp struct {
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("runner update: invalid json response: %w", err)
	}
	if !resp.Ok {
		return fmt.Errorf("runner update: plugin error: %s", resp.Error)
	}
	return nil
}
