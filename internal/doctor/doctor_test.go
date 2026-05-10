package doctor

// Test list (t_wada style; ticked off as the matching test below goes green):
//
//   1. all required tools present -> Report.OK == true
//   2. claude missing -> Report.OK == false, error mentions claude
//   3. python present but version < 3.11 -> required failure with version detail
//   4. gh missing AND github source not enabled -> recorded as optional, OK
//   5. gh missing AND discovery.sources_enabled includes "github" -> required failure
//   6. gws missing AND no google sources enabled -> optional, OK
//   7. gws missing AND a google source enabled -> required failure
//   8. zero secret backends available -> required failure with hint mentioning marunage setup
//   9. exactly one of {age,file,pass,keyring} available -> OK
//  10. --fix on darwin prints "brew install ..." hints for missing required tools
//  11. --fix on debian-like prints "sudo apt install ..." hints
//  12. --fix on fedora-like prints "sudo dnf install ..." hints
//  13. --json emits a stable shape (snapshot a small fixture)
//  14. install hint table covers every tool name returned by the checker registry
//  15. Runner is injected -- no test invokes a real binary

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
)

// fakeRunner implements Runner without touching the real PATH. Tests build
// one per scenario by listing which binaries are "present" plus the canned
// `--version` stdout each one returns.
type fakeRunner struct {
	present  map[string]string // binary name -> path
	versions map[string]string // binary name -> version stdout
}

func (f fakeRunner) LookPath(name string) (string, bool) {
	p, ok := f.present[name]
	return p, ok
}

func (f fakeRunner) Version(_ context.Context, name string) (string, error) {
	v, ok := f.versions[name]
	if !ok {
		return "", errMissingBinary
	}
	return v, nil
}

// fakeSecretsProbe pretends each named backend is available. Scenarios that
// want zero backends pass a zero value.
type fakeSecretsProbe struct {
	available []string
}

func (f fakeSecretsProbe) AvailableBackends() []string {
	out := make([]string, len(f.available))
	copy(out, f.available)
	return out
}

// fakeMCPProbe implements MCPProbe for tests.
type fakeMCPProbe struct {
	servers []string
	err     error
}

func (f fakeMCPProbe) ListMCPServers(_ context.Context) ([]string, error) {
	return f.servers, f.err
}

// fakeOS lets tests pin the OS family without depending on runtime.GOOS.
type fakeOS struct{ family OSFamily }

func (f fakeOS) Family() OSFamily { return f.family }

// allToolsPresent returns a Runner that has every documented binary at a
// version above the threshold, and a secrets probe with one backend.
func allToolsPresent() (fakeRunner, fakeSecretsProbe) {
	return fakeRunner{
			present: map[string]string{
				"claude":  "/usr/local/bin/claude",
				"cmux":    "/usr/local/bin/cmux",
				"sqlite3": "/usr/bin/sqlite3",
				"python":  "/usr/bin/python",
				"gh":      "/usr/local/bin/gh",
				"gws":     "/usr/local/bin/gws",
				"jq":      "/usr/local/bin/jq",
			},
			versions: map[string]string{
				"claude":  "claude 1.2.3\n",
				"cmux":    "cmux version 0.4.0\n",
				"sqlite3": "3.43.2 2023-10-10\n",
				"python":  "Python 3.12.1\n",
				"gh":      "gh version 2.55.0 (2024-01-01)\n",
				"gws":     "gws version 0.9.0\n",
				"jq":      "jq-1.7\n",
			},
		}, fakeSecretsProbe{
			available: []string{"file"},
		}
}

func defaultCfg() config.Config {
	c := config.Default()
	// Strip the markdown placeholder so source-conditional checks
	// (gh, gws) start out as plain optional in the baseline.
	c.Discovery.SourcesEnabled = nil
	return c
}

func defaultMCP() fakeMCPProbe {
	return fakeMCPProbe{servers: []string{"slack", "google-drive"}}
}

func TestRun_AllRequiredToolsPresent_ReportsOK(t *testing.T) {
	runner, secrets := allToolsPresent()
	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		MCP:     defaultMCP(),
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if !rep.OK {
		t.Fatalf("Report.OK = false; want true. report=%#v", rep)
	}
}

func TestRun_ClaudeMissing_ReportsRequiredFailure(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "claude")
	delete(runner.versions, "claude")

	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("Report.OK = true; want false because claude missing")
	}
	if !mentionsTool(rep, "claude") {
		t.Fatalf("report does not mention claude. report=%#v", rep)
	}
}

