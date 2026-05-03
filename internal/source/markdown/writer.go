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

// injectMarker returns a copy of body with the given marker appended to
// the (1-based) lineNumber. Used by List's auto-marker pass: a line
// that arrived without `<!-- marunage:id=... -->` gets one inserted in
// place, leaving every other byte of the file untouched.
//
// The function is line-oriented: it splits on '\n', rewrites the target
// line, and rejoins. This preserves the surrounding prose verbatim,
// including the file's trailing-newline-or-not. Lines that already
// carry a marker are not touched (callers should not invoke this for
// those, but the guard makes injectMarker idempotent for safety).
func injectMarker(body []byte, lineNumber int, mk marker) []byte {
	hadTrailingNewline := len(body) > 0 && body[len(body)-1] == '\n'
	lines := splitLines(body)
	idx := lineNumber - 1
	if idx < 0 || idx >= len(lines) {
		return body
	}
	line := lines[idx]
	// Re-parse the single line so we keep the original indent / done
	// flag without having to thread them through from the caller.
	m := checkboxLine.FindStringSubmatch(line)
	if m == nil {
		return body
	}
	indent := m[1]
	done := m[2] == "x" || m[2] == "X"
	rest := m[3]
	// If a marker is already there, parse and merge rather than blindly
	// appending — defensive against being called twice.
	title := rest
	existing := mk
	if loc := markerComment.FindStringSubmatchIndex(rest); loc != nil {
		title = rest[:loc[0]]
	}
	tl := taskLine{
		Indent: indent,
		Title:  trimTrailingSpace(title),
		Done:   done,
		Marker: existing,
	}
	lines[idx] = renderTaskLine(tl)
	out := joinLines(lines)
	if hadTrailingNewline && (len(out) == 0 || out[len(out)-1] != '\n') {
		out = append(out, '\n')
	}
	return out
}

// splitLines splits body on '\n', dropping a single trailing empty
// element produced by a terminating newline. We avoid bytes.Split
// because it would leave a "" at the end after a trailing newline and
// the caller would have to special-case it on rejoin.
func splitLines(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	s := string(body)
	if s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// joinLines is the inverse of splitLines without the trailing-newline
// handling — caller restores it.
func joinLines(lines []string) []byte {
	var n int
	for _, l := range lines {
		n += len(l) + 1
	}
	out := make([]byte, 0, n)
	for i, l := range lines {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, l...)
	}
	return out
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
