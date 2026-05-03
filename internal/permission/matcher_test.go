package permission

import (
	"errors"
	"strings"
	"testing"
)

// TestMatcherEmpty pins the safe default: an empty allowlist (or nil)
// denies every tool invocation. The dispatcher must escalate on any
// permission prompt rather than silently auto-accept just because no
// rules are loaded.
func TestMatcherEmpty(t *testing.T) {
	cases := [][]string{nil, {}}
	for _, rules := range cases {
		m, err := New(rules)
		if err != nil {
			t.Fatalf("New(%v) = %v", rules, err)
		}
		if m.Allow("Read", "") {
			t.Errorf("Allow(Read,\"\") on empty allowlist = true; want false")
		}
		if m.Allow("Bash", "git status") {
			t.Errorf("Allow(Bash,\"git status\") on empty allowlist = true; want false")
		}
	}
}

// TestMatcherToolNameOnly: a bare tool name ("Read") allows that tool with
// any args. This matches the requirement.md auto_accept_tools sample where
// "Read" / "Grep" / "Glob" are listed without parens.
func TestMatcherToolNameOnly(t *testing.T) {
	m, err := New([]string{"Read", "Glob"})
	if err != nil {
		t.Fatalf("New = %v", err)
	}

	allow := []struct{ tool, args string }{
		{"Read", ""},
		{"Read", "/etc/passwd"},
		{"Glob", "**/*.go"},
	}
	for _, c := range allow {
		if !m.Allow(c.tool, c.args) {
			t.Errorf("Allow(%q,%q) = false; want true", c.tool, c.args)
		}
	}

	deny := []struct{ tool, args string }{
		{"Bash", "git status"},
		{"WebSearch", "anything"},
	}
	for _, c := range deny {
		if m.Allow(c.tool, c.args) {
			t.Errorf("Allow(%q,%q) = true; want false (not in allowlist)", c.tool, c.args)
		}
	}
}

// TestMatcherArgPrefixWildcard: "Bash(git status:*)" allows Bash invocations
// whose args start with "git status". The ":*" suffix is the only wildcard
// the requirement.md grammar specifies, and it always sits at the end of
// the args portion.
func TestMatcherArgPrefixWildcard(t *testing.T) {
	m, err := New([]string{"Bash(git status:*)"})
	if err != nil {
		t.Fatalf("New = %v", err)
	}

	allow := []string{
		"git status",
		"git status .",
		"git status -s",
		"git status --short",
	}
	for _, args := range allow {
		if !m.Allow("Bash", args) {
			t.Errorf("Allow(Bash, %q) = false; want true under git-status prefix", args)
		}
	}

	deny := []string{
		"git diff",
		"rm -rf /",
	}
	for _, args := range deny {
		if m.Allow("Bash", args) {
			t.Errorf("Allow(Bash, %q) = true; want false (different command)", args)
		}
	}

	// Boundary-less wildcard is intentional. Spec text is "以降任意"
	// (anything after), with no word-boundary constraint. Pin the
	// surprising-but-documented cases so a future reader cannot "fix"
	// the matcher to require a space after the prefix without first
	// revising docs/requirement.md.
	allowBoundary := []string{
		"git statusfoo",        // no separator after the prefix
		"git status; rm -rf /", // shell metacharacter: prefix is literal
	}
	for _, args := range allowBoundary {
		if !m.Allow("Bash", args) {
			t.Errorf("Allow(Bash, %q) = false; the ':*' wildcard is suffix-any, not word-boundary", args)
		}
	}
}

// TestMatcherArgExact: "Bash(echo hello)" without ":*" requires exact match
// of the args text. This lets users pin a single-shot allowance without
// opening the door to anything that happens to start with the same prefix.
func TestMatcherArgExact(t *testing.T) {
	m, err := New([]string{"Bash(echo hello)"})
	if err != nil {
		t.Fatalf("New = %v", err)
	}

	if !m.Allow("Bash", "echo hello") {
		t.Errorf("Allow(Bash, \"echo hello\") = false; want true (exact match)")
	}
	deny := []string{
		"echo hi",
		"echo hello world",
		"echo hello\n",
	}
	for _, args := range deny {
		if m.Allow("Bash", args) {
			t.Errorf("Allow(Bash, %q) = true; want false (not exact match)", args)
		}
	}
}

