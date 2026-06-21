package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/haruotsu/marunage/internal/store"
)

// isDirOnDisk is the production filesystem probe resolveCwd uses to decide
// whether a candidate ghq clone path exists.
func isDirOnDisk(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolveCwd picks the working directory a task runs in. Resolution order:
//
//  1. an explicit task CWD always wins;
//  2. with strategy=="ghq", a repo-bound task (a GitHub / GHES issue or PR,
//     recognised by its source + external_url) maps onto its local ghq clone
//     (<ghqRoot>/<host>/<owner>/<repo>) when that directory exists;
//  3. everything else falls back to defaultCwd.
//
// The clone path is keyed by the URL host (not a hardcoded github.com) so a
// GitHub Enterprise Server task whose external_url is https://ghe.corp/o/r
// resolves to <ghqRoot>/ghe.corp/o/r — exactly how ghq lays repos out. Only
// github-flavoured sources are probed so a gmail/calendar URL is never
// mistaken for a repo path.
//
// The second return is a short human note, non-empty only in the "repo was
// identified but is not cloned locally" case, so the dispatcher can record
// why the task ran in the default root instead of the repo. isDir abstracts
// the filesystem probe for tests.
func resolveCwd(task store.Task, defaultCwd, ghqRoot, strategy string, isDir func(string) bool) (cwd, note string) {
	if task.CWD != "" {
		return task.CWD, ""
	}
	if strategy == "ghq" && ghqRoot != "" && isGitHubSource(task.Source) {
		if host, owner, repo, ok := parseRepoURL(task.ExternalURL); ok {
			p := filepath.Join(ghqRoot, host, owner, repo)
			if isDir(p) {
				return p, ""
			}
			return defaultCwd, fmt.Sprintf("cwd: %s/%s/%s not cloned under ghq root; ran in default_cwd", host, owner, repo)
		}
	}
	return defaultCwd, ""
}

// isGitHubSource reports whether a source name is one of the github-backed
// plugins (plain "github" today; the prefix keeps future variants like
// "github_pr" repo-aware automatically). GHES tasks still report source
// "github" — only their external_url host differs — so this gate covers them.
func isGitHubSource(source string) bool {
	return source == "github" || strings.HasPrefix(source, "github")
}

// parseRepoURL extracts host/owner/repo from a forge URL — an issue, PR, or
// bare repo link on github.com or a GHES host all parse the same way. It
// returns ok=false for malformed URLs so callers fall back rather than
// building a bogus path.
func parseRepoURL(rawURL string) (host, owner, repo string, ok bool) {
	rest := rawURL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+len("://"):]
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return "", "", "", false
	}
	host, owner = parts[0], parts[1]
	repo = strings.TrimSuffix(parts[2], ".git")
	if host == "" || owner == "" || repo == "" {
		return "", "", "", false
	}
	return host, owner, repo, true
}
