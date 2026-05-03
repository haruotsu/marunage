package skills

import (
	"errors"
	"strings"
	"testing"
)

// TestExtractVersion_HTMLComment pins the documented metadata convention:
// each SKILL.md leads with an HTML comment of the shape
//
//	<!-- version: X.Y.Z -->
//
// so the file remains valid Markdown (the comment renders as nothing) while
// `marunage setup --skills --check-updates` can still read the version.
func TestExtractVersion_HTMLComment(t *testing.T) {
	content := []byte("<!-- version: 0.2.3 -->\n# triage\n\nbody\n")
	got, err := ExtractVersion(content)
	if err != nil {
		t.Fatalf("ExtractVersion: %v", err)
	}
	if got != "0.2.3" {
		t.Errorf("ExtractVersion = %q; want %q", got, "0.2.3")
	}
}

// TestExtractVersion_TolerantToWhitespace pins that minor formatting
// variance (extra spaces inside the HTML comment) does not break parsing.
// Users editing the file by hand will not always preserve exact spacing.
func TestExtractVersion_TolerantToWhitespace(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"tight", "<!--version:1.0.0-->\n", "1.0.0"},
		{"padded", "<!--   version:   1.2.3   -->\n", "1.2.3"},
		{"leading-blank", "\n\n<!-- version: 0.4.0 -->\n", "0.4.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractVersion([]byte(tc.in))
			if err != nil {
				t.Fatalf("ExtractVersion: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q; want %q", got, tc.want)
			}
		})
	}
}

// TestExtractVersion_Missing returns the typed sentinel so callers
// (the installer) can distinguish "no version metadata" from "I/O error".
func TestExtractVersion_Missing(t *testing.T) {
	content := []byte("# triage\n\nbody\n")
	_, err := ExtractVersion(content)
	if !errors.Is(err, ErrMissingVersion) {
		t.Errorf("err = %v; want errors.Is(_, ErrMissingVersion)", err)
	}
}

// TestValidateRequiredSections_Present passes when every required H2 is
// found in the document. The matcher must accept Markdown's leading-`## `
// convention but NOT match deeper headers (`### 判定ロジック`) — those
// nest a different concept than the SKILL contract requires.
func TestValidateRequiredSections_Present(t *testing.T) {
	doc := []byte("# triage\n\n## 判定ロジック\n\nrules\n\n## 出力フォーマット\n\nschema\n")
	if err := ValidateRequiredSections(doc, []string{"判定ロジック", "出力フォーマット"}); err != nil {
		t.Errorf("ValidateRequiredSections: %v", err)
	}
}

// TestValidateRequiredSections_Missing returns ErrMissingSection naming
// the absent header, so the CLI surface can render the actionable error
// without re-scanning the file itself.
func TestValidateRequiredSections_Missing(t *testing.T) {
	doc := []byte("# triage\n\n## 判定ロジック\n\nrules\n")
	err := ValidateRequiredSections(doc, []string{"判定ロジック", "出力フォーマット"})
	if !errors.Is(err, ErrMissingSection) {
		t.Fatalf("err = %v; want errors.Is(_, ErrMissingSection)", err)
	}
	if !strings.Contains(err.Error(), "出力フォーマット") {
		t.Errorf("err message = %q; want it to mention the missing section", err.Error())
	}
}

// TestValidateRequiredSections_RejectsDeeperHeaders pins the matcher's
// strictness: `### 出力フォーマット` (an H3) does not satisfy the H2
// contract and must be reported as missing. The H2/H3 distinction matters
// because dispatch / triage tooling looks for the top-level structure.
func TestValidateRequiredSections_RejectsDeeperHeaders(t *testing.T) {
	doc := []byte("# triage\n\n## 判定ロジック\n\nrules\n\n### 出力フォーマット\n")
	err := ValidateRequiredSections(doc, []string{"判定ロジック", "出力フォーマット"})
	if !errors.Is(err, ErrMissingSection) {
		t.Errorf("err = %v; want ErrMissingSection (H3 should not satisfy the H2 contract)", err)
	}
}
