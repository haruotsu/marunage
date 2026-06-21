package herdr

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Runner abstracts the single operation this package needs against the
// outside world: invoke `herdr <args...>` and capture its stdout / stderr.
// Splitting it out lets tests inject canned output for `herdr workspace
// create`, `herdr pane read`, etc. without ever spawning a real herdr. It
// mirrors internal/exec/tmux.Runner so the two terminal-multiplexer backends
// share the same test-injection pattern.
type Runner interface {
	// Run executes name with args and returns stdout and stderr separately.
	// Implementations must honour ctx (deadline + cancel) so a wedged herdr
	// cannot block dispatch indefinitely.
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// ExecRunner is the production Runner backing every herdr call. It defers
// PATH lookup to os/exec so a $PATH override (a user's custom herdr build)
// is honoured without extra configuration.
type ExecRunner struct{}

// Run shells out to name with args, capturing stdout and stderr into
// independent buffers.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// isBinaryNotFound reports whether err indicates the herdr binary is missing
// from PATH, centralised here so callers can map it to ErrHerdrNotFound
// without leaking exec internals.
func isBinaryNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, exec.ErrNotFound)
	}
	return false
}
