package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PR-71 follow-on: direct tests for the fileBackedDaemon I/O primitives
// (readPID / writePID / processAlive). The CLI tests in daemon_test.go
// stop at the daemonControl interface so they cannot catch a malformed
// pidfile, a torn write, or a misclassified alive probe — those bugs
// only surface against the real file-backed implementation. These tests
// stay platform-agnostic: they never spawn a subprocess (avoiding the
// "real-binary E2E" complexity) and exercise the on-disk + kernel
// surface directly.

// TestWritePID_RoundTripsThroughReadPID pins the on-disk format Status /
// Stop rely on: writePID(123) must be readable back as 123. The atomic
// tmp + rename path means a concurrent reader sees either the previous
// value or 123, never a half-written file — implicit in this test
// because the file is read after the write returned.
func TestWritePID_RoundTripsThroughReadPID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")

	if err := writePID(path, 12345); err != nil {
		t.Fatalf("writePID: %v", err)
	}
	got, err := readPID(path)
	if err != nil {
		t.Fatalf("readPID: %v", err)
	}
	if got != 12345 {
		t.Errorf("readPID = %d; want 12345", got)
	}

	// File mode should be 0o600 — pidfile is per-user state, no other
	// account on the host should be able to read or stomp it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("pidfile perm = %v; want 0o600", perm)
	}
}

// TestReadPID_MissingFileReturnsErrNotExist locks in the contract Status
// uses to distinguish "no daemon ever started" from "stale pidfile":
// errors.Is(err, os.ErrNotExist) must be true on the missing-file path.
func TestReadPID_MissingFileReturnsErrNotExist(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "daemon.pid")
	_, err := readPID(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readPID(missing) err = %v; want errors.Is os.ErrNotExist", err)
	}
}

// TestReadPID_MalformedContentSurfacesError covers the corrupt-pidfile
// branch. Without an explicit error here, Status would silently treat a
// garbage file as "no daemon" and Start would refuse to spawn over it.
// The error message names the path so the operator knows which file to
// delete.
func TestReadPID_MalformedContentSurfacesError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	if err := os.WriteFile(path, []byte("not a number\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := readPID(path)
	if err == nil {
		t.Fatal("readPID(malformed) returned nil; want error")
	}
	if !strings.Contains(err.Error(), "malformed pidfile") {
		t.Errorf("err = %q; want it to mention 'malformed pidfile'", err.Error())
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("err = %q; want it to name the offending path", err.Error())
	}
}

// TestReadPID_NonPositiveRejected guards against `0` or a negative pid
// silently passing through — Stop's `os.FindProcess(0)` is undefined on
// most platforms and `Signal(0)` against pid=0 sends to the whole
// process group, so we want loud rejection at parse time.
func TestReadPID_NonPositiveRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, val := range []string{"0", "-1"} {
		t.Run(val, func(t *testing.T) {
			path := filepath.Join(dir, "daemon-"+val+".pid")
			if err := os.WriteFile(path, []byte(val+"\n"), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if _, err := readPID(path); err == nil {
				t.Fatalf("readPID(%q) returned nil; want error", val)
			}
		})
	}
}

// TestProcessAlive_SelfPidIsAlive: the running test process is, by
// definition, alive. processAlive(os.Getpid()) must return true so
// Status correctly classifies a daemon that is in fact running.
func TestProcessAlive_SelfPidIsAlive(t *testing.T) {
	t.Parallel()
	if !processAlive(os.Getpid()) {
		t.Fatalf("processAlive(self pid %d) = false; want true", os.Getpid())
	}
}

// TestProcessAlive_NonExistentPidIsDead picks a pid the kernel cannot
// possibly have allocated (negative + an absurdly large number) so the
// Signal(0) probe must report dead. Picking a small "definitely dead"
// pid is unsafe (the kernel may have recycled it), so we use values
// outside the kernel's pid range.
func TestProcessAlive_NonExistentPidIsDead(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{-1, 0} {
		if processAlive(pid) {
			t.Errorf("processAlive(%d) = true; want false (non-positive pid is dead)", pid)
		}
	}
	// Linux defaults pid_max to 2^22 = 4194304; macOS caps below
	// 99999. A pid above 2^31 cannot exist on either kernel, so
	// Signal(0) returns ESRCH and the helper reports dead.
	if processAlive(2147483646) {
		t.Errorf("processAlive(2147483646) = true; want false (pid above kernel range is dead)")
	}
}
