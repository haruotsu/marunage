package source

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifest is a tiny helper that drops a TOML body in t.TempDir() and
// returns its path. Inlining this in every table case would bury the
// assertion under boilerplate.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	return path
}

func TestLoadManifestParsesValidPluginToml(t *testing.T) {
	t.Parallel()

	path := writeManifest(t, `
[plugin]
name = "markdown"
version = "0.1.0"
description = "Markdown TODO file source"
sync_mode = "bidirectional"
capabilities = ["list", "setup", "auth-status", "since", "add", "complete", "delete"]
`)

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "markdown" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Version != "0.1.0" {
		t.Errorf("Version = %q", m.Version)
	}
	if m.SyncMode != SyncModeBidirectional {
		t.Errorf("SyncMode = %q", m.SyncMode)
	}
	if len(m.Capabilities) != 7 {
		t.Errorf("Capabilities = %v", m.Capabilities)
	}
	if !m.HasCapability(CapAdd) {
		t.Errorf("expected `add` capability to be present, got %v", m.Capabilities)
	}
}

func TestLoadManifestRejectsMissingName(t *testing.T) {
	t.Parallel()

	path := writeManifest(t, `
[plugin]
version = "0.1.0"
description = "no name"
sync_mode = "read-only"
capabilities = ["list", "setup", "auth-status"]
`)

	_, err := LoadManifest(path)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention missing field name: %v", err)
	}
}

func TestLoadManifestRejectsMissingVersion(t *testing.T) {
	t.Parallel()

	path := writeManifest(t, `
[plugin]
name = "markdown"
description = "no version"
sync_mode = "read-only"
capabilities = ["list", "setup", "auth-status"]
`)

	_, err := LoadManifest(path)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error should mention version: %v", err)
	}
}

func TestLoadManifestRejectsUnknownCapability(t *testing.T) {
	t.Parallel()

	path := writeManifest(t, `
[plugin]
name = "markdown"
version = "0.1.0"
description = "bad capability"
sync_mode = "read-only"
capabilities = ["list", "setup", "auth-status", "telepathy"]
`)

	_, err := LoadManifest(path)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
	if !strings.Contains(err.Error(), "telepathy") {
		t.Errorf("error should name the offending capability: %v", err)
	}
}

func TestLoadManifestRejectsMissingMandatoryCapability(t *testing.T) {
	t.Parallel()

	// `list` is mandatory per requirement.md; declaring a manifest without
	// it lets a manifest-as-doc drift away from the actual contract. We
	// catch that drift at load time rather than at first invocation.
	path := writeManifest(t, `
[plugin]
name = "markdown"
version = "0.1.0"
description = "no list"
sync_mode = "read-only"
capabilities = ["setup", "auth-status"]
`)

	_, err := LoadManifest(path)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
}

func TestLoadManifestRejectsUnknownSyncMode(t *testing.T) {
	t.Parallel()

	path := writeManifest(t, `
[plugin]
name = "x"
version = "1"
description = "bad mode"
sync_mode = "telepathic"
capabilities = ["list", "setup", "auth-status"]
`)

	_, err := LoadManifest(path)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
}

func TestLoadManifestMissingFile(t *testing.T) {
	t.Parallel()

	_, err := LoadManifest(filepath.Join(t.TempDir(), "no-such.toml"))
	if err == nil {
		t.Fatalf("want error for missing file, got nil")
	}
}
