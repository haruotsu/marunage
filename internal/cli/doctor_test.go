package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestDoctor_RunsAndExits exercises the wired-in `marunage doctor` command
// end-to-end. We don't pin per-tool results (those depend on the host's
// $PATH and are covered in internal/doctor); we only pin the contract the
// CLI layer owns: --help works, --json prints parseable JSON, and the
// command does not panic.
func TestDoctor_HelpSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"doctor", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor --help exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "doctor") {
		t.Errorf("doctor --help output missing usage; got %q", stdout.String())
	}
}

func TestDoctor_JSONIsParseable(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	// We do NOT assert the exit code: on a CI host without claude/cmux it
	// will be 1, on a developer machine with everything installed it will
	// be 0. Either way the JSON shape must parse.
	_ = Execute([]string{"--config", path, "doctor", "--json"}, &stdout, &stderr)

	body := stdout.Bytes()
	if len(body) == 0 {
		t.Fatalf("doctor --json wrote no stdout; stderr=%q", stderr.String())
	}

	var parsed struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
			OK       bool   `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("doctor --json output is not valid JSON: %v\nbody=%s", err, body)
	}
	if len(parsed.Checks) == 0 {
		t.Errorf("doctor --json reported zero checks; expected at least the required tools")
	}
}

// TestDoctor_NoLongerLeafStub ensures the migration from "stub" to "real
// implementation" is complete: invoking `marunage doctor` (no flags) must
// no longer print "not yet implemented", regardless of whether the host
// happens to have every tool installed.
func TestDoctor_NoLongerLeafStub(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	_ = Execute([]string{"--config", path, "doctor"}, &stdout, &stderr)

	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "not yet implemented") {
		t.Errorf("doctor still routed to the not-yet-implemented stub\noutput:\n%s", combined)
	}
}
