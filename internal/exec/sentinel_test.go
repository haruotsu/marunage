package exec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/exec"
)

func TestReadExitCodeReturnsCodeWhenPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exec.SentinelFile), []byte("0\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	code, ok, err := exec.ReadExitCode(filepath.Join(dir, exec.SentinelFile))
	if err != nil {
		t.Fatalf("ReadExitCode: %v", err)
	}
	if !ok || code != 0 {
		t.Errorf("ReadExitCode = (%d, %v); want (0, true)", code, ok)
	}
}

func TestReadExitCodeAbsentFileIsNotReady(t *testing.T) {
	dir := t.TempDir()
	_, ok, err := exec.ReadExitCode(filepath.Join(dir, exec.SentinelFile))
	if err != nil {
		t.Fatalf("ReadExitCode on absent file: %v", err)
	}
	if ok {
		t.Error("ok = true for absent sentinel; want false")
	}
}

func TestReadExitCodeRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, exec.SentinelFile)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, _, err := exec.ReadExitCode(link); err == nil {
		t.Fatal("ReadExitCode err = nil; want symlink rejection")
	}
}

func TestReadExitCodeRejectsUnparseable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exec.SentinelFile), []byte("nope"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if _, _, err := exec.ReadExitCode(filepath.Join(dir, exec.SentinelFile)); err == nil {
		t.Fatal("ReadExitCode err = nil; want parse error")
	}
}

func TestAwaitSentinelReturnsCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exec.SentinelFile), []byte("127"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	code, err := exec.AwaitSentinel(context.Background(), dir, time.Millisecond, 0)
	if err != nil {
		t.Fatalf("AwaitSentinel: %v", err)
	}
	if code != 127 {
		t.Errorf("code = %d; want 127", code)
	}
}

func TestAwaitSentinelTimesOut(t *testing.T) {
	dir := t.TempDir()
	_, err := exec.AwaitSentinel(context.Background(), dir, time.Millisecond, 20*time.Millisecond)
	if !errors.Is(err, exec.ErrAwaitTimeout) {
		t.Errorf("err = %v; want ErrAwaitTimeout", err)
	}
}

func TestAwaitSentinelRejectsEmptyDir(t *testing.T) {
	_, err := exec.AwaitSentinel(context.Background(), "", time.Millisecond, 0)
	if !errors.Is(err, exec.ErrNoSentinelDir) {
		t.Errorf("err = %v; want ErrNoSentinelDir", err)
	}
}

func TestAwaitSentinelHonoursContextCancel(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := exec.AwaitSentinel(ctx, dir, time.Millisecond, 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
}
