package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeRegistryTarball assembles the tar.gz body the fixture registry
// will hand back. Centralising the helper keeps the per-test wiring
// short.
func makeRegistryTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o600,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return raw.Bytes()
}

// startFixtureRegistry stands up a tiny in-memory registry that
// publishes one named skill at one version. The test owns the
// returned URL via t.Cleanup.
func startFixtureRegistry(t *testing.T, name, version, body string) string {
	t.Helper()
	tarball := makeRegistryTarball(t, map[string]string{
		name + "/SKILL.md": body,
	})
	sum := sha256.Sum256(tarball)
	hexSum := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"schema_version": 1,
			"skills": [
				{"name": %q, "latest": %q, "description": "test fixture"}
			]
		}`, name, version)
	})
	mux.HandleFunc("/skills/"+name+"/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		base := "http://" + r.Host
		fmt.Fprintf(w, `{
			"schema_version": 1,
			"name": %q,
			"versions": [
				{"version": %q, "tarball_url": "%s/skills/%s/%s.tar.gz", "sha256": %q}
			]
		}`, name, version, base, name, version, hexSum)
	})
	mux.HandleFunc("/skills/"+name+"/"+version+".tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestSkills_Install_WritesSkillUnderHome pins the end-to-end CLI:
// `marunage skills install <name> --registry <url>` puts the
// SKILL.md under ~/.claude/skills/<name>/SKILL.md.
func TestSkills_Install_WritesSkillUnderHome(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	url := startFixtureRegistry(t, "marunage-source-jira", "0.1.0",
		"<!-- version: 0.1.0 -->\n# jira\n")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "install", "marunage-source-jira", "--registry", url}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}

	body, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "marunage-source-jira", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(body), "version: 0.1.0") {
		t.Errorf("SKILL.md = %q", body)
	}
	if !strings.Contains(stdout.String(), "marunage-source-jira") {
		t.Errorf("stdout missing skill name; got %q", stdout.String())
	}
}

// TestSkills_Install_WithoutRegistryURL_Errors pins the operator
// safety net: the CLI must refuse to make a network call when no URL
// has been configured.
func TestSkills_Install_WithoutRegistryURL_Errors(t *testing.T) {
	withHomeDir(t, t.TempDir())
	t.Setenv(EnvRegistryURL, "")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "install", "marunage-source-jira"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit=0; want non-zero")
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "registry URL") {
		t.Errorf("error message should mention 'registry URL'; got %q", combined)
	}
}

// TestSkills_Install_HonoursEnvVar pins the env-var override path so
// scripted use does not need to thread --registry through every
// invocation.
func TestSkills_Install_HonoursEnvVar(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	url := startFixtureRegistry(t, "marunage-source-x", "0.1.0",
		"<!-- version: 0.1.0 -->\n# x\n")
	t.Setenv(EnvRegistryURL, url)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "install", "marunage-source-x"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "marunage-source-x", "SKILL.md")); err != nil {
		t.Errorf("expected SKILL.md to exist: %v", err)
	}
}

// TestSkills_Install_RefusesEmbeddedNameByDefault pins the conflict
// guard at the CLI surface, mirroring the registry-package contract.
func TestSkills_Install_RefusesEmbeddedNameByDefault(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)
	t.Setenv(EnvRegistryURL, "https://example")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "install", "marunage-triage"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit=0; want non-zero on embedded name")
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "embedded") {
		t.Errorf("error should mention embedded conflict; got %q", combined)
	}
}

// TestSkills_List_RendersInstalledSkills pins the read-only `list`
// surface: a state file with one entry produces one row of output.
func TestSkills_List_RendersInstalledSkills(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)
	root := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	state := map[string]any{
		"schema_version": 1,
		"installed": []map[string]any{
			{"name": "marunage-source-jira", "version": "0.1.0", "source": "https://example"},
		},
	}
	body, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(root, ".marunage-registry.json"), body, 0o600); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "marunage-source-jira") {
		t.Errorf("stdout missing entry; got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "0.1.0") {
		t.Errorf("stdout missing version; got %q", stdout.String())
	}
}

// TestSkills_List_EmptyState pins the empty-state UX so a fresh user
// gets an explicit message rather than silence.
func TestSkills_List_EmptyState(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No registry-installed skills") {
		t.Errorf("stdout missing empty-state message; got %q", stdout.String())
	}
}

// TestSkills_Search_RendersIndexEntries pins the catalog query path.
func TestSkills_Search_RendersIndexEntries(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	url := startFixtureRegistry(t, "marunage-source-jira", "0.1.0",
		"<!-- version: 0.1.0 -->\n# jira\n")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "search", "jira", "--registry", url}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "marunage-source-jira") {
		t.Errorf("stdout missing entry; got %q", stdout.String())
	}
}

// TestSkills_Update_UpgradesOutdatedSkill pins the differential
// `update` command: a skill recorded at an older version is bumped
// to the registry's latest.
func TestSkills_Update_UpgradesOutdatedSkill(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)
	root := filepath.Join(home, ".claude", "skills")

	// Seed an "older" install in the state file.
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	state := map[string]any{
		"schema_version": 1,
		"installed": []map[string]any{
			{"name": "marunage-source-x", "version": "0.0.1", "source": "https://old"},
		},
	}
	body, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(root, ".marunage-registry.json"), body, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	url := startFixtureRegistry(t, "marunage-source-x", "0.2.0",
		"<!-- version: 0.2.0 -->\n# x\n")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "update", "--registry", url}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0.0.1 -> 0.2.0") {
		t.Errorf("stdout missing version transition; got %q", stdout.String())
	}
	got, err := os.ReadFile(filepath.Join(root, "marunage-source-x", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(got), "version: 0.2.0") {
		t.Errorf("SKILL.md not bumped; got %q", got)
	}
}

// TestSkills_Update_AllUpToDate pins the no-op branch: if the only
// installed skill is already at the registry latest, the command
// reports it instead of silently doing nothing.
func TestSkills_Update_AllUpToDate(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)
	root := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	state := map[string]any{
		"schema_version": 1,
		"installed": []map[string]any{
			{"name": "marunage-source-x", "version": "0.1.0"},
		},
	}
	body, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(root, ".marunage-registry.json"), body, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	url := startFixtureRegistry(t, "marunage-source-x", "0.1.0",
		"<!-- version: 0.1.0 -->\n# x\n")

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"skills", "update", "--registry", url}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "latest version") {
		t.Errorf("stdout missing up-to-date message; got %q", stdout.String())
	}
}
