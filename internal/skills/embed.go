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
