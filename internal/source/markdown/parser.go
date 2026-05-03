// Package markdown's parser.go owns the line-level scanner that turns a
// Markdown file into a slice of parsedTask values. It is intentionally
// minimal: only GitHub-flavoured checklist syntax `- [ ] ...` / `- [x] ...`
// is recognised, plus the trailing HTML marker comment
// `<!-- marunage:key=value ... -->` that the writer injects to keep
// ExternalIDs stable across re-syncs.
//
// Why a hand-rolled parser rather than a Markdown AST library: the only
// shape we care about is "checklist line + optional trailing marker",
// the file may contain arbitrary surrounding prose we must preserve
// verbatim on round-trip, and we want zero third-party dependencies in
// internal/. The line-level approach lets writer.go rewrite a single
// line in-place without re-rendering the whole document.
package markdown

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
)

// parsedTask is the parser's output row. The public Task type wraps this
// with derived fields (ExternalID resolution, Notes accumulation) added in
// markdown.go; keeping the parser oblivious to those higher-level concerns
// makes parser_test.go easy to read.
type parsedTask struct {
	Title      string
	Done       bool
	LineNumber int    // 1-based
	SourcePath string // verbatim copy of the path passed in
	Marker     marker // zero value when no `<!-- marunage:... -->` was present
}

// marker is the parsed form of a `<!-- marunage:key=value ... -->` trailing
// comment. Only the keys we actively use today are typed; unknown keys
// land in Extra so a future PR can read them without reparsing.
type marker struct {
	Present    bool
	ID         string            // marunage:id=...
	ExternalID string            // marunage:external_id=... (alias retained for symmetry with the requirement doc table; we treat ID as the canonical key)
	Source     string            // marunage:source=...
	Extra      map[string]string // any other key=value pairs
}

// checkboxLine matches the GFM task-list prefix. We deliberately accept
// only `-` (not `*` / `+`) because the writer always emits `-` and we
// would otherwise have to remember which bullet style each line used in
// order to round-trip cleanly. Indentation is captured so writer.go can
// reproduce the same leading whitespace when rewriting a line.
var checkboxLine = regexp.MustCompile(`^(\s*)- \[( |x|X)\] (.*)$`)

// markerComment matches the trailing `<!-- marunage:key=value ... -->`.
// Anchored to end-of-line so a comment in the middle of the title (which
// users do not write, but might paste accidentally) does not get mistaken
// for a marker. The capture group is the raw key=value payload, parsed
// further by parseMarkerPayload.
var markerComment = regexp.MustCompile(`\s*<!--\s*marunage:(.*?)\s*-->\s*$`)

// parse scans src line-by-line and returns the parsedTask slice in source
// order. path is echoed back into parsedTask.SourcePath so callers that
// pass several files through parse can attribute results without keeping
// a side map.
func parse(path string, src []byte) ([]parsedTask, error) {
	var out []parsedTask
	sc := bufio.NewScanner(bytes.NewReader(src))
	// Allow long lines: the default 64 KB is fine for prose but a user
	// could conceivably paste a long URL into a task title.
	const maxLine = 1024 * 1024
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, maxLine)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		m := checkboxLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		done := m[2] == "x" || m[2] == "X"
		rest := m[3]

		// Pull off the trailing marker comment (if any) before storing
		// the title so the title field never contains the bookkeeping
		// metadata.
		title := rest
		var mk marker
		if loc := markerComment.FindStringSubmatchIndex(rest); loc != nil {
			payload := rest[loc[2]:loc[3]]
			parsed, err := parseMarkerPayload(payload)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
			}
			mk = parsed
			mk.Present = true
			title = rest[:loc[0]]
		}

		out = append(out, parsedTask{
			Title:      trimTrailingSpace(title),
			Done:       done,
			LineNumber: lineNo,
			SourcePath: path,
			Marker:     mk,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

// parseMarkerPayload turns "id=abc source=markdown" into a marker. The
// payload uses space-separated `key=value` pairs because that matches the
// example in the PR-50 brief and avoids having to escape commas inside
// values.
func parseMarkerPayload(payload string) (marker, error) {
	mk := marker{Extra: map[string]string{}}
	for _, raw := range splitFields(payload) {
		eq := indexByte(raw, '=')
		if eq < 0 {
			return marker{}, fmt.Errorf("%w: %q", ErrInvalidMarker, raw)
		}
		key := raw[:eq]
		val := raw[eq+1:]
		switch key {
		case "id":
			mk.ID = val
		case "external_id":
			mk.ExternalID = val
		case "source":
			mk.Source = val
		default:
			mk.Extra[key] = val
		}
	}
	return mk, nil
}

// splitFields splits on runs of ASCII whitespace. We do not use
// strings.Fields because we want a guarantee that values cannot contain
// whitespace (they are short ids / source names), and a stricter splitter
// makes that contract explicit.
func splitFields(s string) []string {
	var out []string
	start := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		isSpace := c == ' ' || c == '\t'
		if isSpace {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimTrailingSpace(s string) string {
	end := len(s)
	for end > 0 {
		c := s[end-1]
		if c == ' ' || c == '\t' {
			end--
			continue
		}
		break
	}
	return s[:end]
}
