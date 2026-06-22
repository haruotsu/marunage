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
	"os"
	"sort"
	"strings"

	"github.com/haruotsu/marunage/internal/fsutil"
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
	eol := detectEOL(body)
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
	// If a marker is already there, drop it from the title segment and
	// keep the supplied marker. injectMarker is only ever invoked for
	// lines listLocked has already classified as marker-less, but the
	// guard makes the function idempotent for safety.
	title := rest
	if loc := markerComment.FindStringSubmatchIndex(rest); loc != nil {
		title = rest[:loc[0]]
	}
	tl := taskLine{
		Indent: indent,
		Title:  trimTrailingSpace(title),
		Done:   done,
		Marker: mk,
	}
	lines[idx] = renderTaskLine(tl)
	out := joinLines(lines, eol)
	if hadTrailingNewline && (len(out) == 0 || out[len(out)-1] != '\n') {
		out = append(out, eol...)
	}
	return out
}

// splitLines splits body on '\n', dropping a single trailing empty
// element produced by a terminating newline. Each returned line has any
// trailing '\r' stripped so callers can rebuild lines without carrying a
// stray CR byte through render paths; the original line ending is
// recovered separately via detectEOL so CRLF files round-trip cleanly.
//
// We avoid bytes.Split because it would leave a "" at the end after a
// trailing newline and the caller would have to special-case it on
// rejoin.
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
			out = append(out, stripTrailingCR(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, stripTrailingCR(s[start:]))
	return out
}

// stripTrailingCR removes a single trailing '\r' so a line scanned out
// of a CRLF body does not carry the CR forward into render / regex
// paths. We only strip one CR — a line that legitimately ends with two
// CRs (extremely unlikely in a Markdown TODO file) keeps the leading
// one, which is the conservative choice.
func stripTrailingCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}

// detectEOL returns the dominant line-ending byte sequence of body.
// CRLF wins if the body contains any "\r\n", otherwise LF — the standard
// "first wins" heuristic used by editors when a file is mixed. Empty or
// newline-free bodies default to LF, which is the OS-neutral default
// for files we author from scratch (Add into a missing file, Setup).
func detectEOL(body []byte) string {
	for i := 0; i+1 < len(body); i++ {
		if body[i] == '\r' && body[i+1] == '\n' {
			return "\r\n"
		}
	}
	return "\n"
}

// joinLines is the inverse of splitLines without the trailing-newline
// handling — caller restores it. The eol argument lets callers preserve
// CRLF when the source body was Windows-encoded; LF-only bodies should
// pass "\n".
func joinLines(lines []string, eol string) []byte {
	if eol == "" {
		eol = "\n"
	}
	var n int
	for _, l := range lines {
		n += len(l) + len(eol)
	}
	out := make([]byte, 0, n)
	for i, l := range lines {
		if i > 0 {
			out = append(out, eol...)
		}
		out = append(out, l...)
	}
	return out
}

// atomicWriteFile writes data to path via a sibling tmp file then renames it
// into place, chmod-ing the tmp before rename so a racing reader never observes
// a wider mode. It delegates to the shared fsutil.AtomicWrite implementation;
// the thin wrapper keeps the package-local call sites and the requirement
// rationale (Reversibility for hand-written notes) documented here.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return fsutil.AtomicWrite(path, data, perm)
}