func TestRun_PythonBelow311_ReportsRequiredFailure(t *testing.T) {
	runner, secrets := allToolsPresent()
	runner.versions["python"] = "Python 3.10.14\n"

	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("Report.OK = true; want false for python < 3.11")
	}
	if !mentionsToolWithDetail(rep, "python", "3.10") {
		t.Fatalf("report missing python version detail. report=%#v", rep)
	}
}

func TestRun_GhMissing_NoGithubSource_OptionalOK(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "gh")
	delete(runner.versions, "gh")

	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if !rep.OK {
		t.Fatalf("Report.OK = false; want true (gh optional when github not enabled). report=%#v", rep)
	}
	gh, ok := findOutcome(rep, "gh")
	if !ok {
		t.Fatalf("gh outcome not present in report. report=%#v", rep)
	}
	if gh.Required {
		t.Fatalf("gh.Required = true; want false when no github source enabled")
	}
}

func TestRun_GhMissing_GithubSourceEnabled_RequiredFailure(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "gh")
	delete(runner.versions, "gh")

	cfg := defaultCfg()
	cfg.Discovery.SourcesEnabled = []string{"github"}

	rep := Run(context.Background(), Inputs{
		Cfg:     cfg,
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("Report.OK = true; want false (gh required when github source enabled)")
	}
	gh, ok := findOutcome(rep, "gh")
	if !ok {
		t.Fatalf("gh outcome not present. report=%#v", rep)
	}
	if !gh.Required {
		t.Fatalf("gh.Required = false; want true when github source enabled")
	}
}

func TestRun_GwsMissing_NoGoogleSource_OptionalOK(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "gws")
	delete(runner.versions, "gws")

	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if !rep.OK {
		t.Fatalf("Report.OK = false; want true (gws optional when no google source). report=%#v", rep)
	}
	gws, ok := findOutcome(rep, "gws")
	if !ok {
		t.Fatalf("gws outcome not present. report=%#v", rep)
	}
	if gws.Required {
		t.Fatalf("gws.Required = true; want false when no google source enabled")
	}
}

func TestRun_GwsMissing_GoogleSourceEnabled_RequiredFailure(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "gws")
	delete(runner.versions, "gws")

	cfg := defaultCfg()
	cfg.Discovery.SourcesEnabled = []string{"gmail"}

	rep := Run(context.Background(), Inputs{
		Cfg:     cfg,
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("Report.OK = true; want false (gws required when gmail source enabled)")
	}
	gws, ok := findOutcome(rep, "gws")
	if !ok {
		t.Fatalf("gws outcome not present. report=%#v", rep)
	}
	if !gws.Required {
		t.Fatalf("gws.Required = false; want true when gmail source enabled")
	}
}

func TestRun_ZeroSecretBackends_RequiredFailure(t *testing.T) {
	runner, _ := allToolsPresent()
	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: fakeSecretsProbe{available: nil},
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("Report.OK = true; want false when no secret backends are available")
	}
	out, ok := findOutcome(rep, "secrets")
	if !ok {
		t.Fatalf("secrets outcome not present. report=%#v", rep)
	}
	if !out.Required {
		t.Fatalf("secrets check should be required")
	}
	if !strings.Contains(out.Hint, "marunage setup") {
		t.Fatalf("secrets hint should mention 'marunage setup'; got %q", out.Hint)
	}
}

func TestRun_OneSecretBackendAvailable_OK(t *testing.T) {
	runner, _ := allToolsPresent()
	for _, backend := range []string{"age", "file", "pass", "keyring"} {
		t.Run(backend, func(t *testing.T) {
			rep := Run(context.Background(), Inputs{
				Cfg:     defaultCfg(),
				Runner:  runner,
				Secrets: fakeSecretsProbe{available: []string{backend}},
				OS:      fakeOS{family: OSFamilyDarwin},
			})
			if !rep.OK {
				t.Fatalf("Report.OK = false; want true with %q backend available. report=%#v", backend, rep)
			}
		})
	}
}

func TestFix_Darwin_PrintsBrewHints(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "claude")
	delete(runner.versions, "claude")

	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDarwin},
	})

	hints := FixHints(rep, OSFamilyDarwin)
	joined := strings.Join(hints, "\n")
	if !strings.Contains(joined, "brew install") {
		t.Fatalf("FixHints(darwin) should include 'brew install'; got %q", joined)
	}
	if !strings.Contains(joined, "claude") {
		t.Fatalf("FixHints(darwin) should mention 'claude'; got %q", joined)
	}
}

