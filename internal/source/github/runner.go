package github

import (
	"bytes"
	"context"
	"os/exec"
)

// Runner abstracts the single operation this package needs against the
// outside world: invoke `gh <args...>` and capture its stdout / stderr.
// Splitting it out lets tests inject canned stdout for `gh search issues`
// without ever spawning a real gh process. The shape mirrors
// internal/workspace/runner.go so cross-package readers see the same vocabulary
// for "shell out to a tool with deadline support".
type Runner interface {
	// Run executes name with args and returns stdout and stderr separately.
	// Implementations must honour ctx (deadline + cancel) so a wedged gh
	// cannot block discovery indefinitely.
	//
	// When name is not on PATH the returned error must wrap exec.ErrNotFound
	// so callers can map it via errors.Is — AuthStatus uses this signal to
	// downgrade "gh missing" into AuthNotConfigured rather than a hard error.
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// ExecRunner is the production Runner backing every gh call. It defers PATH
// lookup to os/exec so $PATH overrides (Homebrew gh, a custom build under
// ~/.local/bin) are honoured without any extra configuration.
type ExecRunner struct{}

// Run shells out to name with args, capturing stdout and stderr into
// independent buffers. Errors from cmd.Run are returned verbatim; callers
// can `errors.Is(err, exec.ErrNotFound)` to detect a missing binary, but
// AuthStatus deliberately treats every error path identically (see the
// godoc on Plugin.AuthStatus) so PR-83 itself never branches on the type.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}
