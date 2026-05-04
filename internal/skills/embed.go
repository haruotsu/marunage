package skills

import (
	"embed"
	"io/fs"
)

//go:embed embedded
var embedded embed.FS

// EmbeddedFS returns the //go:embed-bundled Skills root, rooted at the
// `embedded/` subdirectory so callers see a layout of
// `marunage-triage/SKILL.md`, `marunage-execute/SKILL.md`, ... at the
// top level (matching what FromDir returns).
func EmbeddedFS() fs.FS {
	sub, err := fs.Sub(embedded, "embedded")
	if err != nil {
		// Sub only fails when the path is malformed at compile time.
		// The embed directive guarantees the path exists, so this is a
		// programmer error and panicking is the simplest contract.
		panic("skills: embedded subtree missing: " + err.Error())
	}
	return sub
}

// EmbeddedSkillNames returns the names of the //go:embed-bundled
// skills (`marunage-triage`, `marunage-execute`, `marunage-reflect`)
// in deterministic order. The function is the single source of
// truth for "is this skill name owned by PR-34?" so the registry
// installer can refuse to clobber bundled skills without hard-coding
// a parallel list.
func EmbeddedSkillNames() []string {
	names, err := listSkillDirs(EmbeddedFS())
	if err != nil {
		// listSkillDirs only fails when the embedded FS is unreadable,
		// which the build guarantees against. Treat as programmer
		// error — same rationale as EmbeddedFS panicking.
		panic("skills: cannot enumerate embedded skills: " + err.Error())
	}
	return names
}
