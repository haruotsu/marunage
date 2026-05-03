// Package permission implements the auto-accept allowlist matcher used by
// PR-42 dispatch when running Claude under the non-bypass permission
// modes ("default" / "acceptEdits"). It parses the
// `execution.auto_accept_tools` entries from config.toml and decides
// whether a single (tool, args) request should be auto-confirmed or
// bubble up to the human-escalation path.
//
// Grammar (docs/requirement.md L657-661):
//
//	Read                       — tool-name only; allow any args for that tool
//	Bash(git status:*)         — tool + args prefix (":*" is the only wildcard)
//	Bash(echo hello)           — tool + exact args literal
//
// The ":*" wildcard is suffix-any (no word boundary), matching the spec
// phrase "以降任意". A trailing space is intentionally not implied — pin
// "Bash(git status :*)" if the user wants to reject "git statusfoo".
package permission

import (
	"errors"
	"fmt"
	"strings"
)

// Typed sentinel errors returned by New. PR-42 / the CLI loader can use
// errors.Is to surface a precise hint when config.toml has a typo.
var (
	ErrEmptyRule     = errors.New("permission: rule is empty")
	ErrUnclosedParen = errors.New("permission: rule missing closing ')'")
	ErrEmptyArgs     = errors.New("permission: rule has empty argument list")
	ErrMissingTool   = errors.New("permission: rule missing tool name")
)

// Matcher decides whether a (tool, args) pair is in the allowlist.
// Construction parses every rule once; Allow is a tight loop over the
// pre-parsed slice so it is cheap to call from the dispatcher's hot
// path (one call per Claude permission prompt).
type Matcher struct {
	rules []rule
}

type ruleKind uint8

const (
	ruleAll    ruleKind = iota // tool-name only ("Read")
	ruleExact                  // exact args match ("Bash(echo hello)")
	rulePrefix                 // args prefix match ("Bash(git status:*)")
)

type rule struct {
	tool string
	kind ruleKind
	arg  string // exact literal or prefix sans ":*"
}

// wildcardSuffix is the only wildcard token the requirement.md grammar
// recognises. Centralised so the parser and the docs cannot drift.
const wildcardSuffix = ":*"

// New parses raw allowlist entries (typically Config.Execution.AutoAcceptTools)
// into a Matcher. Returns the first parse error so a config.toml typo fails
// loudly at startup rather than silently denying every prompt forever.
func New(rules []string) (*Matcher, error) {
	parsed := make([]rule, 0, len(rules))
	for _, raw := range rules {
		r, err := parseRule(raw)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", raw, err)
		}
		parsed = append(parsed, r)
	}
	return &Matcher{rules: parsed}, nil
}

func parseRule(s string) (rule, error) {
	if s == "" {
		return rule{}, ErrEmptyRule
	}
	open := strings.Index(s, "(")
	if open < 0 {
		return rule{tool: s, kind: ruleAll}, nil
	}
	if open == 0 {
		return rule{}, ErrMissingTool
	}
	if !strings.HasSuffix(s, ")") {
		return rule{}, ErrUnclosedParen
	}
	tool := s[:open]
	args := s[open+1 : len(s)-1]
	if args == "" {
		return rule{}, ErrEmptyArgs
	}
	if strings.HasSuffix(args, wildcardSuffix) {
		return rule{
			tool: tool,
			kind: rulePrefix,
			arg:  strings.TrimSuffix(args, wildcardSuffix),
		}, nil
	}
	return rule{tool: tool, kind: ruleExact, arg: args}, nil
}

// Allow reports whether the (tool, args) request matches any rule.
// args should be the literal text Claude would put inside the tool's
// invocation parens (e.g. the shell line for Bash). Tool-name-only
// rules ignore args; exact rules require a byte-for-byte match;
// prefix rules require strings.HasPrefix.
func (m *Matcher) Allow(tool, args string) bool {
	for _, r := range m.rules {
		if r.tool != tool {
			continue
		}
		switch r.kind {
		case ruleAll:
			return true
		case ruleExact:
			if args == r.arg {
				return true
			}
		case rulePrefix:
			if strings.HasPrefix(args, r.arg) {
				return true
			}
		}
	}
	return false
}
