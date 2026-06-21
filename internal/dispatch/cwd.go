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
//  2. with strategy=="ghq", a repo-bound task (a GitHub issue/PR, recognised
//     by its external_url) maps onto its local ghq clone
//     (<ghqRoot>/github.com/<owner>/<repo>) when that directory exists;
//  3. everything else falls back to defaultCwd.
//
// The second return is a short human note, non-empty only in the "repo was
// identified but is not cloned locally" case, so the dispatcher can record
// why the task ran in the default root instead of the repo. isDir abstracts
// the filesystem probe for tests.
func resolveCwd(task store.Task, defaultCwd, ghqRoot, strategy string, isDir func(string) bool) (cwd, note string) {
	if task.CWD != "" {
		return task.CWD, ""
	}
	if strategy == "ghq" && ghqRoot != "" {
		if owner, repo, ok := parseGitHubRepo(task.ExternalURL); ok {
			p := filepath.Join(ghqRoot, "github.com", owner, repo)
			if isDir(p) {
				return p, ""
			}
			return defaultCwd, fmt.Sprintf("cwd: %s/%s not cloned under ghq root; ran in default_cwd", owner, repo)
		}
	}
	return defaultCwd, ""
}

// parseGitHubRepo extracts owner/repo from a github.com URL — an issue, PR, or
// bare repo link all parse the same way. It returns ok=false for non-github or
// malformed URLs so callers fall back rather than building a bogus path.
func parseGitHubRepo(rawURL string) (owner, repo string, ok bool) {
	const marker = "github.com/"
	i := strings.Index(rawURL, marker)
	if i < 0 {
		return "", "", false
	}
	parts := strings.Split(rawURL[i+len(marker):], "/")
	if len(parts) < 2 {
		return "", "", false
	}
	owner = parts[0]
	repo = strings.TrimSuffix(parts[1], ".git")
	if owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}
