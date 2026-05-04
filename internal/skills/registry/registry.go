// Package registry implements the HTTPS-based shared skill registry
// that PR-203 layers on top of the //go:embed bundle owned by PR-34.
//
// The registry protocol is intentionally narrow: a publisher serves a
// JSON `index.json` enumerating skill names + their latest version, a
// per-skill `manifest.json` listing every published version with its
// tarball URL and a sha256 digest, and one `<version>.tar.gz` per
// release. Consumers (the CLI and the Web UI) verify the tarball
// against the manifest digest before extracting it under
// `~/.claude/skills/<name>/`.
//
// The package keeps zero state: callers own the destination directory
// and the on-disk record of what they have installed (see state.go).
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SchemaVersion is the wire-format major number every Index and
// Manifest must declare. Bumping it forces consumers to opt into
// breaking changes rather than silently mis-parsing a future shape.
const SchemaVersion = 1

// IndexFileName is the well-known relative URL of the registry
// catalog: `<base>/index.json`.
const IndexFileName = "index.json"

// ManifestFileName is the well-known relative URL of a per-skill
// manifest: `<base>/skills/<name>/manifest.json`.
const ManifestFileName = "manifest.json"

// IndexEntry is one row of the registry catalog. The Latest pointer
// names the version `marunage skills install <name>` resolves when
// the user does not pin one.
type IndexEntry struct {
	Name        string `json:"name"`
	Latest      string `json:"latest"`
	Description string `json:"description,omitempty"`
}

// Index is the parsed `index.json` payload. The list is the public
// catalog; SchemaVersion guards the wire format.
type Index struct {
	SchemaVersion int          `json:"schema_version"`
	Skills        []IndexEntry `json:"skills"`
}

// Version is one published release of a single skill. SHA256 is the
// hex-encoded digest of the tarball body the consumer fetched.
type Version struct {
	Version     string `json:"version"`
	TarballURL  string `json:"tarball_url"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// Manifest is the parsed per-skill `manifest.json`. Versions is sorted
// newest-first by the publisher; consumers read [0] when the user did
// not pin a version.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Versions      []Version `json:"versions"`
}

// ErrUnsupportedSchema is returned when SchemaVersion does not match
// the SchemaVersion constant. The typed sentinel lets the CLI render
// an actionable "upgrade marunage" message rather than a generic
// JSON parse error.
var ErrUnsupportedSchema = errors.New("registry: unsupported schema version")

// ErrManifestMalformed is returned when a Manifest parses but is
// structurally unusable (no versions, missing required fields).
var ErrManifestMalformed = errors.New("registry: manifest malformed")

// ParseIndex decodes raw JSON into an Index and validates the schema
// version. Callers should not parse the JSON themselves: the typed
// errors here are part of the package's public contract.
func ParseIndex(body []byte) (Index, error) {
	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return Index{}, fmt.Errorf("registry: parse index: %w", err)
	}
	if idx.SchemaVersion != SchemaVersion {
		return Index{}, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, idx.SchemaVersion, SchemaVersion)
	}
	return idx, nil
}

// ParseManifest decodes raw JSON into a Manifest, validates the
// schema version, and surfaces a typed error when the manifest is
// missing the bits the installer requires.
func ParseManifest(body []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, fmt.Errorf("registry: parse manifest: %w", err)
	}
	if m.SchemaVersion != SchemaVersion {
		return Manifest{}, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, m.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(m.Name) == "" {
		return Manifest{}, fmt.Errorf("%w: name is empty", ErrManifestMalformed)
	}
	if len(m.Versions) == 0 {
		return Manifest{}, fmt.Errorf("%w: no versions", ErrManifestMalformed)
	}
	for i, v := range m.Versions {
		if strings.TrimSpace(v.Version) == "" {
			return Manifest{}, fmt.Errorf("%w: versions[%d].version is empty", ErrManifestMalformed, i)
		}
		if strings.TrimSpace(v.TarballURL) == "" {
			return Manifest{}, fmt.Errorf("%w: versions[%d].tarball_url is empty", ErrManifestMalformed, i)
		}
		if strings.TrimSpace(v.SHA256) == "" {
			return Manifest{}, fmt.Errorf("%w: versions[%d].sha256 is empty", ErrManifestMalformed, i)
		}
	}
	return m, nil
}

// Find returns the Version matching tag in m. An empty tag resolves
// to the first (latest) entry. The boolean lets callers distinguish
// "not present" from "present but zero-valued".
func (m Manifest) Find(tag string) (Version, bool) {
	if len(m.Versions) == 0 {
		return Version{}, false
	}
	if tag == "" {
		return m.Versions[0], true
	}
	for _, v := range m.Versions {
		if v.Version == tag {
			return v, true
		}
	}
	return Version{}, false
}
