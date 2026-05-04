package connector_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haruotsu/marunage/internal/connector"
	"github.com/haruotsu/marunage/internal/source"
)

const connectorTOML = `
[connector]
name = "my-webhook"
type = "discover"
adapter_version = "v1"

[endpoint]
discover = "http://localhost:8080/discover"

[auth]
type = "none"
`

func TestRegisterFromDir_RegistersConnectors(t *testing.T) {
	dir := t.TempDir()
	connDir := filepath.Join(dir, "my-webhook")
	if err := os.MkdirAll(connDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(connDir, "connector.toml"), []byte(connectorTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	r := source.NewRegistry()
	if err := connector.RegisterFromDir(r, dir); err != nil {
		t.Fatalf("RegisterFromDir: %v", err)
	}

	p, err := r.Get("my-webhook")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Name() != "my-webhook" {
		t.Errorf("Name: got %q, want %q", p.Name(), "my-webhook")
	}
}

func TestRegisterFromDir_SkipsNonConnectorDirs(t *testing.T) {
	dir := t.TempDir()
	// directory without connector.toml
	if err := os.MkdirAll(filepath.Join(dir, "not-a-connector"), 0o755); err != nil {
		t.Fatal(err)
	}
	// non-directory file
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := source.NewRegistry()
	if err := connector.RegisterFromDir(r, dir); err != nil {
		t.Fatalf("RegisterFromDir: %v", err)
	}
	if names := r.Names(); len(names) != 0 {
		t.Errorf("expected no registrations, got %v", names)
	}
}

func TestRegisterFromDir_NonExistentDir(t *testing.T) {
	r := source.NewRegistry()
	// non-existent directory should not error (returns empty)
	if err := connector.RegisterFromDir(r, "/nonexistent/path"); err != nil {
		t.Fatalf("expected no error for non-existent dir, got: %v", err)
	}
}

func TestRegisterFromDir_MultipleConnectors(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"webhook-a", "webhook-b"} {
		connDir := filepath.Join(dir, name)
		if err := os.MkdirAll(connDir, 0o755); err != nil {
			t.Fatal(err)
		}
		toml := "[connector]\nname = \"" + name + "\"\ntype = \"discover\"\n\n[endpoint]\ndiscover = \"http://localhost/discover\"\n\n[auth]\ntype = \"none\"\n"
		if err := os.WriteFile(filepath.Join(connDir, "connector.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := source.NewRegistry()
	if err := connector.RegisterFromDir(r, dir); err != nil {
		t.Fatalf("RegisterFromDir: %v", err)
	}
	names := r.Names()
	if len(names) != 2 {
		t.Errorf("expected 2 registrations, got %v", names)
	}
}
