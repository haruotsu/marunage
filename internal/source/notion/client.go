// Package notion implements the Notion Discovery source plugin promised in
// PR-201 of docs/pr_split_plan.md. The plugin queries one Notion database
// per construction and exposes its pages through the source.Plugin contract
// (List / Since / Setup / AuthStatus, with optional Adder / Completer /
// Deleter for the bidirectional path).
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
	"strings"
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

// QueryOptions carries the filter / pagination knobs Plugin.List and
// Plugin.Since pass into the Client. Today only OnOrAfter is honoured;
// PageSize / StartCursor are reserved for a follow-up that paginates
// large databases without holding the whole result in memory.
type QueryOptions struct {
	// OnOrAfter, when non-empty, restricts the query to pages whose
	// last_edited_time is >= the supplied ISO-8601 timestamp. The Notion
	// API filter is `{"timestamp":"last_edited_time","last_edited_time":
	// {"on_or_after":"..."}}`. Empty means "no filter — return all pages".
	OnOrAfter string

	// PageSize / StartCursor are reserved for a follow-up that paginates;
	// today the http client always asks for Notion's max (100) and walks
	// cursors internally.
	PageSize    int
	StartCursor string
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

// fakeClient is the in-memory Client every test in this package uses.
// Lives in the production file (rather than a _test.go) so adapter / builtin
// tests in sibling files can build it without re-declaring the type. The
// behaviour mirrors the real Notion API closely enough that swapping
// fakeClient for httpClient in an integration test is a one-line change.
type fakeClient struct {
	pages       []Page
	queryErr    error
	usersMeErr  error
	createPage  Page // returned by CreatePage when no createErr set
	createErr   error
	updateErr   error
	updateCalls []updateCall
	createCalls []createCall
}

type updateCall struct {
	pageID   string
	archived bool
}

type createCall struct {
	databaseID string
	title      string
}

func (c *fakeClient) QueryDatabase(_ context.Context, _ string, opts QueryOptions) ([]Page, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	if opts.OnOrAfter == "" {
		out := make([]Page, len(c.pages))
		copy(out, c.pages)
		return out, nil
	}
	var out []Page
	for _, p := range c.pages {
		// Notion's filter is "on or after" — inclusive lower bound. Lex
		// compare works because LastEditedTime is fixed-width ISO-8601
		// with millisecond precision; this is the same property Plugin's
		// checkpoint comparison relies on.
		if strings.Compare(p.LastEditedTime, opts.OnOrAfter) >= 0 {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *fakeClient) UsersMe(_ context.Context) error {
	return c.usersMeErr
}

func (c *fakeClient) CreatePage(_ context.Context, databaseID, title string) (Page, error) {
	c.createCalls = append(c.createCalls, createCall{databaseID: databaseID, title: title})
	if c.createErr != nil {
		return Page{}, c.createErr
	}
	if c.createPage.ID != "" {
		return c.createPage, nil
	}
	// Default response: synthesise a deterministic page so tests that do
	// not pre-load createPage can still assert on a non-empty ExternalID.
	return Page{ID: "fake-" + title, Title: title, DatabaseID: databaseID}, nil
}

func (c *fakeClient) UpdatePage(_ context.Context, pageID string, archived bool) error {
	c.updateCalls = append(c.updateCalls, updateCall{pageID: pageID, archived: archived})
	return c.updateErr
}
