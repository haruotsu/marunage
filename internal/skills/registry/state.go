package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/haruotsu/marunage/internal/skills"
)

// StateFileName is the on-disk record of every skill `marunage skills
// install` has written under the skills root. Pinning the name here
// keeps the embedded installer (PR-34) and the registry installer
// (PR-203) honest about a single source of truth.
const StateFileName = ".marunage-registry.json"

// InstalledSkill is one entry of the on-disk state file. Source is
// the registry BaseURL the skill was fetched from so `update` can
// re-resolve against the same publisher; SHA256 lets `marunage skills
// list` flag tampered files.
type InstalledSkill struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Source    string `json:"source,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// State is the parsed `<skillsRoot>/.marunage-registry.json`. It is
// not the source of truth for "what is on disk" — the SKILL.md file
// is — but it is the source of truth for "where did this come from".
type State struct {
	SchemaVersion int              `json:"schema_version"`
	Installed     []InstalledSkill `json:"installed"`
}

// LoadState reads the state file under skillsRoot. A missing file is
// not an error: the zero State means "no registry-installed skills
// yet", which is exactly the truth on a fresh machine.
func LoadState(skillsRoot string) (State, error) {
	p := filepath.Join(skillsRoot, StateFileName)
	body, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return State{SchemaVersion: SchemaVersion}, nil
		}
		return State{}, fmt.Errorf("registry: read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(body, &s); err != nil {
		return State{}, fmt.Errorf("registry: parse state: %w", err)
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	return s, nil
}

// SaveState writes State as pretty-printed JSON via the tmp+rename
// pattern so a crash mid-write never leaves a half-written state
// file. Also chmods to 0600 since the file is per-user metadata.
func SaveState(skillsRoot string, s State) error {
	if err := os.MkdirAll(skillsRoot, 0o700); err != nil {
		return fmt.Errorf("registry: mkdir %s: %w", skillsRoot, err)
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	sort.Slice(s.Installed, func(i, j int) bool {
		return s.Installed[i].Name < s.Installed[j].Name
	})

	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: marshal state: %w", err)
	}

	p := filepath.Join(skillsRoot, StateFileName)
	tmp, err := os.CreateTemp(skillsRoot, ".marunage-registry-*.tmp")
	if err != nil {
		return fmt.Errorf("registry: tmp state: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("registry: write state tmp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("registry: chmod state tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("registry: close state tmp: %w", err)
	}
	if err := os.Rename(tmpPath, p); err != nil {
		cleanup()
		return fmt.Errorf("registry: rename state: %w", err)
	}
	return nil
}

// Find returns the recorded entry for name and a bool indicating
// presence. The bool lets callers cleanly differentiate "never
// installed" from "installed but at version zero".
func (s State) Find(name string) (InstalledSkill, bool) {
	for _, e := range s.Installed {
		if e.Name == name {
			return e, true
		}
	}
	return InstalledSkill{}, false
}

// Upsert inserts or replaces the entry whose name matches e.Name and
// returns the updated state. The original is not mutated, so callers
// can keep a snapshot for rollback.
func (s State) Upsert(e InstalledSkill) State {
	out := State{SchemaVersion: s.SchemaVersion, Installed: make([]InstalledSkill, 0, len(s.Installed)+1)}
	replaced := false
	for _, existing := range s.Installed {
		if existing.Name == e.Name {
			out.Installed = append(out.Installed, e)
			replaced = true
			continue
		}
		out.Installed = append(out.Installed, existing)
	}
	if !replaced {
		out.Installed = append(out.Installed, e)
	}
	return out
}

// IsEmbeddedSkill reports whether name is one of the //go:embed
// shipped skills the registry installer must NOT clobber by default.
// Authority lives in skills.EmbeddedSkillNames so adding or renaming
// an embedded SKILL.md in PR-34's bundle automatically tightens the
// registry's refusal list — no parallel hard-coded list to drift.
func IsEmbeddedSkill(name string) bool {
	target := strings.TrimSpace(name)
	for _, n := range skills.EmbeddedSkillNames() {
		if n == target {
			return true
		}
	}
	return false
}
