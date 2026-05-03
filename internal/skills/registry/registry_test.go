package registry

import (
	"errors"
	"testing"
)

// TestParseIndex_HappyPath pins the contract: a well-formed index JSON
// decodes into the public Index shape with every entry preserved.
func TestParseIndex_HappyPath(t *testing.T) {
	body := []byte(`{
		"schema_version": 1,
		"skills": [
			{"name": "marunage-source-jira", "latest": "0.2.0", "description": "Jira issue source"},
			{"name": "marunage-source-linear", "latest": "0.1.0"}
		]
	}`)

	got, err := ParseIndex(body)
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if got.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d; want 1", got.SchemaVersion)
	}
	if len(got.Skills) != 2 {
		t.Fatalf("len(Skills) = %d; want 2", len(got.Skills))
	}
	if got.Skills[0].Name != "marunage-source-jira" {
		t.Errorf("Skills[0].Name = %q; want marunage-source-jira", got.Skills[0].Name)
	}
	if got.Skills[0].Latest != "0.2.0" {
		t.Errorf("Skills[0].Latest = %q; want 0.2.0", got.Skills[0].Latest)
	}
}

// TestParseIndex_RejectsUnknownSchema pins the forwards-compat guard:
// a future wire bump should not silently parse with the v1 shape.
func TestParseIndex_RejectsUnknownSchema(t *testing.T) {
	body := []byte(`{"schema_version": 99, "skills": []}`)
	_, err := ParseIndex(body)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsupportedSchema)", err)
	}
}

// TestParseManifest_HappyPath pins the per-skill manifest shape and
// the newest-first convention the installer reads from.
func TestParseManifest_HappyPath(t *testing.T) {
	body := []byte(`{
		"schema_version": 1,
		"name": "marunage-source-jira",
		"description": "Jira issue source",
		"versions": [
			{"version": "0.2.0", "tarball_url": "https://example/0.2.0.tgz", "sha256": "deadbeef", "size_bytes": 1024},
			{"version": "0.1.0", "tarball_url": "https://example/0.1.0.tgz", "sha256": "cafefeed"}
		]
	}`)

	m, err := ParseManifest(body)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Name != "marunage-source-jira" {
		t.Errorf("Name = %q", m.Name)
	}
	if len(m.Versions) != 2 {
		t.Fatalf("len(Versions) = %d", len(m.Versions))
	}
	v, ok := m.Find("")
	if !ok || v.Version != "0.2.0" {
		t.Errorf("Find(\"\") = %v ok=%v; want 0.2.0 / true", v, ok)
	}
	v, ok = m.Find("0.1.0")
	if !ok || v.SHA256 != "cafefeed" {
		t.Errorf("Find(0.1.0) = %v ok=%v; want sha256=cafefeed / true", v, ok)
	}
	if _, ok := m.Find("9.9.9"); ok {
		t.Errorf("Find(9.9.9) = ok=true; want false")
	}
}

// TestParseManifest_RejectsEmptyVersions pins the structural-integrity
// branch: a manifest with no versions is unusable to the installer.
func TestParseManifest_RejectsEmptyVersions(t *testing.T) {
	body := []byte(`{"schema_version": 1, "name": "x", "versions": []}`)
	_, err := ParseManifest(body)
	if !errors.Is(err, ErrManifestMalformed) {
		t.Errorf("err = %v; want errors.Is(_, ErrManifestMalformed)", err)
	}
}

// TestParseManifest_RejectsEmptyName pins the second structural
// requirement: a manifest with an empty name field is unusable.
func TestParseManifest_RejectsEmptyName(t *testing.T) {
	body := []byte(`{"schema_version": 1, "name": "", "versions": [
		{"version": "0.1.0", "tarball_url": "https://x/y.tgz", "sha256": "abc"}
	]}`)
	_, err := ParseManifest(body)
	if !errors.Is(err, ErrManifestMalformed) {
		t.Errorf("err = %v; want errors.Is(_, ErrManifestMalformed)", err)
	}
}

// TestParseManifest_RejectsMissingFields pins per-version validation:
// an entry lacking version / tarball_url / sha256 is unusable.
func TestParseManifest_RejectsMissingFields(t *testing.T) {
	body := []byte(`{"schema_version": 1, "name": "x", "versions": [
		{"version": "0.1.0", "tarball_url": "https://x/y.tgz", "sha256": ""}
	]}`)
	_, err := ParseManifest(body)
	if !errors.Is(err, ErrManifestMalformed) {
		t.Errorf("err = %v; want errors.Is(_, ErrManifestMalformed)", err)
	}
}

// TestParseManifest_RejectsUnknownSchema mirrors the index guard for
// per-skill manifests so a future wire bump never partially-parses.
func TestParseManifest_RejectsUnknownSchema(t *testing.T) {
	body := []byte(`{"schema_version": 7, "name": "x", "versions": [
		{"version": "0.1.0", "tarball_url": "https://x/y.tgz", "sha256": "abc"}
	]}`)
	_, err := ParseManifest(body)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsupportedSchema)", err)
	}
}
