package policy_test

import (
	"testing"

	"github.com/haruotsu/marunage/internal/policy"
)

func TestCwdAllowed_EmptyPrefixes_AllowsAll(t *testing.T) {
	t.Parallel()
	if !policy.CwdAllowed("/any/path", nil) {
		t.Error("empty prefixes should allow all paths")
	}
}

func TestCwdAllowed_MatchingPrefix_Allowed(t *testing.T) {
	t.Parallel()
	if !policy.CwdAllowed("/home/user/src/repo", []string{"/home/user/src"}) {
		t.Error("cwd under allowed prefix should be allowed")
	}
}

func TestCwdAllowed_ExactMatch_Allowed(t *testing.T) {
	t.Parallel()
	if !policy.CwdAllowed("/home/user/src", []string{"/home/user/src"}) {
		t.Error("exact match of prefix should be allowed")
	}
}

func TestCwdAllowed_UnrelatedPath_Denied(t *testing.T) {
	t.Parallel()
	if policy.CwdAllowed("/etc/passwd", []string{"/home/user/src"}) {
		t.Error("unrelated path should be denied")
	}
}

// Boundary: /home/user/work must NOT match prefix /home/user/works
func TestCwdAllowed_PrefixBoundary_Denied(t *testing.T) {
	t.Parallel()
	if policy.CwdAllowed("/home/user/works/proj", []string{"/home/user/work"}) {
		t.Error("/home/user/works should NOT match prefix /home/user/work (boundary violation)")
	}
}

// Traversal: ../ must not bypass the prefix check
func TestCwdAllowed_DotDotTraversal_Denied(t *testing.T) {
	t.Parallel()
	if policy.CwdAllowed("/home/user/src/../../../etc", []string{"/home/user/src"}) {
		t.Error("path with .. traversal must not bypass prefix check")
	}
}

func TestCwdAllowed_MultiplePrefix_MatchesFirst(t *testing.T) {
	t.Parallel()
	prefixes := []string{"/home/user/src", "/tmp/works"}
	if !policy.CwdAllowed("/tmp/works/myrepo", prefixes) {
		t.Error("cwd matching second prefix should be allowed")
	}
}

func TestCwdAllowed_EmptyCWD_WithPrefixes_Denied(t *testing.T) {
	t.Parallel()
	if policy.CwdAllowed("", []string{"/home/user/src"}) {
		t.Error("empty cwd with non-empty prefixes should be denied")
	}
}