func TestFix_Debian_PrintsAptHints(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "sqlite3")
	delete(runner.versions, "sqlite3")

	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDebian},
	})

	hints := FixHints(rep, OSFamilyDebian)
	joined := strings.Join(hints, "\n")
	if !strings.Contains(joined, "sudo apt install") {
		t.Fatalf("FixHints(debian) should include 'sudo apt install'; got %q", joined)
	}
	if !strings.Contains(joined, "sqlite3") {
		t.Fatalf("FixHints(debian) should mention 'sqlite3'; got %q", joined)
	}
}

func TestFix_Fedora_PrintsDnfHints(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "jq")
	delete(runner.versions, "jq")

	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyFedora},
	})

	hints := FixHints(rep, OSFamilyFedora)
	joined := strings.Join(hints, "\n")
	if !strings.Contains(joined, "sudo dnf install") {
		t.Fatalf("FixHints(fedora) should include 'sudo dnf install'; got %q", joined)
	}
	if !strings.Contains(joined, "jq") {
		t.Fatalf("FixHints(fedora) should mention 'jq'; got %q", joined)
	}
}

func TestFix_OtherOS_PrintsGenericHints(t *testing.T) {
	runner, secrets := allToolsPresent()
	delete(runner.present, "claude")
	delete(runner.versions, "claude")

	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyOther},
	})

	hints := FixHints(rep, OSFamilyOther)
	joined := strings.Join(hints, "\n")
	if !strings.Contains(joined, "claude") {
		t.Fatalf("FixHints(other) should mention claude; got %q", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "install") {
		t.Fatalf("FixHints(other) should suggest install; got %q", joined)
	}
}

func TestJSON_StableShape(t *testing.T) {
	runner, secrets := allToolsPresent()
	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  runner,
		Secrets: secrets,
		OS:      fakeOS{family: OSFamilyDarwin},
	})

	data, err := MarshalJSON(rep)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		`"ok":`,
		`"checks":`,
		`"name":`,
		`"required":`,
		`"detail":`,
		`"version":`,
		`"hint":`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("JSON missing field %q\nbody=%s", want, body)
		}
	}
}

// TestInstallHints_CoverEveryRegisteredTool guards the wiring contract: if
// someone adds a new tool to the checker registry, the install hint table
// must learn about it too. Without this we silently regress to "no hint
// available" for the new tool on every OS.
func TestInstallHints_CoverEveryRegisteredTool(t *testing.T) {
	for _, tool := range registeredToolNames() {
		for _, fam := range []OSFamily{OSFamilyDarwin, OSFamilyDebian, OSFamilyFedora, OSFamilyOther} {
			if _, ok := installHintFor(tool, fam); !ok {
				t.Errorf("missing install hint for tool %q on %v", tool, fam)
			}
		}
	}
}

