package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureRegistry stands up a tiny in-memory registry that publishes
// one skill at one version. Returns the test server URL and the
// digest of the served tarball so tests can assert on the recorded
// state.
type fixtureRegistry struct {
	URL       string
	Tarball   []byte
	SHA256Hex string
}

func newFixtureRegistry(t *testing.T, name, version, body string) fixtureRegistry {
	t.Helper()
	tarball := makeTarball(t, map[string]string{
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
				{"name": %q, "latest": %q, "description": "fixture"}
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
	return fixtureRegistry{URL: srv.URL, Tarball: tarball, SHA256Hex: hexSum}
}

// TestInstaller_HappyPath_WritesSkillAndState pins the basic
// end-to-end install: the SKILL.md is on disk and the state file is
// updated.
func TestInstaller_HappyPath_WritesSkillAndState(t *testing.T) {
	fix := newFixtureRegistry(t, "marunage-source-jira", "0.1.0",
		"<!-- version: 0.1.0 -->\n# jira\n")
	root := filepath.Join(t.TempDir(), ".claude", "skills")

	in := &Installer{
		Client:     &Client{BaseURL: fix.URL},
		SkillsRoot: root,
		Clock:      func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	rep, err := in.Install(context.Background(), InstallOptions{Name: "marunage-source-jira"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if rep.NewVersion != "0.1.0" {
		t.Errorf("NewVersion = %q; want 0.1.0", rep.NewVersion)
	}

	body, err := os.ReadFile(filepath.Join(root, "marunage-source-jira", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(body), "version: 0.1.0") {
		t.Errorf("SKILL.md body wrong: %q", body)
	}

	state, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	rec, ok := state.Find("marunage-source-jira")
	if !ok {
		t.Fatalf("state missing marunage-source-jira")
	}
	if rec.Version != "0.1.0" {
		t.Errorf("state Version = %q", rec.Version)
	}
	if rec.SHA256 != fix.SHA256Hex {
		t.Errorf("state SHA256 = %q; want %q", rec.SHA256, fix.SHA256Hex)
	}
	if rec.UpdatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("state UpdatedAt = %q", rec.UpdatedAt)
	}
}

// TestInstaller_RefusesEmbeddedNameByDefault pins the conflict-with-
// PR-34 guard: the registry must refuse `install marunage-triage`
// unless the operator opts in explicitly.
func TestInstaller_RefusesEmbeddedNameByDefault(t *testing.T) {
	in := &Installer{
		Client:     &Client{BaseURL: "https://example"},
		SkillsRoot: t.TempDir(),
	}
	_, err := in.Install(context.Background(), InstallOptions{Name: "marunage-triage"})
	if !errors.Is(err, ErrEmbeddedConflict) {
		t.Errorf("err = %v; want errors.Is(_, ErrEmbeddedConflict)", err)
	}
}

// TestInstaller_RecordsPreviousVersion pins the upgrade path:
// re-installing the same skill at a new version reports the previous
// pinned version under OldVersion.
func TestInstaller_RecordsPreviousVersion(t *testing.T) {
	fix1 := newFixtureRegistry(t, "marunage-source-x", "0.1.0", "<!-- version: 0.1.0 -->\n# x\n")
	root := filepath.Join(t.TempDir(), ".claude", "skills")

	in1 := &Installer{Client: &Client{BaseURL: fix1.URL}, SkillsRoot: root}
	if _, err := in1.Install(context.Background(), InstallOptions{Name: "marunage-source-x"}); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	fix2 := newFixtureRegistry(t, "marunage-source-x", "0.2.0", "<!-- version: 0.2.0 -->\n# x v2\n")
	in2 := &Installer{Client: &Client{BaseURL: fix2.URL}, SkillsRoot: root}
	rep, err := in2.Install(context.Background(), InstallOptions{Name: "marunage-source-x"})
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if rep.OldVersion != "0.1.0" {
		t.Errorf("OldVersion = %q; want 0.1.0", rep.OldVersion)
	}
	if rep.NewVersion != "0.2.0" {
		t.Errorf("NewVersion = %q; want 0.2.0", rep.NewVersion)
	}
}

// TestSearch_FiltersByQuery pins the small helper Search() to keep
// the CLI and Web UI on the same matching rules.
func TestSearch_FiltersByQuery(t *testing.T) {
	idx := Index{
		SchemaVersion: 1,
		Skills: []IndexEntry{
			{Name: "marunage-source-jira", Description: "Jira issue source"},
			{Name: "marunage-source-linear", Description: "Linear source"},
			{Name: "marunage-source-github", Description: "GitHub issue source"},
		},
	}
	got := Search(idx, "linear")
	if len(got) != 1 || got[0].Name != "marunage-source-linear" {
		t.Errorf("Search(linear) = %+v; want one linear entry", got)
	}
	got = Search(idx, "ISSUE")
	if len(got) != 2 {
		t.Errorf("Search(ISSUE) = %+v; want jira and github", got)
	}
	if len(Search(idx, "")) != 3 {
		t.Errorf("Search(\"\") should return all entries")
	}
}

// TestFindUpdates_ReportsOutdated pins the differential `update`
// helper used by both the CLI and the Web UI.
func TestFindUpdates_ReportsOutdated(t *testing.T) {
	idx := Index{
		SchemaVersion: 1,
		Skills: []IndexEntry{
			{Name: "marunage-source-jira", Latest: "0.2.0"},
			{Name: "marunage-source-linear", Latest: "0.1.0"},
		},
	}
	state := State{Installed: []InstalledSkill{
		{Name: "marunage-source-jira", Version: "0.1.0"},
		{Name: "marunage-source-linear", Version: "0.1.0"},
	}}
	got := FindUpdates(state, idx)
	if len(got) != 1 || got[0].Name != "marunage-source-jira" {
		t.Errorf("FindUpdates = %+v; want one jira entry", got)
	}
}
