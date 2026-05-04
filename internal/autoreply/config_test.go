package autoreply_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haruotsu/marunage/internal/autoreply"
)

func TestLoad_FileNotExist_ReturnsDefaults(t *testing.T) {
	cfg, err := autoreply.Load(filepath.Join(t.TempDir(), "autoreply.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, want := range []string{"schedule_adjustment", "information_sharing", "known_questions"} {
		if !sliceContains(cfg.Permissions.Allow, want) {
			t.Errorf("default Allow missing %q; got %v", want, cfg.Permissions.Allow)
		}
	}
	for _, want := range []string{"personal_information", "contracts", "financial_decisions", "personnel_matters"} {
		if !sliceContains(cfg.Permissions.Deny, want) {
			t.Errorf("default Deny missing %q; got %v", want, cfg.Permissions.Deny)
		}
	}
}

func TestLoad_ValidFile_ParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoreply.toml")
	content := []byte(`
[permissions]
allow = ["schedule_adjustment", "information_sharing"]
deny  = ["personal_information", "financial_decisions"]

[draft_mode]
enabled = true
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := autoreply.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !sliceContains(cfg.Permissions.Allow, "schedule_adjustment") {
		t.Errorf("Allow missing schedule_adjustment; got %v", cfg.Permissions.Allow)
	}
	if !sliceContains(cfg.Permissions.Deny, "personal_information") {
		t.Errorf("Deny missing personal_information; got %v", cfg.Permissions.Deny)
	}
	if !cfg.DraftMode.Enabled {
		t.Error("DraftMode.Enabled should be true")
	}
}

func TestLoad_InvalidTOML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoreply.toml")
	if err := os.WriteFile(path, []byte("not valid toml :::"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := autoreply.Load(path)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

// TestLoad_UserDenyListReplacesDefault pins the go-toml v2 slice-replace
// semantics: when the user writes deny=[...], the default deny list is fully
// replaced, not merged. The hardcoded NG safety is enforced by Boundary, not
// by Config.Permissions.Deny.
func TestLoad_UserDenyListReplacesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoreply.toml")
	content := []byte(`
[permissions]
allow = ["schedule_adjustment"]
deny  = ["personal_information", "financial_decisions"]
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := autoreply.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// User wrote only 2 deny entries; default entries contracts/personnel_matters
	// are replaced (not merged). Boundary still blocks them via hardcoded list.
	if sliceContains(cfg.Permissions.Deny, "contracts") {
		t.Error("Deny should not contain 'contracts' after user override (go-toml replaces slices)")
	}
	if !sliceContains(cfg.Permissions.Deny, "personal_information") {
		t.Error("Deny should contain user-specified 'personal_information'")
	}
}

func TestLoad_EmptyAllow_ParsesWithoutError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoreply.toml")
	content := []byte(`
[permissions]
allow = []
deny  = ["personal_information"]
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := autoreply.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Permissions.Allow) != 0 {
		t.Errorf("Allow should be empty; got %v", cfg.Permissions.Allow)
	}
}

func sliceContains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
