// Package markdown's writer.go owns the side that produces bytes: it
// renders a single checklist line back to its canonical Markdown form
// (used by Add and by the auto-marker injection path that List runs the
// first time it sees a marker-less line) and provides the tmp-file +
// rename "atomic write" the other operations use to mutate files.
//
// Why atomic write: the source files are user-owned hand-written notes.
// A partial write left over from a process kill or power failure would
// corrupt prose surrounding the checklist lines, which is exactly the
// trust failure docs/requirement.md invariant "Reversibility" is meant
// to prevent for this Phase 1 source.
package markdown

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// taskLine is the canonical, render-ready shape of one checklist line.
// It is deliberately separate from parsedTask: parsedTask carries
// LineNumber / SourcePath bookkeeping that makes no sense at render
// time, and taskLine carries an Indent string we want to round-trip
// verbatim.
type taskLine struct {
	Indent string // leading whitespace before "-"
	Title  string
	Done   bool
	Marker marker
}

// renderTaskLine emits the canonical "<indent>- [ ] <title> <!-- marunage:... -->"
// string. The trailing newline is NOT included; callers that build a
// full file body decide where line breaks go (see writeFileBody).
func renderTaskLine(t taskLine) string {
	box := "[ ]"
	if t.Done {
		box = "[x]"
	}
	var b strings.Builder
	b.WriteString(t.Indent)
	b.WriteString("- ")
	b.WriteString(box)
	b.WriteString(" ")
	b.WriteString(t.Title)
	if t.Marker.Present {
		b.WriteString(" <!-- marunage:")
		b.WriteString(renderMarkerPayload(t.Marker))
		b.WriteString(" -->")
	}
	return b.String()
}

// renderMarkerPayload turns a marker back into its space-separated
// key=value form. Keys are emitted in a fixed order (id, external_id,
// source, then Extra alphabetically) so re-parsing then re-rendering
// the same marker is byte-identical — important for the "noop write
// is actually a noop" guarantee that diffs in PR review depend on.
func renderMarkerPayload(m marker) string {
	var parts []string
	if m.ID != "" {
		parts = append(parts, "id="+m.ID)
	}
	if m.ExternalID != "" {
		parts = append(parts, "external_id="+m.ExternalID)
	}
	if m.Source != "" {
		parts = append(parts, "source="+m.Source)
	}
	if len(m.Extra) > 0 {
		keys := make([]string, 0, len(m.Extra))
		for k := range m.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, k+"="+m.Extra[k])
		}
	}
	return strings.Join(parts, " ")
}

// atomicWriteFile writes data to path via a sibling tmp file then
// renames it into place. It mirrors the pattern in
// internal/secrets/file_backend.go fileBackend.Set: chmod the tmp file
// before rename so a racing reader never observes a wider mode.
//
// The tmp lives in the same directory so the final rename is a
// same-filesystem operation (cross-FS rename would fail with EXDEV on
// Linux). Cleanup on every error path keeps a half-written tmp from
// piling up next to the target after a crash.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}
