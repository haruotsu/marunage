package version

import (
	"strings"
	"testing"
)

func TestVersionIsNotEmpty(t *testing.T) {
	got := Version()
	if got == "" {
		t.Fatal("Version() returned empty string; want non-empty")
	}
}

func TestVersionDoesNotContainWhitespace(t *testing.T) {
	got := Version()
	if strings.ContainsAny(got, " \t\n") {
		t.Errorf("Version() returned %q; must not contain whitespace", got)
	}
}
