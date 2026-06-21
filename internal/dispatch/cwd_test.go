package dispatch

import (
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

func TestParseGitHubRepo(t *testing.T) {
	cases := []struct {
		url         string
		owner, repo string
		ok          bool
	}{
		{"https://github.com/haruotsu/marunage/issues/42", "haruotsu", "marunage", true},
		{"https://github.com/haruotsu/marunage/pull/7", "haruotsu", "marunage", true},
		{"https://github.com/haruotsu/marunage", "haruotsu", "marunage", true},
		{"https://github.com/haruotsu/marunage.git", "haruotsu", "marunage", true},
		{"https://gitlab.com/haruotsu/marunage/issues/1", "", "", false},
		{"https://github.com/haruotsu", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := parseGitHubRepo(c.url)
		if ok != c.ok || owner != c.owner || repo != c.repo {
			t.Errorf("parseGitHubRepo(%q) = (%q,%q,%v); want (%q,%q,%v)",
				c.url, owner, repo, ok, c.owner, c.repo, c.ok)
		}
	}
}

func TestResolveCwd(t *testing.T) {
	const root = "/home/u/src"
	clone := root + "/github.com/haruotsu/marunage"
	dirExists := func(p string) bool { return p == clone }

	t.Run("explicit cwd wins", func(t *testing.T) {
		task := store.Task{CWD: "/explicit/path", ExternalURL: "https://github.com/haruotsu/marunage/issues/1"}
		cwd, note := resolveCwd(task, "/default", root, "ghq", dirExists)
		if cwd != "/explicit/path" || note != "" {
			t.Fatalf("got (%q,%q)", cwd, note)
		}
	})

	t.Run("ghq resolves a cloned repo", func(t *testing.T) {
		task := store.Task{Source: "github", ExternalURL: "https://github.com/haruotsu/marunage/issues/1"}
		cwd, note := resolveCwd(task, "/default", root, "ghq", dirExists)
		if cwd != clone || note != "" {
			t.Fatalf("got (%q,%q); want (%q,\"\")", cwd, note, clone)
		}
	})

	t.Run("uncloned repo falls back with a note", func(t *testing.T) {
		task := store.Task{Source: "github", ExternalURL: "https://github.com/other/repo/issues/9"}
		cwd, note := resolveCwd(task, "/default", root, "ghq", dirExists)
		if cwd != "/default" || note == "" {
			t.Fatalf("got (%q,%q); want (/default, <note>)", cwd, note)
		}
	})

	t.Run("non-github task uses default", func(t *testing.T) {
		task := store.Task{Source: "gmail", ExternalURL: "https://mail.google.com/x"}
		cwd, note := resolveCwd(task, "/default", root, "ghq", dirExists)
		if cwd != "/default" || note != "" {
			t.Fatalf("got (%q,%q)", cwd, note)
		}
	})

	t.Run("fixed strategy ignores ghq", func(t *testing.T) {
		task := store.Task{Source: "github", ExternalURL: "https://github.com/haruotsu/marunage/issues/1"}
		cwd, note := resolveCwd(task, "/default", root, "fixed", dirExists)
		if cwd != "/default" || note != "" {
			t.Fatalf("got (%q,%q)", cwd, note)
		}
	})

	t.Run("empty ghq root disables resolution", func(t *testing.T) {
		task := store.Task{Source: "github", ExternalURL: "https://github.com/haruotsu/marunage/issues/1"}
		cwd, note := resolveCwd(task, "/default", "", "ghq", dirExists)
		if cwd != "/default" || note != "" {
			t.Fatalf("got (%q,%q)", cwd, note)
		}
	})
}
