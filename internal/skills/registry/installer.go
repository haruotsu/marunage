package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrEmbeddedConflict is returned by Installer.Install when the
// requested skill name overlaps the //go:embed-shipped bundle and
// the caller did not pass AllowEmbeddedOverride. Mirrors the
// "PR-34 (setup --skills) の embedded 機構と衝突しない" requirement.
var ErrEmbeddedConflict = errors.New("registry: name overlaps an embedded skill")

// Clock is the time.Now seam Installer uses to stamp UpdatedAt. The
// concrete implementation is a func to make it ergonomic for tests
// to inject a fixed instant.
type Clock func() time.Time

// Installer is the high-level driver behind `marunage skills install`
// and `marunage skills update`. It wires Client + ExtractTarball +
// State together so the CLI surface is just argument parsing.
type Installer struct {
	Client     *Client
	SkillsRoot string
	Clock      Clock
}

// InstallOptions tunes a single Install call.
type InstallOptions struct {
	// Name is the skill to install. Empty is rejected.
	Name string
	// Version pins a published version. Empty resolves to the
	// manifest's first entry (newest by convention).
	Version string
	// AllowEmbeddedOverride lets the caller intentionally clobber
	// `marunage-triage|execute|reflect`. The default refuses.
	AllowEmbeddedOverride bool
}

// InstallReport describes the outcome of one Install call.
type InstallReport struct {
	Name       string
	NewVersion string
	OldVersion string
	Source     string
	SHA256     string
	Path       string
}

// Install fetches the manifest, picks the requested version,
// downloads and verifies the tarball, extracts it under
// `<SkillsRoot>/<Name>/`, and updates the on-disk state file.
//
// The function is safe to retry: failures abort before any on-disk
// mutation lands, and the state file is rewritten only after the
// extract succeeds.
func (in *Installer) Install(ctx context.Context, opts InstallOptions) (InstallReport, error) {
	if in == nil {
		return InstallReport{}, fmt.Errorf("registry: Installer is nil")
	}
	if in.Client == nil {
		return InstallReport{}, fmt.Errorf("registry: Installer.Client is nil")
	}
	if in.SkillsRoot == "" {
		return InstallReport{}, fmt.Errorf("registry: Installer.SkillsRoot is empty")
	}
	if opts.Name == "" {
		return InstallReport{}, fmt.Errorf("registry: install name is empty")
	}
	if IsEmbeddedSkill(opts.Name) && !opts.AllowEmbeddedOverride {
		return InstallReport{}, fmt.Errorf("%w: %s (pass --allow-embedded-override)", ErrEmbeddedConflict, opts.Name)
	}

	manifest, err := in.Client.FetchManifest(ctx, opts.Name)
	if err != nil {
		return InstallReport{}, err
	}
	v, ok := manifest.Find(opts.Version)
	if !ok {
		return InstallReport{}, fmt.Errorf("registry: version %q not found in manifest for %s", opts.Version, opts.Name)
	}

	body, err := in.Client.FetchTarball(ctx, v)
	if err != nil {
		return InstallReport{}, err
	}

	if err := os.MkdirAll(in.SkillsRoot, 0o700); err != nil {
		return InstallReport{}, fmt.Errorf("registry: mkdir skills root: %w", err)
	}
	dest := filepath.Join(in.SkillsRoot, opts.Name)
	if err := ExtractTarball(body, ExtractOptions{Dest: dest, SkillName: opts.Name}); err != nil {
		return InstallReport{}, err
	}

	state, err := LoadState(in.SkillsRoot)
	if err != nil {
		return InstallReport{}, err
	}
	prev, _ := state.Find(opts.Name)
	clock := in.Clock
	if clock == nil {
		clock = time.Now
	}
	state = state.Upsert(InstalledSkill{
		Name:      opts.Name,
		Version:   v.Version,
		Source:    in.Client.BaseURL,
		SHA256:    v.SHA256,
		UpdatedAt: clock().UTC().Format(time.RFC3339),
	})
	if err := SaveState(in.SkillsRoot, state); err != nil {
		return InstallReport{}, err
	}

	return InstallReport{
		Name:       opts.Name,
		NewVersion: v.Version,
		OldVersion: prev.Version,
		Source:     in.Client.BaseURL,
		SHA256:     v.SHA256,
		Path:       filepath.Join(dest, "SKILL.md"),
	}, nil
}

// Search returns the catalog entries whose name or description
// contains query (case-insensitive). An empty query returns every
// entry. Centralising the filter here keeps both the CLI and the
// Web UI on the same matching rules.
func Search(idx Index, query string) []IndexEntry {
	if query == "" {
		return append([]IndexEntry(nil), idx.Skills...)
	}
	q := toLower(query)
	var hits []IndexEntry
	for _, e := range idx.Skills {
		if substr(toLower(e.Name), q) || substr(toLower(e.Description), q) {
			hits = append(hits, e)
		}
	}
	return hits
}

// FindUpdates compares state against idx and returns the entries
// whose Latest is different from the recorded Version. Skills
// recorded in state but no longer in the index are NOT reported —
// they may have been republished elsewhere and the operator should
// see "missing in registry" only on explicit `marunage skills
// search`.
func FindUpdates(state State, idx Index) []IndexEntry {
	byName := map[string]IndexEntry{}
	for _, e := range idx.Skills {
		byName[e.Name] = e
	}
	var outdated []IndexEntry
	for _, inst := range state.Installed {
		entry, ok := byName[inst.Name]
		if !ok {
			continue
		}
		if entry.Latest != "" && entry.Latest != inst.Version {
			outdated = append(outdated, entry)
		}
	}
	return outdated
}

func toLower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func substr(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
