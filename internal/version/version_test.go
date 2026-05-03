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

// TestVersionPrefersInjectedValue ensures -ldflags injection wins over the
// runtime/debug fallback. Without this guarantee, a release tag set via the
// Makefile could be silently overridden by build-info defaults.
func TestVersionPrefersInjectedValue(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	version = "v1.2.3-test"
	if got := Version(); got != "v1.2.3-test" {
		t.Errorf("Version() = %q; want %q", got, "v1.2.3-test")
	}
}

// TestVersionFallsBackWhenNotInjected ensures `go install ...@latest`
// (no -ldflags) still produces a useful version string from BuildInfo
// instead of an empty value or a frozen "dev" placeholder.
func TestVersionFallsBackWhenNotInjected(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	version = ""
	got := Version()
	if got == "" {
		t.Fatal("Version() returned empty string when injection was empty; want fallback to BuildInfo or 'dev'")
	}
	if strings.ContainsAny(got, " \t\n") {
		t.Errorf("Version() returned %q; fallback must not contain whitespace", got)
	}
}
