package cmux

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Runner abstracts the single operation this package needs against the
// outside world: invoke `cmux <args...>` and capture its stdout / stderr.
// Splitting it out lets tests inject canned stdout for `cmux new-workspace`
// without ever spawning a real cmux process. See internal/doctor/runner.go
// for the same pattern applied to `claude --version` style probes.
type Runner interface {
	// Run executes name with args and returns stdout and stderr separately.
	// Implementations must honour ctx (deadline + cancel) so a wedged cmux
	// cannot block dispatch indefinitely.
	//
	// When name is not on PATH the returned error must wrap exec.ErrNotFound
	// so callers can map it to ErrCmuxNotFound via errors.Is.
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// ExecRunner is the production Runner backing every cmux call. It defers
// PATH lookup to os/exec so $PATH overrides (e.g. a user's nvm shim or a
// custom cmux build under ~/.local/bin) are honoured without any extra
// configuration.
type ExecRunner struct{}

// Run shells out to name with args, capturing stdout and stderr into
// independent buffers. Errors from CombinedOutput are returned verbatim;
// callers map *exec.Error / exec.ErrNotFound into typed sentinels so the
// CLI layer never has to substring-match.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// isBinaryNotFound reports whether err indicates the target binary is
// missing from PATH. Centralised here so NewWorkspace can map it to
// ErrCmuxNotFound without leaking exec internals to its callers.
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
