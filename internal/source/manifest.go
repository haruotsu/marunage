package source

import (
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// ErrInvalidManifest is the typed sentinel returned by LoadManifest for any
// validation failure (missing field, unknown capability, bad sync_mode, ...).
// Callers branch on errors.Is rather than parsing strings; the wrapped error
// message names the specific offence so a human reading the log can fix it.
var ErrInvalidManifest = errors.New("source: invalid plugin manifest")

// Capability is the typed-string view of one entry from a manifest's
// `capabilities` array. Defined as a named string so an array of these can
// be compared without converting back to plain string at every callsite.
type Capability string

const (
	CapList       Capability = "list"
	CapSetup      Capability = "setup"
	CapAuthStatus Capability = "auth-status"
	CapSince      Capability = "since"
	CapAdd        Capability = "add"
	CapComplete   Capability = "complete"
	CapDelete     Capability = "delete"
)

// knownCapabilities is the set the validator checks against. Keeping it as a
// map makes the lookup O(1) and lets validation report the offending value
// verbatim rather than lying about what was expected.
var knownCapabilities = map[Capability]struct{}{
	CapList:       {},
	CapSetup:      {},
	CapAuthStatus: {},
	CapSince:      {},
	CapAdd:        {},
	CapComplete:   {},
	CapDelete:     {},
}

// mandatoryCapabilities are the three subcommands every Discovery plugin
// MUST implement per requirement.md lines 102-114. A manifest that omits
// any of them is structurally inconsistent and refused at load time.
var mandatoryCapabilities = []Capability{CapList, CapSetup, CapAuthStatus}

// SyncMode is the typed-string view of plugin.toml's `sync_mode` field.
// Today only "bidirectional" and "read-only" are accepted; future modes
// (e.g. "write-only" for an outbound notifier) would be added here and the
// validator's switch updated.
type SyncMode string

const (
	SyncModeBidirectional SyncMode = "bidirectional"
	SyncModeReadOnly      SyncMode = "read-only"
)

// Manifest is the parsed view of a plugin.toml. The on-disk shape uses a
// nested `[plugin]` table to leave room for future top-level sections
// (`[settings]`, `[telemetry]`, ...). Storing the parsed form flat keeps
// callers from having to walk through `m.Plugin.Name`.
type Manifest struct {
	Name         string
	Version      string
	Description  string
	SyncMode     SyncMode
	Capabilities []Capability
}

// HasCapability reports whether the manifest declares want. Linear scan is
// fine — the capability list never grows beyond a handful of entries.
// (`want` rather than `cap` to avoid shadowing the builtin cap().)
func (m Manifest) HasCapability(want Capability) bool {
	for _, c := range m.Capabilities {
		if c == want {
			return true
		}
	}
	return false
}

// rawManifest is the on-disk TOML shape; the public Manifest is flattened
// for callers. Keeping this type private prevents downstream packages from
// depending on the file layout, which gives us room to evolve plugin.toml
// without breaking compilations.
type rawManifest struct {
	Plugin struct {
		Name         string   `toml:"name"`
		Version      string   `toml:"version"`
		Description  string   `toml:"description"`
		SyncMode     string   `toml:"sync_mode"`
		Capabilities []string `toml:"capabilities"`
	} `toml:"plugin"`
}

// LoadManifest reads path, parses the TOML body, and validates the result.
// Validation is loud: any deviation from the contract returns ErrInvalidManifest
// wrapped with a context message naming the specific offence so a misconfigured
// plugin fails at startup rather than at first call.
func LoadManifest(path string) (*Manifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		// Wrap (not Is-tag) the OS error: a missing-file case is distinct
		// from a malformed-content case, and the OS-side typed errors
		// already carry the right structure for callers that care.
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	return LoadManifestFromBytes(body)
}

// LoadManifestFromBytes is the parsing core shared by LoadManifest and any
// caller holding the manifest in memory (the markdown built-in embeds its
// own plugin.toml via go:embed). Centralising the validation here means
// the on-disk and in-memory paths cannot drift in their accept/reject
// rules and removes the tempfile dance the markdown built-in previously
// needed.
func LoadManifestFromBytes(body []byte) (*Manifest, error) {
	var raw rawManifest
	if err := toml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidManifest, err)
	}

	if raw.Plugin.Name == "" {
		return nil, fmt.Errorf("%w: missing required field `plugin.name`", ErrInvalidManifest)
	}
	if raw.Plugin.Version == "" {
		return nil, fmt.Errorf("%w: missing required field `plugin.version`", ErrInvalidManifest)
	}

	mode := SyncMode(raw.Plugin.SyncMode)
	switch mode {
	case SyncModeBidirectional, SyncModeReadOnly:
		// Accepted.
	case "":
		return nil, fmt.Errorf("%w: missing required field `plugin.sync_mode`", ErrInvalidManifest)
	default:
		return nil, fmt.Errorf("%w: unknown sync_mode %q", ErrInvalidManifest, raw.Plugin.SyncMode)
	}

	caps := make([]Capability, 0, len(raw.Plugin.Capabilities))
	seen := map[Capability]struct{}{}
	for _, s := range raw.Plugin.Capabilities {
		c := Capability(s)
		if _, ok := knownCapabilities[c]; !ok {
			return nil, fmt.Errorf("%w: unknown capability %q", ErrInvalidManifest, s)
		}
		if _, dup := seen[c]; dup {
			return nil, fmt.Errorf("%w: capability %q listed twice", ErrInvalidManifest, s)
		}
		seen[c] = struct{}{}
		caps = append(caps, c)
	}
	for _, must := range mandatoryCapabilities {
		if _, ok := seen[must]; !ok {
			return nil, fmt.Errorf("%w: missing mandatory capability %q", ErrInvalidManifest, must)
		}
	}

	return &Manifest{
		Name:         raw.Plugin.Name,
		Version:      raw.Plugin.Version,
		Description:  raw.Plugin.Description,
		SyncMode:     mode,
		Capabilities: caps,
	}, nil
}
