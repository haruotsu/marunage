package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/doctor"
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

// TestDoctor_RuntimeIsInjectable pins the seam the doctor.go comment
// promises ("overridable in tests"). Tests must be able to swap in fake
// Runner / SecretsProbe / OSDetector implementations without invoking
// the real os/exec PATH lookup, otherwise the CLI layer's behavior is
// host-dependent and cannot be exercised deterministically.
func TestDoctor_RuntimeIsInjectable(t *testing.T) {
	path := configPathFlag(t)

	withDoctorRuntime(t, doctorRuntimeOverride{
		Inputs: func(cfg config.Config) doctor.Inputs {
			return doctor.Inputs{
				Cfg: cfg,
				Runner: stubRunner{
					present: map[string]string{
						"claude":  "/fake/claude",
						"cmux":    "/fake/cmux",
						"sqlite3": "/fake/sqlite3",
						"python":  "/fake/python",
					},
					versions: map[string]string{
						"claude":  "claude 1.0.0",
						"cmux":    "cmux 0.4.0",
						"sqlite3": "3.43.2",
						"python":  "Python 3.12.1",
					},
				},
				Secrets: stubSecrets{available: []string{"file"}},
				OS:      stubOS{family: doctor.OSFamilyDarwin},
			}
		},
		Family: func() doctor.OSFamily { return doctor.OSFamilyDarwin },
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "doctor", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor with all-fake-present runtime exit=%d; want 0; stderr=%q", code, stderr.String())
	}

	body := stdout.String()
	if !strings.Contains(body, `"name": "claude"`) {
		t.Errorf("JSON body missing claude entry; body=%s", body)
	}
	// Critical: the path must be the FAKE path, proving the injected
	// runner is what drove the result rather than the real os/exec.
	if !strings.Contains(body, "/fake/claude") {
		t.Errorf("JSON body did not include injected fake claude path; body=%s", body)
	}
}

type stubRunner struct {
	present  map[string]string
	versions map[string]string
}

func (s stubRunner) LookPath(name string) (string, bool) {
	p, ok := s.present[name]
	return p, ok
}

func (s stubRunner) Version(_ context.Context, name string) (string, error) {
	v, ok := s.versions[name]
	if !ok {
		return "", errStubMissing
	}
	return v, nil
}

var errStubMissing = errStub("stub: binary not present")

type errStub string

func (e errStub) Error() string { return string(e) }

type stubSecrets struct{ available []string }

func (s stubSecrets) AvailableBackends() []string {
	out := make([]string, len(s.available))
	copy(out, s.available)
	return out
}

type stubOS struct{ family doctor.OSFamily }

func (s stubOS) Family() doctor.OSFamily { return s.family }