// TestMatcherMultipleRulesUnion: rules combine as a union. Any single
// matching rule produces accept; iteration order is irrelevant.
func TestMatcherMultipleRulesUnion(t *testing.T) {
	m, err := New([]string{"Read", "Bash(git status:*)", "Bash(git diff:*)"})
	if err != nil {
		t.Fatalf("New = %v", err)
	}
	allow := []struct{ tool, args string }{
		{"Read", "anything"},
		{"Bash", "git status"},
		{"Bash", "git diff HEAD~1"},
	}
	for _, c := range allow {
		if !m.Allow(c.tool, c.args) {
			t.Errorf("Allow(%q,%q) = false; want true (union of rules)", c.tool, c.args)
		}
	}
	if m.Allow("Bash", "git push") {
		t.Errorf("Allow(Bash, \"git push\") = true; want false (no matching rule)")
	}
}

// TestMatcherDegenerateWildcardAllowsAnyArgs pins the corner of the
// grammar where args is just ":*". parseRule strips the wildcard suffix
// and is left with prefix == "", which strings.HasPrefix treats as
// always-true. The result is "Bash(:*)" being equivalent to bare "Bash".
// The grammar permits this by construction; pin the equivalence so a
// future tightening of parseRule (e.g. rejecting empty prefix) cannot
// silently change the meaning of an in-the-wild config.toml entry.
func TestMatcherDegenerateWildcardAllowsAnyArgs(t *testing.T) {
	m, err := New([]string{"Bash(:*)"})
	if err != nil {
		t.Fatalf("New(Bash(:*)): %v", err)
	}
	allow := []string{"", "git status", "rm -rf /", "echo\nhello"}
	for _, args := range allow {
		if !m.Allow("Bash", args) {
			t.Errorf("Allow(Bash, %q) = false; Bash(:*) should be a Bash-everything rule", args)
		}
	}
	if m.Allow("Read", "anything") {
		t.Errorf("Allow(Read, anything) = true; Bash(:*) must not bleed across tools")
	}
}

// TestMatcherPrefixHandlesNewlineArgs documents that args matching is
// byte-literal: a multi-line shell snippet matches a prefix rule iff its
// leading bytes match. This matters because Claude can pass a heredoc or
// a shell line with embedded \n; we must neither silently drop the rule
// nor accidentally split on newlines and match each line independently.
func TestMatcherPrefixHandlesNewlineArgs(t *testing.T) {
	m, err := New([]string{"Bash(cat <<EOF:*)"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !m.Allow("Bash", "cat <<EOF\nhello\nEOF") {
		t.Errorf("Allow(Bash, multiline) = false; prefix rule must accept embedded newlines after the prefix")
	}
	if m.Allow("Bash", "echo hi\ncat <<EOF\nhello\nEOF") {
		t.Errorf("Allow(Bash, prefixed-by-other-cmd) = true; prefix rule must anchor at byte 0")
	}
}

// TestMatcherInvalidRules: malformed allowlist entries surface a typed
// error at construction so a typo in config.toml fails loudly at startup
// rather than silently auto-accepting nothing forever.
func TestMatcherInvalidRules(t *testing.T) {
	cases := []struct {
		name    string
		rule    string
		wantErr error
	}{
		{"empty rule", "", ErrEmptyRule},
		{"missing close paren", "Bash(git status", ErrUnclosedParen},
		{"empty args", "Bash()", ErrEmptyArgs},
		{"missing tool name", "(git status)", ErrMissingTool},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New([]string{tc.rule})
			if err == nil {
				t.Fatalf("New(%q) = nil; want %v", tc.rule, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("New(%q) err = %v; want errors.Is(%v)", tc.rule, err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.rule) && tc.rule != "" {
				t.Errorf("err = %q; want mention of the offending rule %q", err.Error(), tc.rule)
			}
		})
	}
}

// TestMatcherInvalidRuleErrorContainsIndex pins that when one entry in a
// multi-rule slice fails to parse, the error names the offending index so
// the user can find it in their config.toml without grep'ing through 20
// lookalike entries. Without the index a "Bash(...): missing close paren"
// message could match many rules in a real allowlist.
func TestMatcherInvalidRuleErrorContainsIndex(t *testing.T) {
	rules := []string{"Read", "Glob", "Bash(git status"} // index 2 is malformed
	_, err := New(rules)
	if err == nil {
		t.Fatal("New = nil; want error for malformed rule at index 2")
	}
	if !strings.Contains(err.Error(), "rule[2]") {
		t.Errorf("err = %q; want mention of \"rule[2]\" so the user knows which entry to fix", err.Error())
	}
}
