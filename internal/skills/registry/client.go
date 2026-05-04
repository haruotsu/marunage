package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultMaxBodyBytes caps any single body the client will ever read
// into memory. The cap protects the CLI from a hostile (or buggy)
// publisher returning a multi-gigabyte JSON document — the user can
// always rerun with a larger MaxBodyBytes if a legitimate manifest
// outgrows the default.
const DefaultMaxBodyBytes = int64(8 << 20) // 8 MiB

// DefaultTimeout is the per-request HTTP timeout the zero-value
// Client applies. Long enough to ride out a slow registry, short
// enough that a hung TCP session does not stall `marunage skills
// install` for minutes.
const DefaultTimeout = 30 * time.Second

// Doer abstracts *http.Client so tests can plug in an httptest server
// without an unused dependency on the real network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client speaks the registry protocol over HTTP(S). The zero value is
// not usable — callers must set BaseURL. HTTPClient defaults to a
// timeout-bounded *http.Client; tests inject a httptest server.
//
// AllowInsecure is the explicit opt-in switch for plain `http://`
// registries. The default refuses http to keep MITM-rewriting of the
// sha256 manifest itself out of scope (OpenClaw §11.1 #10). Tests
// flip it for httptest, and the CLI surfaces it via
// `--allow-insecure-registry`.
type Client struct {
	BaseURL       string
	HTTPClient    Doer
	MaxBodyBytes  int64
	UserAgent     string
	AllowInsecure bool
}

// ErrInsecureRegistry is returned when BaseURL uses a scheme other
// than http or https. The CLI surfaces it as an actionable "use https
// or set --insecure" message.
var ErrInsecureRegistry = errors.New("registry: BaseURL must be http or https")

// ErrIntegrity is returned by FetchTarball when the downloaded body's
// SHA256 does not match the expected digest from the manifest. The
// typed sentinel keeps the install path's "abort and clean up" branch
// explicit instead of relying on a string match.
var ErrIntegrity = errors.New("registry: tarball integrity check failed")

// ErrUpstream is the wrapper FetchIndex / FetchManifest / FetchTarball
// return for non-2xx responses, so callers can distinguish a registry-
// level failure (HTTP 500) from a transport-level one (DNS, dial
// timeout) without parsing strings.
var ErrUpstream = errors.New("registry: upstream returned non-2xx")

// ErrBodyTooLarge is returned when an upstream body exceeds
// MaxBodyBytes (or the package default). The typed sentinel keeps
// the size-cap branch grep-able and assertable without falling back
// to error-message string matches.
var ErrBodyTooLarge = errors.New("registry: upstream body exceeds size cap")

// FetchIndex retrieves and parses `<BaseURL>/index.json`.
func (c *Client) FetchIndex(ctx context.Context) (Index, error) {
	body, err := c.fetchJSON(ctx, IndexFileName)
	if err != nil {
		return Index{}, err
	}
	return ParseIndex(body)
}

// FetchManifest retrieves and parses
// `<BaseURL>/skills/<name>/manifest.json`.
//
// The skill name is path-segment-encoded so a stray slash (the
// publisher cannot, but a fuzz-tested user input might) cannot
// escape the `skills/` prefix.
func (c *Client) FetchManifest(ctx context.Context, name string) (Manifest, error) {
	if strings.TrimSpace(name) == "" {
		return Manifest{}, fmt.Errorf("registry: FetchManifest: empty name")
	}
	rel := "skills/" + url.PathEscape(name) + "/" + ManifestFileName
	body, err := c.fetchJSON(ctx, rel)
	if err != nil {
		return Manifest{}, err
	}
	return ParseManifest(body)
}

// FetchTarball downloads v.TarballURL, verifies its SHA256 against
// v.SHA256, and returns the verified body. On a digest mismatch the
// returned error wraps ErrIntegrity.
//
// We intentionally read the body fully into memory rather than
// streaming straight to disk: ExtractTarball needs to know the body
// is trustworthy before it walks any tar headers. Capping the read
// at MaxBodyBytes (default 8 MiB) keeps the memory footprint bounded
// even when an attacker hands us a "compressed bomb" content-length.
func (c *Client) FetchTarball(ctx context.Context, v Version) ([]byte, error) {
	if strings.TrimSpace(v.TarballURL) == "" {
		return nil, fmt.Errorf("registry: FetchTarball: empty tarball URL")
	}
	if strings.TrimSpace(v.SHA256) == "" {
		return nil, fmt.Errorf("registry: FetchTarball: empty SHA256")
	}
	if err := c.assertScheme(v.TarballURL); err != nil {
		return nil, err
	}
	body, err := c.get(ctx, v.TarballURL)
	if err != nil {
		return nil, err
	}
	gotSum := sha256.Sum256(body)
	got := hex.EncodeToString(gotSum[:])
	if !strings.EqualFold(got, v.SHA256) {
		return nil, fmt.Errorf("%w: want %s, got %s", ErrIntegrity, v.SHA256, got)
	}
	return body, nil
}

func (c *Client) fetchJSON(ctx context.Context, rel string) ([]byte, error) {
	full, err := c.resolve(rel)
	if err != nil {
		return nil, err
	}
	return c.get(ctx, full)
}

func (c *Client) resolve(rel string) (string, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return "", fmt.Errorf("registry: BaseURL is empty")
	}
	if err := c.assertScheme(c.BaseURL); err != nil {
		return "", err
	}
	base := strings.TrimRight(c.BaseURL, "/")
	rel = strings.TrimLeft(rel, "/")
	return base + "/" + rel, nil
}

func (c *Client) get(ctx context.Context, full string) ([]byte, error) {
	safe := redactURL(full)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("registry: build request %s: %w", safe, err)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	doer := c.HTTPClient
	if doer == nil {
		doer = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry: GET %s: %w", safe, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: GET %s: %d", ErrUpstream, safe, resp.StatusCode)
	}

	max := c.MaxBodyBytes
	if max <= 0 {
		max = DefaultMaxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("registry: read %s: %w", safe, err)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("%w: %s exceeded %d bytes", ErrBodyTooLarge, safe, max)
	}
	return body, nil
}

// redactURL returns u with any userinfo component (`user:token@`)
// stripped, so error messages, audit logs, and the state file never
// surface a credential the operator typed into `--registry`. Falls
// back to the literal input on parse failure (keeps error wrapping
// safe) and to the original on URLs without userinfo.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// assertScheme is the per-Client scheme guard. https is always
// accepted; http requires AllowInsecure. Anything else (file://,
// ftp://, ...) is rejected with ErrInsecureRegistry.
func (c *Client) assertScheme(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("registry: parse url %s: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if c.AllowInsecure {
			return nil
		}
		return fmt.Errorf("%w: http registries require AllowInsecure (CLI: --allow-insecure-registry)", ErrInsecureRegistry)
	}
	return fmt.Errorf("%w: %s", ErrInsecureRegistry, u.Scheme)
}
