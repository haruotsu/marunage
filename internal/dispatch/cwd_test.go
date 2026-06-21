package dispatch

import (
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

func TestParseRepoURL(t *testing.T) {
	cases := []struct {
		url               string
		host, owner, repo string
		ok                bool
	}{
		{"https://github.com/haruotsu/marunage/issues/42", "github.com", "haruotsu", "marunage", true},
		{"https://github.com/haruotsu/marunage/pull/7", "github.com", "haruotsu", "marunage", true},
		{"https://github.com/haruotsu/marunage", "github.com", "haruotsu", "marunage", true},
		{"https://github.com/haruotsu/marunage.git", "github.com", "haruotsu", "marunage", true},
		// GitHub Enterprise Server: host is not github.com.
		{"https://ghe.corp.example/team/service/issues/3", "ghe.corp.example", "team", "service", true},
		{"https://github.com/haruotsu", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, c := range cases {
		host, owner, repo, ok := parseRepoURL(c.url)
		if ok != c.ok || host != c.host || owner != c.owner || repo != c.repo {
			t.Errorf("parseRepoURL(%q) = (%q,%q,%q,%v); want (%q,%q,%q,%v)",
				c.url, host, owner, repo, ok, c.host, c.owner, c.repo, c.ok)
		}
	}
}

func TestResolveCwd(t *testing.T) {
	const root = "/home/u/src"
	clone := root + "/github.com/haruotsu/marunage"
	gheClone := root + "/ghe.corp.example/team/service"
	dirExists := func(p string) bool { return p == clone || p == gheClone }

	t.Run("explicit cwd wins", func(t *testing.T) {
		task := store.Task{Source: "github", CWD: "/explicit/path", ExternalURL: "https://github.com/haruotsu/marunage/issues/1"}
		cwd, note := resolveCwd(task, "/default", root, "ghq", dirExists)
		if cwd != "/explicit/path" || note != "" {
			t.Fatalf("got (%q,%q)", cwd, note)
		}
	})

	t.Run("ghq resolves a cloned github.com repo", func(t *testing.T) {
		task := store.Task{Source: "github", ExternalURL: "https://github.com/haruotsu/marunage/issues/1"}
		cwd, note := resolveCwd(task, "/default", root, "ghq", dirExists)
		if cwd != clone || note != "" {
			t.Fatalf("got (%q,%q); want (%q,\"\")", cwd, note, clone)
		}
	})

	t.Run("ghq resolves a cloned GHES repo by host", func(t *testing.T) {
		task := store.Task{Source: "github", ExternalURL: "https://ghe.corp.example/team/service/pull/9"}
		cwd, note := resolveCwd(task, "/default", root, "ghq", dirExists)
		if cwd != gheClone || note != "" {
			t.Fatalf("got (%q,%q); want (%q,\"\")", cwd, note, gheClone)
		}
	})

	t.Run("uncloned repo falls back with a note", func(t *testing.T) {
		task := store.Task{Source: "github", ExternalURL: "https://github.com/other/repo/issues/9"}
		cwd, note := resolveCwd(task, "/default", root, "ghq", dirExists)
		if cwd != "/default" || note == "" {
			t.Fatalf("got (%q,%q); want (/default, <note>)", cwd, note)
		}
	})

	t.Run("non-github source is never probed", func(t *testing.T) {
		task := store.Task{Source: "gmail", ExternalURL: "https://mail.google.com/mail/u/0"}
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
