package registry

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLoadState_MissingFileIsZero pins that a brand-new machine —
// where `<root>/.marunage-registry.json` does not exist yet — still
// yields a usable zero State.
func TestLoadState_MissingFileIsZero(t *testing.T) {
	root := t.TempDir()
	s, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(s.Installed) != 0 {
		t.Errorf("Installed = %v; want empty", s.Installed)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d; want %d", s.SchemaVersion, SchemaVersion)
	}
}

// TestSaveAndLoadState_RoundTrip pins the basic persistence
// contract. Uses Upsert to drive the path callers actually take.
func TestSaveAndLoadState_RoundTrip(t *testing.T) {
	root := t.TempDir()

	s := State{}
	s = s.Upsert(InstalledSkill{Name: "marunage-source-jira", Version: "0.2.0", Source: "https://x/", SHA256: "abc"})
	s = s.Upsert(InstalledSkill{Name: "marunage-source-linear", Version: "0.1.0", Source: "https://x/", SHA256: "def"})

	if err := SaveState(root, s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(got.Installed) != 2 {
		t.Fatalf("len(Installed) = %d; want 2", len(got.Installed))
	}
	jira, ok := got.Find("marunage-source-jira")
	if !ok || jira.Version != "0.2.0" {
		t.Errorf("Find jira = %+v ok=%v", jira, ok)
	}
}

// TestUpsert_ReplacesExistingEntry pins the upgrade path: re-
// installing a skill at a new version replaces (not duplicates) the
// state row.
func TestUpsert_ReplacesExistingEntry(t *testing.T) {
	s := State{}
	s = s.Upsert(InstalledSkill{Name: "marunage-x", Version: "0.1.0"})
	s = s.Upsert(InstalledSkill{Name: "marunage-x", Version: "0.2.0"})
	if len(s.Installed) != 1 {
		t.Fatalf("len(Installed) = %d; want 1 after upsert", len(s.Installed))
	}
	if s.Installed[0].Version != "0.2.0" {
		t.Errorf("Version = %q; want 0.2.0", s.Installed[0].Version)
	}
}

// TestSaveState_PersistsAt0600 pins that the per-user metadata file
// is not world-readable on POSIX hosts.
func TestSaveState_PersistsAt0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permissions not enforced on windows")
	}
	root := t.TempDir()
	if err := SaveState(root, State{Installed: []InstalledSkill{{Name: "x", Version: "0.1.0"}}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, StateFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o; want 0600", perm)
	}
}

// TestIsEmbeddedSkill pins the documented skip-list: registry
// `install` of one of these names must take an explicit override
// path rather than silently overwriting the bundled copy.
func TestIsEmbeddedSkill(t *testing.T) {
	for _, name := range []string{"marunage-triage", "marunage-execute", "marunage-reflect"} {
		if !IsEmbeddedSkill(name) {
			t.Errorf("IsEmbeddedSkill(%q) = false; want true", name)
		}
	}
	for _, name := range []string{"marunage-source-jira", "", "marunage-other"} {
		if IsEmbeddedSkill(name) {
			t.Errorf("IsEmbeddedSkill(%q) = true; want false", name)
		}
	}
}
