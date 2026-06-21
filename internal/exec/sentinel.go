package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SentinelFile is the atomic exit-code file a dispatched Claude writes when
// it finishes (echo $? > .exit_code.tmp && mv .exit_code.tmp .exit_code).
// Every backend whose AwaitExit is sentinel-based polls this name, so the
// constant lives in the shared exec package rather than being re-declared
// per backend. It must agree with the filename the dispatcher's prompt
// embeds.
const SentinelFile = ".exit_code"

// maxSentinelBytes caps how much of the sentinel a reader will slurp. A
// valid exit code is at most a few bytes; the cap stops a prompt-injected
// Claude from swapping the file for a huge one and making the reader hang.
const maxSentinelBytes = 64

var (
	// ErrAwaitTimeout is returned by AwaitSentinel when the await timeout
	// elapses before the sentinel appears. Distinct from context
	// cancellation so a caller can tell "the session is taking too long"
	// from "we were asked to stop".
	ErrAwaitTimeout = errors.New("exec: timed out waiting for exit sentinel")

	// ErrNoSentinelDir is returned by AwaitSentinel when no control
	// directory was wired, so there is no file to poll. Surfacing it loudly
	// keeps a mis-wired caller from silently blocking forever.
	ErrNoSentinelDir = errors.New("exec: session has no sentinel directory")
)

// AwaitSentinel polls dir/.exit_code until it appears, ctx is cancelled, or
// awaitTimeout elapses. It is the backend-agnostic completion mechanism the
// sentinel-based executors (cmux, tmux) share: the dispatched Claude writes
// the exit code atomically, so a reader sees either the final value or no
// file at all. The parsed exit code is returned even when non-zero; a
// non-nil error is reserved for I/O / timeout / cancellation. An
// awaitTimeout of zero means "no cap" (wait until ctx is cancelled).
func AwaitSentinel(ctx context.Context, dir string, pollInterval, awaitTimeout time.Duration) (int, error) {
	if dir == "" {
		return 0, ErrNoSentinelDir
	}
	path := filepath.Join(dir, SentinelFile)

	var deadline time.Time
	if awaitTimeout > 0 {
		deadline = time.Now().Add(awaitTimeout)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		code, ok, err := ReadExitCode(path)
		if err != nil {
			return 0, err
		}
		if ok {
			return code, nil
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return 0, ErrAwaitTimeout
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

// ReadExitCode performs a single bounded, symlink-refusing read of the exit
// sentinel at path. It returns (code, true, nil) once the file is present
// and parses, (0, false, nil) while it is still absent, and a non-nil error
// only for a genuine I/O / parse failure. The O_NOFOLLOW + size cap harden
// the read so a prompt-injected Claude cannot make the reader follow a
// symlink off the control dir or slurp a huge file.
func ReadExitCode(path string) (int, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		if errors.Is(err, syscall.ELOOP) {
			return 0, false, fmt.Errorf("exec: refused symlink at %s", filepath.Base(path))
		}
		return 0, false, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	if !info.Mode().IsRegular() {
		return 0, false, fmt.Errorf("exec: %s is not a regular file", filepath.Base(path))
	}
	if info.Size() > maxSentinelBytes {
		return 0, false, fmt.Errorf("exec: %s exceeds %d bytes", filepath.Base(path), maxSentinelBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSentinelBytes+1))
	if err != nil {
		return 0, false, err
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false, fmt.Errorf("exec: parse %s: %w", filepath.Base(path), err)
	}
	return code, true, nil
}
