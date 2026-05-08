// Package policy centralizes security policy helpers shared across
// packages that cannot import each other without creating a cycle.
package policy

import (
	"path/filepath"
	"strings"
)

// CwdAllowed reports whether cwd satisfies the allowlist. An empty
// prefixes slice means "all paths allowed". Otherwise cwd must equal
// one of the prefixes or be a direct descendant (starts with prefix+"/").
//
// CWD is cleaned with filepath.Clean before comparison so that paths
// containing ".." cannot bypass the check.
func CwdAllowed(cwd string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	clean := filepath.Clean(cwd)
	for _, p := range prefixes {
		cleanP := filepath.Clean(p)
		if clean == cleanP || strings.HasPrefix(clean, cleanP+"/") {
			return true
		}
	}
	return false
}
