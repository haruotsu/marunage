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
	if len(cfg.Permissions.Allow) == 0 {
		t.Error("default Allow list must not be empty")
	}
	if len(cfg.Permissions.Deny) == 0 {
		t.Error("default Deny list must not be empty")
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
