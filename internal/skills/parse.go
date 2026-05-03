// Package skills installs and validates the SKILL.md bundle that the
// marunage triage / execute / reflect Claude flows depend on. The OSS
// ships its own canonical Skills via //go:embed and surfaces them through
// `marunage setup --skills`; users are free to edit on-disk copies and
// `--check-updates` will surface drift against the embedded version.
package skills

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrMissingVersion is the typed sentinel ExtractVersion returns when the
// caller's SKILL.md lacks a `<!-- version: X.Y.Z -->` header. The
// installer relies on the typed sentinel (rather than a string match) to
// distinguish "no version yet" from real I/O errors when an on-disk file
// has been hand-edited and the leading comment was deleted.
var ErrMissingVersion = errors.New("skills: SKILL.md is missing a `<!-- version: X.Y.Z -->` metadata comment")

// ErrMissingSection is the sentinel for "a required H2 header is absent".
// It wraps the section name so the CLI surface can render the actionable
// "fix your SKILL.md" message without re-parsing the file.
var ErrMissingSection = errors.New("skills: SKILL.md is missing a required section")

// versionRe matches the documented `<!-- version: X.Y.Z -->` metadata
// comment. The pattern is intentionally permissive about whitespace so
// users editing SKILL.md by hand do not have to worry about exact
// formatting; it is strict about the `version:` keyword and the dotted
// version body so an accidental comment is not misread as metadata.
var versionRe = regexp.MustCompile(`<!--\s*version\s*:\s*([0-9A-Za-z][0-9A-Za-z.+\-]*)\s*-->`)

// ExtractVersion returns the version string declared in the leading
// HTML comment of a SKILL.md document. The first match wins, so a
// document with multiple comments yields the topmost declaration —
// callers should not rely on overriding via later comments.
func ExtractVersion(content []byte) (string, error) {
	m := versionRe.FindSubmatch(content)
	if m == nil {
		return "", ErrMissingVersion
	}
	return string(m[1]), nil
}

// ValidateRequiredSections checks that every name in `required` appears
// as an H2 header (`## name`) in the document. Missing entries return
// ErrMissingSection wrapped with the offending names so the caller can
// surface them in one error rather than re-running the check per name.
//
// The H2 strictness is deliberate: deeper headers (H3+) belong to a
// section's interior and would let a hand-edited SKILL.md silently lose
// its top-level contract.
func ValidateRequiredSections(content []byte, required []string) error {
	headers := h2Headers(content)
	var missing []string
	for _, want := range required {
		if !contains(headers, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrMissingSection, strings.Join(missing, ", "))
}

// h2Headers returns the trimmed text of every `## ...` line in the
// document. The `## ` (two hashes + space) prefix excludes deeper
// headers — `### foo` starts with `##` but the third byte is `#`, not
// the space the prefix demands, so H3+ are skipped automatically.
//
// We do not parse fenced code blocks, so a `## ` literal inside a code
// fence would be picked up; SKILL.md authors are expected to keep H2
// reserved for real sections.
func h2Headers(content []byte) []string {
	var out []string
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if !strings.HasPrefix(trimmed, "## ") {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
	}
	return out
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
