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
		"git statusfoo", // prefix-but-no-boundary; we still treat as match per spec since ":*" is "any suffix"
		"rm -rf /",
	}
	// "git statusfoo" matches because the ":*" wildcard does not require a
	// word boundary; the spec text is "以降任意" (anything after). Pin the
	// deny set excluding that ambiguous case but document the choice in a
	// dedicated subtest below.
	for _, args := range deny[:1] {
		if m.Allow("Bash", args) {
			t.Errorf("Allow(Bash, %q) = true; want false (different command)", args)
		}
	}
	for _, args := range deny[2:] {
		if m.Allow("Bash", args) {
			t.Errorf("Allow(Bash, %q) = true; want false", args)
		}
	}

	// Boundary-less wildcard is intentional. Documented here so a future
	// reader does not "fix" the matcher to require a space after the
	// prefix without first revising docs/requirement.md.
	if !m.Allow("Bash", "git statusfoo") {
		t.Errorf("Allow(Bash, %q) = false; the ':*' wildcard is suffix-any, not word-boundary", "git statusfoo")
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
