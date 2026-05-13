package doctor

// Internal-only tests for the small parsing helpers that probeBinary leans
// on. These functions are private but their behavior on weird input
// (Windows CRLF, two-component "3.12", no version at all) is what decides
// whether the version-floor check correctly rejects an old python or
// silently accepts garbage. Pin the contract directly.

import (
	"testing"

	"github.com/haruotsu/marunage/internal/config"
)

// TestBackendIs_FlipsBetweenCmuxAndHerdr pins the
// required/optional split between the two backends. doctor uses
// backendIs to decide whether cmux or herdr is the "hard requirement"
// for the current config; without this test a regression that hard-
// codes one backend would silently pass.
func TestBackendIs_FlipsBetweenCmuxAndHerdr(t *testing.T) {
	cases := []struct {
		name        string
		cfgBackend  string
		wantCmux    bool
		wantHerdr   bool
		description string
	}{
		{"empty defaults to cmux", "", true, false, "older config.toml without backend field"},
		{"explicit cmux", "cmux", true, false, "default selection"},
		{"explicit herdr", "herdr", false, true, "opt-in to herdr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Execution.Backend = tc.cfgBackend

			gotCmux := backendIs("cmux")(cfg)
			gotHerdr := backendIs("herdr")(cfg)
			if gotCmux != tc.wantCmux {
				t.Errorf("backendIs(cmux) = %v; want %v (%s)", gotCmux, tc.wantCmux, tc.description)
			}
			if gotHerdr != tc.wantHerdr {
				t.Errorf("backendIs(herdr) = %v; want %v (%s)", gotHerdr, tc.wantHerdr, tc.description)
			}
		})
	}
}

func TestExtractSemver(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain three-part", "Python 3.12.1", "3.12.1"},
		{"two-part is enough", "go version go1.22 linux/amd64", "1.22"},
		{"trailing newline trimmed", "Python 3.11.9\n", "3.11.9"},
		{"crlf trimmed too", "Python 3.11.9\r\n", "3.11.9"},
		{"surrounding whitespace", "  3.10.0  ", "3.10.0"},
		{"version embedded mid-banner", "gh version 2.55.0 (2024-01-01)", "2.55.0"},
		{"no version at all", "no version here", ""},
		{"empty string", "", ""},
		{"only major number", "v3", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractSemver(tc.raw); got != tc.want {
				t.Errorf("extractSemver(%q) = %q; want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseMajorMinor(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		major  int
		minor  int
		wantOK bool
	}{
		{"x.y.z", "3.12.1", 3, 12, true},
		{"x.y", "3.11", 3, 11, true},
		{"empty", "", 0, 0, false},
		{"single component", "3", 0, 0, false},
		{"non-numeric major", "abc.1", 0, 0, false},
		{"non-numeric minor", "3.abc", 0, 0, false},
		{"trailing dot", "3.", 0, 0, false},
		{"leading dot", ".3", 0, 0, false},
		{"big version", "100.200.300", 100, 200, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			major, minor, ok := parseMajorMinor(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("parseMajorMinor(%q) ok = %v; want %v", tc.in, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if major != tc.major || minor != tc.minor {
				t.Errorf("parseMajorMinor(%q) = (%d, %d); want (%d, %d)",
					tc.in, major, minor, tc.major, tc.minor)
			}
		})
	}
}
