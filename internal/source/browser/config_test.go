package browser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return path
}

// TestLoadConfigParsesSingleSite verifies the happy path: a minimal
// browser.toml round-trips into a SiteConfig with every documented field
// populated.
func TestLoadConfigParsesSingleSite(t *testing.T) {
	t.Parallel()

	body := `
[[site]]
name = "slack-saved"
url = "https://app.slack.com/saved"
item_selector = ".p-saved_msg"
key_field = "id"

[site.fields]
title = { selector = ".p-msg__title" }
body = { selector = ".p-msg__body" }
id = { selector = "[data-id]", attr = "data-id" }
`
	path := writeFile(t, t.TempDir(), "browser.toml", body)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Sites) != 1 {
		t.Fatalf("Sites = %d, want 1", len(cfg.Sites))
	}
	s := cfg.Sites[0]
	if s.Name != "slack-saved" {
		t.Errorf("Name = %q", s.Name)
	}
	if s.URL != "https://app.slack.com/saved" {
		t.Errorf("URL = %q", s.URL)
	}
	if s.ItemSelector != ".p-saved_msg" {
		t.Errorf("ItemSelector = %q", s.ItemSelector)
	}
	if s.KeyField != "id" {
		t.Errorf("KeyField = %q", s.KeyField)
	}
	if r, ok := s.Fields["id"]; !ok || r.Selector != "[data-id]" || r.Attr != "data-id" {
		t.Errorf("Fields[id] = %+v ok=%v", r, ok)
	}
	if r := s.Fields["title"]; r.Attr != "" || r.Selector != ".p-msg__title" {
		t.Errorf("Fields[title] = %+v", r)
	}
}

// TestLoadConfigParsesMultipleSitesPreservesOrder asserts that two sites
// declared in TOML come back in declaration order — the plugin's List
// concatenates per-site results in this order so a stable ordering is
// load-bearing for diffability.
func TestLoadConfigParsesMultipleSitesPreservesOrder(t *testing.T) {
	t.Parallel()

	body := `
[[site]]
name = "first"
url = "https://example.com/1"
item_selector = ".x"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }

[[site]]
name = "second"
url = "https://example.com/2"
item_selector = ".y"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`
	path := writeFile(t, t.TempDir(), "browser.toml", body)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Sites) != 2 {
		t.Fatalf("Sites = %d", len(cfg.Sites))
	}
	if cfg.Sites[0].Name != "first" || cfg.Sites[1].Name != "second" {
		t.Errorf("order: %q, %q", cfg.Sites[0].Name, cfg.Sites[1].Name)
	}
}

// TestLoadConfigRejectsMissingFields enumerates every required field and
// verifies the typed sentinel surfaces with a message naming the field
// (so the operator knows what to fix). We deliberately test these
// scenarios in one table because the validator returns the same sentinel
// for each — the message is the only differentiator.
func TestLoadConfigRejectsMissingFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			name: "missing name",
			body: `[[site]]
url = "https://example.com/"
item_selector = ".x"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`,
			wantSub: "name",
		},
		{
			name: "missing url",
			body: `[[site]]
name = "x"
item_selector = ".x"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`,
			wantSub: "url",
		},
		{
			name: "missing item_selector",
			body: `[[site]]
name = "x"
url = "https://example.com/"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`,
			wantSub: "item_selector",
		},
		{
			name: "missing key_field",
			body: `[[site]]
name = "x"
url = "https://example.com/"
item_selector = ".x"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`,
			wantSub: "key_field",
		},
		{
			name: "key_field not declared in fields",
			body: `[[site]]
name = "x"
url = "https://example.com/"
item_selector = ".x"
key_field = "id"
[site.fields]
title = { selector = ".t" }
`,
			wantSub: "key_field",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, t.TempDir(), "browser.toml", tc.body)
			_, err := LoadConfig(path)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err message %q missing hint %q", err, tc.wantSub)
			}
		})
	}
}

// TestLoadConfigRejectsDuplicateSiteNames guards against a copy-paste
// mistake: two sites with the same name would make ExternalIDs ambiguous
// (Source = "browser:slack-saved" twice) and confuse the operator.
func TestLoadConfigRejectsDuplicateSiteNames(t *testing.T) {
	t.Parallel()

	body := `
[[site]]
name = "dup"
url = "https://a/"
item_selector = ".x"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }

[[site]]
name = "dup"
url = "https://b/"
item_selector = ".y"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`
	path := writeFile(t, t.TempDir(), "browser.toml", body)
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("err message %q missing 'duplicate'", err)
	}
}

