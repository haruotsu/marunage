package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/version"
)

func TestRunVersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"--version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("run --version exit code = %d; want 0; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != version.Version() {
		t.Errorf("stdout = %q; want %q", got, version.Version())
	}
}

func TestRunUnknownFlagReturnsNonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"--no-such-flag"}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("run --no-such-flag exit code = 0; want non-zero")
	}
}
