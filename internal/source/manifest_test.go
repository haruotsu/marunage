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

// TestLoadManifestFromBytesParsesValidPluginToml drives the bytes-based loader
// the embedded built-in manifest needs. Going through tempfile + os.ReadFile
// just to feed go-embed bytes through the same parser is a wart we now
// remove; tests pin both APIs to the same validation rules.
func TestLoadManifestFromBytesParsesValidPluginToml(t *testing.T) {
	t.Parallel()

	body := []byte(`
[plugin]
name = "markdown"
version = "0.1.0"
description = "x"
sync_mode = "bidirectional"
capabilities = ["list", "setup", "auth-status", "since", "add", "complete", "delete"]
`)
	m, err := LoadManifestFromBytes(body)
	if err != nil {
		t.Fatalf("LoadManifestFromBytes: %v", err)
	}
	if m.Name != "markdown" || !m.HasCapability(CapAdd) {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

// TestLoadManifestFromBytesValidates ensures bytes input shares the same
// validation pipeline as on-disk manifests; an unknown capability must
// surface ErrInvalidManifest just like LoadManifest.
func TestLoadManifestFromBytesValidates(t *testing.T) {
	t.Parallel()

	body := []byte(`
[plugin]
name = "x"
version = "1"
sync_mode = "bidirectional"
capabilities = ["list", "setup", "auth-status", "telepathy"]
`)
	_, err := LoadManifestFromBytes(body)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
}

// TestLoadManifestRejectsDuplicateCapability covers the manifest.go
// "listed twice" branch that was previously uncovered (review iter1 W8).
func TestLoadManifestRejectsDuplicateCapability(t *testing.T) {
	t.Parallel()

	path := writeManifest(t, `
[plugin]
name = "x"
version = "1"
sync_mode = "bidirectional"
capabilities = ["list", "setup", "auth-status", "list"]
`)
	_, err := LoadManifest(path)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error should mention duplicate: %v", err)
	}
}

// TestLoadManifestRejectsMissingSyncMode covers the empty-sync_mode branch
// distinct from "unknown sync_mode" (review iter1 W8).
func TestLoadManifestRejectsMissingSyncMode(t *testing.T) {
	t.Parallel()

	path := writeManifest(t, `
[plugin]
name = "x"
version = "1"
description = "no mode"
capabilities = ["list", "setup", "auth-status"]
`)
	_, err := LoadManifest(path)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
	if !strings.Contains(err.Error(), "sync_mode") {
		t.Errorf("error should mention sync_mode: %v", err)
	}
}

// TestLoadManifestAdapterVersionDefaultsToV1 verifies that omitting
// adapter_version in plugin.toml is treated as "v1" for backward compatibility.
func TestLoadManifestAdapterVersionDefaultsToV1(t *testing.T) {
	t.Parallel()

	m, err := LoadManifestFromBytes([]byte(`
[plugin]
name = "x"
version = "1"
sync_mode = "read-only"
capabilities = ["list", "setup", "auth-status"]
`))
	if err != nil {
		t.Fatalf("LoadManifestFromBytes: %v", err)
	}
	if m.AdapterVersion != "v1" {
		t.Errorf("AdapterVersion = %q, want %q", m.AdapterVersion, "v1")
	}
}

// TestLoadManifestAdapterVersionV2 verifies that adapter_version = "v2" is
// parsed and surfaced on the Manifest.
func TestLoadManifestAdapterVersionV2(t *testing.T) {
	t.Parallel()

	m, err := LoadManifestFromBytes([]byte(`
[plugin]
name = "x"
version = "1"
sync_mode = "read-only"
adapter_version = "v2"
capabilities = ["list", "setup", "auth-status"]
`))
	if err != nil {
		t.Fatalf("LoadManifestFromBytes: %v", err)
	}
	if m.AdapterVersion != "v2" {
		t.Errorf("AdapterVersion = %q, want %q", m.AdapterVersion, "v2")
	}
}

// TestLoadManifestRejectsUnknownAdapterVersion verifies that an invalid
// adapter_version value returns ErrInvalidManifest.
func TestLoadManifestRejectsUnknownAdapterVersion(t *testing.T) {
	t.Parallel()

	_, err := LoadManifestFromBytes([]byte(`
[plugin]
name = "x"
version = "1"
sync_mode = "read-only"
adapter_version = "v99"
capabilities = ["list", "setup", "auth-status"]
`))
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest, got %v", err)
	}
	if !strings.Contains(err.Error(), "adapter_version") {
		t.Errorf("error should mention adapter_version: %v", err)
	}
}

// TestLoadManifestRejectsUpdateCapabilityOnV1 verifies that declaring "update"
// in a v1 manifest is rejected: the spec requires adapter_version = "v2".
func TestLoadManifestRejectsUpdateCapabilityOnV1(t *testing.T) {
	t.Parallel()

	_, err := LoadManifestFromBytes([]byte(`
[plugin]
name = "x"
version = "1"
sync_mode = "bidirectional"
capabilities = ["list", "setup", "auth-status", "update"]
`))
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("want ErrInvalidManifest for update on v1 manifest, got %v", err)
	}
}

// TestLoadManifestParsesUpdateCapability verifies that "update" is a known
// capability and can be included in v2 manifests.
func TestLoadManifestParsesUpdateCapability(t *testing.T) {
	t.Parallel()

	m, err := LoadManifestFromBytes([]byte(`
[plugin]
name = "x"
version = "1"
sync_mode = "bidirectional"
adapter_version = "v2"
capabilities = ["list", "setup", "auth-status", "update"]
`))
	if err != nil {
		t.Fatalf("LoadManifestFromBytes: %v", err)
	}
	if !m.HasCapability(CapUpdate) {
		t.Errorf("expected CapUpdate to be present, got %v", m.Capabilities)
	}
}
