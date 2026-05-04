package project

import (
	"bytes"
	"context"
	"os/exec"
)

// Runner abstracts the single operation this package needs against the
// outside world: invoke a CLI tool and capture stdout / stderr.
// The shape mirrors internal/source/github/runner.go so cross-package
// readers see consistent vocabulary for "shell out to a tool".
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// ExecRunner is the production Runner. It defers PATH lookup to os/exec
// so PATH overrides are honoured without extra configuration.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}
