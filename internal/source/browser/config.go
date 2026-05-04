// config.go owns the on-disk browser.toml parser and validator. The brief
// (PR-200) wants 1 site = 1 rule entry: each [[site]] table declares one
// scrape target with selectors for the per-item DOM walk.
//
// The validator runs at load time so a misconfigured rule fails before the
// discovery loop ever drives a Scrape call. Wrapping every failure under a
// single typed sentinel (ErrInvalidConfig) lets callers branch with
// errors.Is while the wrapped message names the specific offence so an
// operator reading the log can fix it.
package browser

import (
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// ErrInvalidConfig is the sentinel for any browser.toml validation
// failure. Defined here (rather than in browser.go) so config-only
// callers (e.g. a future `marunage browser doctor` subcommand that
// checks rule files without instantiating a Plugin) need only this
// file's symbols.
var ErrInvalidConfig = errors.New("browser: invalid config")

// SiteConfig is the parsed form of one [[site]] table. Field names match
// the TOML keys with idiomatic Go casing — the on-disk shape is the
// source of truth for the operator, so renames here would force a
// browser.toml rewrite.
type SiteConfig struct {
	Name         string
	URL          string
	ItemSelector string
	KeyField     string
	Fields       map[string]FieldRule
}

// Config is the top-level browser.toml. It carries a Sites slice rather
// than a Sites map so declaration order is preserved — Plugin.List
// concatenates per-site results in the same order so a stable global
// ordering is recoverable from the file alone.
type Config struct {
	Sites []SiteConfig
}

// rawConfig and rawSite are the on-disk TOML shapes. They are kept
// private so downstream packages cannot reach into the unvalidated form
// — callers only ever see the validated *Config.
type rawConfig struct {
	Sites []rawSite `toml:"site"`
}

type rawSite struct {
	Name         string                  `toml:"name"`
	URL          string                  `toml:"url"`
	ItemSelector string                  `toml:"item_selector"`
	KeyField     string                  `toml:"key_field"`
	Fields       map[string]rawFieldRule `toml:"fields"`
}

// rawFieldRule mirrors FieldRule's field set so the validator's
// flatten step can convert with a plain type-cast and the staticcheck
// "S1016: use conversion" lint stays clean.
type rawFieldRule FieldRule

// LoadConfig reads path, parses the TOML body, and validates the result.
// A non-existent file produces a wrapped fs.ErrNotExist (callers that
// want to treat missing-config as "no sites" can branch on errors.Is);
// every other failure surfaces ErrInvalidConfig with a message naming
// the offending field.
func LoadConfig(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read browser config %s: %w", path, err)
	}
	return LoadConfigFromBytes(body)
}

// invalidSiteNameRune returns the first rune in name that falls outside
// the positive allowlist. The site name flows into Task.Source
// ("<plugin>:<sub-id>" — must split unambiguously on the first ':'),
// into RawMetadata["origin"] ("external/browser/<site>" — must read as
// one path segment), and may end up in downstream filenames. Rather
// than enumerate every dangerous code point (whitespace, format
// characters like NBSP/ZWSP, controls, separators), we restrict to a
// small allowlist of safe ASCII identifier characters: letters,
// digits, '-', '_', and '.'. Anything else is rejected at config
// load. Returns (0, false) when the name is acceptable.
func invalidSiteNameRune(name string) (rune, bool) {
	for _, r := range name {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return r, true
	}
	return 0, false
}

// validateScrapeURL is the SSRF / arbitrary-code-execution gate the
// design-review's 🔴 #1 finding demands: a `browser.toml` URL with a
// `javascript:` / `file://` / `data:` / `ftp://` scheme would let a
// malicious config drive `cmux browser goto` into JS execution or
// local-file disclosure. We accept only http(s) here so a bad URL
// never reaches the driver. Domain allowlisting (e.g. blocking
// 169.254.169.254) is a future-PR concern noted in the design's
// "今後の拡張余地".
func validateScrapeURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
		// Accepted. Plain http is allowed for localhost / lab use; the
		// production policy of "https only" belongs in a separate
		// `allow_hosts` policy file.
	case "":
		return fmt.Errorf("scheme is empty (must be http or https)")
	default:
		return fmt.Errorf("scheme %q not allowed (only http/https permitted)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("host is empty")
	}
	return nil
}

// LoadConfigFromBytes is the parsing core shared by LoadConfig and any
// in-memory caller. Centralising validation here means the on-disk and
// in-memory paths cannot drift in their accept/reject rules.
func LoadConfigFromBytes(body []byte) (*Config, error) {
	var raw rawConfig
	if err := toml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidConfig, err)
	}
	if len(raw.Sites) == 0 {
		return nil, fmt.Errorf("%w: at least one [[site]] table is required", ErrInvalidConfig)
	}
	cfg := &Config{Sites: make([]SiteConfig, 0, len(raw.Sites))}
	seen := map[string]struct{}{}
	for i, s := range raw.Sites {
		if s.Name == "" {
			return nil, fmt.Errorf("%w: site #%d missing required field `name`", ErrInvalidConfig, i+1)
		}
		if invalid, ok := invalidSiteNameRune(s.Name); ok {
			return nil, fmt.Errorf("%w: site name %q contains forbidden character %U",
				ErrInvalidConfig, s.Name, invalid)
		}
		if s.URL == "" {
			return nil, fmt.Errorf("%w: site %q missing required field `url`", ErrInvalidConfig, s.Name)
		}
		if err := validateScrapeURL(s.URL); err != nil {
			return nil, fmt.Errorf("%w: site %q url %q: %s", ErrInvalidConfig, s.Name, s.URL, err.Error())
		}
		if s.ItemSelector == "" {
			return nil, fmt.Errorf("%w: site %q missing required field `item_selector`", ErrInvalidConfig, s.Name)
		}
		if s.KeyField == "" {
			return nil, fmt.Errorf("%w: site %q missing required field `key_field`", ErrInvalidConfig, s.Name)
		}
		if _, ok := s.Fields[s.KeyField]; !ok {
			return nil, fmt.Errorf("%w: site %q `key_field` = %q must appear in [site.fields]",
				ErrInvalidConfig, s.Name, s.KeyField)
		}
		if _, dup := seen[s.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate site name %q", ErrInvalidConfig, s.Name)
		}
		seen[s.Name] = struct{}{}

		fields := make(map[string]FieldRule, len(s.Fields))
		for k, v := range s.Fields {
			fields[k] = FieldRule(v)
		}
		cfg.Sites = append(cfg.Sites, SiteConfig{
			Name:         s.Name,
			URL:          s.URL,
			ItemSelector: s.ItemSelector,
			KeyField:     s.KeyField,
			Fields:       fields,
		})
	}
	return cfg, nil
}