// TestLoadConfigRejectsEmptyFile rejects a config that declares zero sites
// — a Plugin with nothing to scrape is almost certainly a misconfiguration
// (empty array left after deleting the sole rule). We surface this as a
// loud failure rather than silently returning an empty task list.
func TestLoadConfigRejectsEmptyFile(t *testing.T) {
	t.Parallel()

	path := writeFile(t, t.TempDir(), "browser.toml", "")
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

// TestLoadConfigMissingFileWraps confirms a missing file produces a
// readable error (not a typed sentinel — the OS error already carries
// fs.ErrNotExist and callers can match on that if they care).
func TestLoadConfigMissingFileWraps(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil {
		t.Fatalf("expected error")
	}
}

// TestLoadConfigRejectsNonHTTPScheme is the SSRF defence demanded by the
// design review's 🔴 #1: a `browser.toml` URL with `javascript:` /
// `file://` / `http://169.254.169.254/...` style scheme would let a
// malicious config drive `cmux browser goto` into arbitrary code
// execution or cloud-metadata exfiltration. The validator MUST reject
// any scheme other than http(s) at load time so a bad URL never reaches
// the driver.
func TestLoadConfigRejectsNonHTTPScheme(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		url  string
	}{
		{"javascript scheme", "javascript:alert(1)"},
		{"file scheme", "file:///etc/passwd"},
		{"data scheme", "data:text/html,<script>alert(1)</script>"},
		{"ftp scheme", "ftp://example.com/"},
		{"empty after parse", "://no-scheme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `[[site]]
name = "x"
url = "` + tc.url + `"
item_selector = ".x"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`
			path := writeFile(t, t.TempDir(), "browser.toml", body)
			_, err := LoadConfig(path)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), "scheme") && !strings.Contains(err.Error(), "url") {
				t.Errorf("err message %q missing scheme/url hint", err)
			}
		})
	}
}

// TestLoadConfigRejectsInvalidSiteName guards the contract documented in
// internal/source/source.go's Task.Source docstring: a sub-id'd source
// is "<plugin>:<sub-id>" and the dispatcher splits on the FIRST ':'.
// A site name containing ':' or whitespace would either break that
// split (producing an unexpected sub-id) or smuggle control characters
// into Source/RawMetadata. The validator rejects them at config load.
func TestLoadConfigRejectsInvalidSiteName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		siteName string
	}{
		{"contains colon", "foo:bar"},
		{"contains space", "foo bar"},
		{"contains tab", "foo\tbar"},
		{"contains newline", "foo\nbar"},
		{"contains slash", "foo/bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `[[site]]
name = ` + tomlString(tc.siteName) + `
url = "https://example.com/"
item_selector = ".x"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`
			path := writeFile(t, t.TempDir(), "browser.toml", body)
			_, err := LoadConfig(path)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), "name") {
				t.Errorf("err message %q missing `name` hint", err)
			}
		})
	}
}

// tomlString renders s as a TOML basic string with the bare-minimum
// escapes the validator's negative tests require (`"`, `\`, `\n`,
// `\t`). Avoids reaching for a full TOML encoder.
func tomlString(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\t':
			out = append(out, '\\', 't')
		default:
			out = append(out, []byte(string(r))...)
		}
	}
	out = append(out, '"')
	return string(out)
}

// TestLoadConfigAcceptsHTTPSchemes is the symmetric positive: http://
// and https:// must continue to work. Plain http is allowed because
// localhost / lab environments often serve over plain http; production
// is expected to use https. The narrower allowlist (https-only) belongs
// in a higher layer (allow_hosts policy file) the design review marks
// as a future extension.
func TestLoadConfigAcceptsHTTPSchemes(t *testing.T) {
	t.Parallel()

	for _, u := range []string{"http://example.com/", "https://example.com/"} {
		body := `[[site]]
name = "x"
url = "` + u + `"
item_selector = ".x"
key_field = "id"
[site.fields]
id = { selector = "[data-id]", attr = "data-id" }
`
			path := writeFile(t, t.TempDir(), "browser.toml", body)
			if _, err := LoadConfig(path); err != nil {
				t.Errorf("LoadConfig(%s): %v", u, err)
			}
	}
}