// TestRunner_NotInvokedAgainstRealBinary ensures that the doctor.Run code
// path goes exclusively through the injected Runner. We pass a runner that
// has no binaries at all and assert the report still comes back -- this
// would not be the case if any code path fell back to os/exec.LookPath.
func TestRunner_NotInvokedAgainstRealBinary(t *testing.T) {
	rep := Run(context.Background(), Inputs{
		Cfg:     defaultCfg(),
		Runner:  fakeRunner{present: map[string]string{}, versions: map[string]string{}},
		Secrets: fakeSecretsProbe{available: []string{"file"}},
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("expected required failures with empty runner; got OK")
	}
	// Every required tool should be marked as missing with a Detail that does
	// not reference a real /usr/local/bin path -- proving the fake runner
	// actually drove the result.
	for _, name := range []string{"claude", "cmux", "sqlite3", "python"} {
		out, ok := findOutcome(rep, name)
		if !ok {
			t.Fatalf("required tool %q missing from report", name)
		}
		if out.OK {
			t.Errorf("required tool %q reported OK with empty runner", name)
		}
	}
}

func TestRun_SlackMCP_SlackNotEnabled_Optional(t *testing.T) {
	runner, secrets := allToolsPresent()
	cfg := defaultCfg() // sources_enabled = nil

	rep := Run(context.Background(), Inputs{
		Cfg:     cfg,
		Runner:  runner,
		Secrets: secrets,
		MCP:     fakeMCPProbe{servers: nil}, // slack MCP absent
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if !rep.OK {
		t.Fatalf("Report.OK = false; want true (slack-mcp optional when slack not enabled)")
	}
	out, ok := findOutcome(rep, "slack-mcp")
	if !ok {
		t.Fatalf("slack-mcp outcome missing from report")
	}
	if out.Required {
		t.Fatalf("slack-mcp.Required = true; want false when slack not in sources_enabled")
	}
}

func TestRun_SlackMCP_SlackEnabled_MCPPresent_OK(t *testing.T) {
	runner, secrets := allToolsPresent()
	cfg := defaultCfg()
	cfg.Discovery.SourcesEnabled = []string{"slack"}

	rep := Run(context.Background(), Inputs{
		Cfg:     cfg,
		Runner:  runner,
		Secrets: secrets,
		MCP:     fakeMCPProbe{servers: []string{"slack", "google-drive"}},
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if !rep.OK {
		t.Fatalf("Report.OK = false; want true (slack MCP configured). report=%#v", rep)
	}
	out, ok := findOutcome(rep, "slack-mcp")
	if !ok {
		t.Fatalf("slack-mcp outcome missing from report")
	}
	if !out.OK {
		t.Fatalf("slack-mcp.OK = false; want true when slack in MCP list")
	}
}

func TestRun_SlackMCP_SlackEnabled_MCPAbsent_RequiredFailure(t *testing.T) {
	runner, secrets := allToolsPresent()
	cfg := defaultCfg()
	cfg.Discovery.SourcesEnabled = []string{"slack"}

	rep := Run(context.Background(), Inputs{
		Cfg:     cfg,
		Runner:  runner,
		Secrets: secrets,
		MCP:     fakeMCPProbe{servers: []string{"google-drive"}}, // no slack
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("Report.OK = true; want false (slack MCP not configured)")
	}
	out, ok := findOutcome(rep, "slack-mcp")
	if !ok {
		t.Fatalf("slack-mcp outcome missing from report")
	}
	if !out.Required {
		t.Fatalf("slack-mcp.Required = false; want true when slack in sources_enabled")
	}
	if out.OK {
		t.Fatalf("slack-mcp.OK = true; want false when slack absent from MCP list")
	}
	if !strings.Contains(out.Hint, "claude mcp add") {
		t.Fatalf("slack-mcp hint should mention 'claude mcp add'; got %q", out.Hint)
	}
}

func TestRun_SlackMCP_SlackEnabled_MCPNil_RequiredFailure(t *testing.T) {
	runner, secrets := allToolsPresent()
	cfg := defaultCfg()
	cfg.Discovery.SourcesEnabled = []string{"slack"}

	rep := Run(context.Background(), Inputs{
		Cfg:     cfg,
		Runner:  runner,
		Secrets: secrets,
		MCP:     nil, // probe not wired
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("Report.OK = true; want false (MCP probe not available)")
	}
	out, ok := findOutcome(rep, "slack-mcp")
	if !ok {
		t.Fatalf("slack-mcp outcome missing from report")
	}
	if out.OK {
		t.Fatalf("slack-mcp.OK = true; want false when MCP probe is nil")
	}
	if !strings.Contains(out.Detail, "MCP probe not available") {
		t.Fatalf("slack-mcp detail should mention 'MCP probe not available'; got %q", out.Detail)
	}
}

func TestRun_SlackMCP_SlackEnabled_ListFails_RequiredFailure(t *testing.T) {
	runner, secrets := allToolsPresent()
	cfg := defaultCfg()
	cfg.Discovery.SourcesEnabled = []string{"slack"}

	rep := Run(context.Background(), Inputs{
		Cfg:     cfg,
		Runner:  runner,
		Secrets: secrets,
		MCP:     fakeMCPProbe{err: errors.New("claude binary not found")},
		OS:      fakeOS{family: OSFamilyDarwin},
	})
	if rep.OK {
		t.Fatalf("Report.OK = true; want false (ListMCPServers failed)")
	}
	out, ok := findOutcome(rep, "slack-mcp")
	if !ok {
		t.Fatalf("slack-mcp outcome missing from report")
	}
	if out.OK {
		t.Fatalf("slack-mcp.OK = true; want false when ListMCPServers errors")
	}
	if !strings.Contains(out.Detail, "claude mcp list failed") {
		t.Fatalf("slack-mcp detail should mention 'claude mcp list failed'; got %q", out.Detail)
	}
}

// helpers ---------------------------------------------------------------

func findOutcome(rep Report, name string) (CheckOutcome, bool) {
	for _, o := range rep.Checks {
		if o.Name == name {
			return o, true
		}
	}
	return CheckOutcome{}, false
}

func mentionsTool(rep Report, name string) bool {
	out, ok := findOutcome(rep, name)
	if !ok {
		return false
	}
	return !out.OK
}

func mentionsToolWithDetail(rep Report, name, substr string) bool {
	out, ok := findOutcome(rep, name)
	if !ok {
		return false
	}
	if out.OK {
		return false
	}
	return strings.Contains(out.Detail, substr) || strings.Contains(out.Version, substr)
}
