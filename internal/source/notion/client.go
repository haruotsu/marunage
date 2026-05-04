// Package notion implements the Notion Discovery source plugin promised in
// PR-201 of docs/pr_split_plan.md. The plugin queries one Notion database
// per construction and exposes its pages through the source.Plugin contract
// (List / Since / Setup / AuthStatus, with optional Adder / Completer /
// Deleter for the bidirectional path).
//
// Non-goals (per docs/requirement.md §3.7 and §5.12):
//
//   - Notion is a Source — never an interactive Channel. The plugin does
//     NOT consume webhooks, post comments, or otherwise turn Notion into
//     a two-way conversation surface.
//   - Notion is NOT the SSOT (§3.8). Markdown remains the SSOT;
//     Complete and Delete archive the Notion page so the user-visible
//     state stays consistent, but the queue's authoritative state lives
//     elsewhere. Adding a Notion-driven reconciliation that overrides
//     queue state is explicitly out of scope.
//
// The package is split into:
//
//   - client.go  — Client interface + Page / QueryOptions value types and a
//     test-only fakeClient. The interface is the seam so unit tests never
//     need a real httptest server (see PR-201 brief: "Notion API 通信は
//     interface 抽象化 + fake で単体テスト").
//   - http_client.go — production HTTP implementation of Client.
//   - notion.go  — Plugin core (List / Since / Setup / AuthStatus).
//   - adapter.go — bridge to source.Plugin (matches markdown's adapter
//     shape so PR-71 dispatch stays uniform across sources).
//   - builtin.go — RegisterBuiltin + go:embed plugin.toml.
//
// Why a separate Client interface rather than calling the official SDK
// directly: the Notion SDKs are heavyweight and bring transitive deps the
// rest of marunage does not need. A small interface keeps testing trivial
// (fake everything) and lets a future PR swap in the SDK without touching
// the Plugin's logic.
package notion

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrUnauthorized is the typed sentinel surfaced by Client implementations
// when Notion returns a 401-equivalent (Bearer token revoked / unknown).
// Plugin.AuthStatus maps this to source.AuthRevoked. Callers branch on
// errors.Is rather than parsing the underlying string so a future change
// to the wrapped error chain stays transparent.
var ErrUnauthorized = errors.New("notion: unauthorized (token revoked)")

// ErrTokenExpired is the typed sentinel surfaced when an OAuth access token
// has aged past its TTL. Distinct from ErrUnauthorized so AuthStatus can
// return AuthExpired (refresh) rather than AuthRevoked (re-grant).
var ErrTokenExpired = errors.New("notion: token expired")

// ErrRateLimited is the typed sentinel for 429 / "rate_limited" responses.
// Wrapped by RateLimitedError so callers branching on errors.Is still get
// the structured Retry-After delay via errors.As.
var ErrRateLimited = errors.New("notion: rate limited")

// RateLimitedError carries the Retry-After hint that 429 responses include
// (or a conservative default when the header is absent). Daemons running
// the Discovery loop should sleep RetryAfter before retrying so they
// neither hammer Notion nor retry too eagerly.
type RateLimitedError struct {
	// RetryAfter is the documented Retry-After delay parsed from the
	// response header, or a conservative 5s default when no header was
	// returned (or it carried an unsupported HTTP-date form).
	RetryAfter time.Duration
	// Message is Notion's own human-readable explanation. Safe to log:
	// the API does not include credential material here.
	Message string
}

// Error returns the typed-error string. Wrapping ErrRateLimited via the
// `%w` verb means errors.Is(err, ErrRateLimited) keeps working for
// callers that only need the categorical bucket.
func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("%s: %s (retry after %s)", ErrRateLimited.Error(), e.Message, e.RetryAfter)
}

// Unwrap lets errors.Is traverse to ErrRateLimited so the existing typed-
// sentinel branch in callers stays correct.
func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// Page is the source-side neutral view of one Notion page returned by the
// `databases/{id}/query` endpoint. We deliberately project only the fields
// the source layer needs; a future PR that wants properties beyond title
// can extend this struct without changing the Client contract.
//
// LastEditedTime stays a string (ISO-8601 with millis, exactly as Notion
// returns it) so the Since checkpoint round-trips byte-for-byte: parsing
// to time.Time and reformatting would risk losing trailing zeros that the
// API filter compares lexicographically.
type Page struct {
	ID             string
	Title          string
	LastEditedTime string
	URL            string
	Archived       bool
	DatabaseID     string
}

// QueryOptions carries the filter knobs Plugin.List and Plugin.Since pass
// into the Client. The HTTP client always asks for Notion's max page size
// (100) and walks cursors internally, so callers do not need pagination
// knobs here today; if a future PR needs streaming or partial reads, it
// can extend this struct.
type QueryOptions struct {
	// OnOrAfter, when non-empty, restricts the query to pages whose
	// last_edited_time is >= the supplied ISO-8601 timestamp. The Notion
	// API filter is `{"timestamp":"last_edited_time","last_edited_time":
	// {"on_or_after":"..."}}`. Empty means "no filter — return all pages".
	OnOrAfter string
}

// Client is the seam between Plugin logic and the Notion HTTP API. The
// production implementation lives in http_client.go; tests construct
// fakeClient values directly. Adding a new endpoint means adding a new
// method here so the interface always documents what the Plugin actually
// reaches for upstream.
type Client interface {
	// QueryDatabase returns every page in the supplied Notion database id
	// matching opts. Implementations are expected to walk Notion's cursor
	// pagination internally so callers see one flat slice.
	QueryDatabase(ctx context.Context, databaseID string, opts QueryOptions) ([]Page, error)

	// UsersMe is the Notion `/v1/users/me` smoke probe. Returns nil on a
	// 200, ErrUnauthorized on a 401, ErrTokenExpired on a "token expired"
	// 401-equivalent. AuthStatus uses this to distinguish "configured but
	// dead" from "configured and live".
	UsersMe(ctx context.Context) error

	// CreatePage inserts a new page in the supplied database with the
	// given title (and optional archive state). Used by Adder.
	CreatePage(ctx context.Context, databaseID, title string) (Page, error)

	// UpdatePage mutates an existing page's archived flag (and, in the
	// future, properties). Used by Completer / Deleter; Notion has no
	// "permanent delete" so archive is the user-visible delete.
	UpdatePage(ctx context.Context, pageID string, archived bool) error
}
