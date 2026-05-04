package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// notionAPIVersion is the Notion-Version header value the HTTP client sends
// on every request. The Notion API requires the header on every call; the
// supported versions are documented at
// https://developers.notion.com/reference/versioning. We pin a date here
// rather than tracking "latest" so a server-side change to defaults cannot
// silently change client behaviour.
const notionAPIVersion = "2022-06-28"

// defaultPageSize is the request page_size used when QueryOptions does not
// override it. Notion's max is 100; smaller values would only mean more
// round trips per List call.
const defaultPageSize = 100

// defaultTitleProperty is the Notion property name CreatePage emits when a
// caller did not configure one explicitly. Notion's default title-property
// label for a new database is "Name", which matches what the UI shows; a
// future WithTitleProperty option would let callers override.
const defaultTitleProperty = "Name"

// defaultBaseURL is the production Notion API endpoint. NewHTTPClient takes
// a baseURL parameter so tests can point at httptest.NewServer; callers in
// production glue should use NewDefaultHTTPClient (or pass this value).
const defaultBaseURL = "https://api.notion.com"

// HTTPClient is the production Client implementation. It is concurrency-safe
// in the standard library sense (http.Client is safe for concurrent use)
// and holds no caching state — each call hits Notion. Callers wanting
// per-process rate limiting should layer it on top.
type HTTPClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

// NewHTTPClient constructs an HTTPClient bound to baseURL with the supplied
// bearer token. Production callers in CLI glue (a future PR) typically pass
// http.DefaultClient and the production baseURL; tests inject an
// httptest.Server and its Client / URL.
func NewHTTPClient(httpClient *http.Client, baseURL, token string) *HTTPClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	// Trim trailing slash so callers that pass "https://api.notion.com/"
	// do not end up with a double slash in the request path.
	baseURL = strings.TrimRight(baseURL, "/")
	return &HTTPClient{httpClient: httpClient, baseURL: baseURL, token: token}
}

// QueryDatabase walks the documented `databases/{id}/query` endpoint and
// returns every page that matches opts. The walker follows has_more /
// next_cursor pagination internally so callers see one flat slice; this
// is a convenience the source.Plugin contract pays for at construction
// time (a future streaming variant could be added without breaking it).
func (c *HTTPClient) QueryDatabase(ctx context.Context, databaseID string, opts QueryOptions) ([]Page, error) {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	cursor := opts.StartCursor
	var out []Page
	for {
		body := map[string]any{"page_size": pageSize}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		if opts.OnOrAfter != "" {
			body["filter"] = map[string]any{
				"timestamp": "last_edited_time",
				"last_edited_time": map[string]any{
					"on_or_after": opts.OnOrAfter,
				},
			}
		}
		var resp queryDatabaseResponse
		path := "/v1/databases/" + databaseID + "/query"
		if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
			return nil, err
		}
		for _, raw := range resp.Results {
			out = append(out, raw.toPage(databaseID))
		}
		if !resp.HasMore || resp.NextCursor == "" {
			return out, nil
		}
		cursor = resp.NextCursor
	}
}

// UsersMe is the Notion `/v1/users/me` smoke probe. AuthStatus uses the
// success / 401 split to distinguish authenticated / revoked / expired.
// We do not bother decoding the user object — only the HTTP status drives
// the typed-error mapping.
func (c *HTTPClient) UsersMe(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/users/me", nil, nil)
}

// CreatePage posts to /v1/pages with a parent.database_id and a single
// title property. We default to property name "Name" — Notion's UI default
// for a fresh database — and let a future WithTitleProperty option
// override it without breaking the Adder contract.
func (c *HTTPClient) CreatePage(ctx context.Context, databaseID, title string) (Page, error) {
	body := map[string]any{
		"parent": map[string]any{"database_id": databaseID},
		"properties": map[string]any{
			defaultTitleProperty: map[string]any{
				"title": []map[string]any{{"text": map[string]any{"content": title}}},
			},
		},
	}
	var resp pageObject
	if err := c.do(ctx, http.MethodPost, "/v1/pages", body, &resp); err != nil {
		return Page{}, err
	}
	return resp.toPage(databaseID), nil
}

// UpdatePage patches /v1/pages/<id>. Today only the archived flag is
// honoured; properties / icon / cover would extend the body shape in a
// follow-up. The {"archived": true} call doubles as Notion's "delete" —
// see Plugin.Delete for the rationale.
func (c *HTTPClient) UpdatePage(ctx context.Context, pageID string, archived bool) error {
	body := map[string]any{"archived": archived}
	return c.do(ctx, http.MethodPatch, "/v1/pages/"+pageID, body, nil)
}

// do issues one Notion-API request, decoding the response into `out` (when
// non-nil) and turning any non-2xx into a typed error. Centralising the
// header / error-mapping logic here keeps the per-endpoint methods focused
// on the body shape they actually care about.
func (c *HTTPClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("notion: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("notion: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", notionAPIVersion)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notion: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return decodeErrorResponse(resp)
	}
	if out == nil {
		// Drain the body so the connection can be reused even if the
		// caller did not ask for the payload.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("notion: decode response: %w", err)
	}
	return nil
}

// decodeErrorResponse maps a non-2xx Notion response into a typed error.
// The Notion API documents an "error" object with fields {status, code,
// message}; we read code first because it distinguishes
// "expired_token" from generic "unauthorized" (both 401).
func decodeErrorResponse(resp *http.Response) error {
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Status  int    `json:"status"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &body)

	switch body.Code {
	case "expired_token":
		return fmt.Errorf("%w: %s", ErrTokenExpired, body.Message)
	case "unauthorized", "restricted_resource":
		return fmt.Errorf("%w: %s", ErrUnauthorized, body.Message)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Some Notion error payloads omit `code`; fall back to the
		// HTTP status so AuthStatus still gets a typed error to act on.
		return fmt.Errorf("%w: %s", ErrUnauthorized, body.Message)
	}
	return errors.New("notion: " + resp.Status + ": " + body.Message)
}

// queryDatabaseResponse is the wire-format envelope returned by
// /v1/databases/{id}/query. The pagination fields drive the cursor walk in
// QueryDatabase; results are individually mapped to Page values.
type queryDatabaseResponse struct {
	Results    []pageObject `json:"results"`
	HasMore    bool         `json:"has_more"`
	NextCursor string       `json:"next_cursor"`
}

// pageObject is the wire shape of a single Notion page. Only the fields the
// source layer needs are decoded; properties is left as a raw map so the
// title-extraction logic can iterate without an extra round of unmarshal.
type pageObject struct {
	ID             string                     `json:"id"`
	URL            string                     `json:"url"`
	Archived       bool                       `json:"archived"`
	LastEditedTime string                     `json:"last_edited_time"`
	Properties     map[string]json.RawMessage `json:"properties"`
}

// toPage lifts the wire object into a Page. The title is extracted from
// whichever property has type=="title"; this lets callers rename their
// title column ("Name" / "Title" / "Task" / ...) without breaking the
// plugin. databaseID is supplied by the caller because Notion's response
// does not echo it.
func (p pageObject) toPage(databaseID string) Page {
	return Page{
		ID:             p.ID,
		Title:          extractTitle(p.Properties),
		LastEditedTime: p.LastEditedTime,
		URL:            p.URL,
		Archived:       p.Archived,
		DatabaseID:     databaseID,
	}
}

// extractTitle finds the property whose type is "title" and concatenates
// its rich-text segments into one string. Notion stores a database row's
// title under whichever property the user labelled as the title column,
// so we cannot hard-code a name; the type field is the stable signal.
//
// Returns "" when no title property exists (e.g. a malformed page or one
// constructed by a non-database integration). We deliberately do not error
// here: an untitled page is a real upstream state, not a programmer bug,
// and the source layer should keep going.
func extractTitle(props map[string]json.RawMessage) string {
	type richText struct {
		PlainText string `json:"plain_text"`
		Text      *struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	type titleProperty struct {
		Type  string     `json:"type"`
		Title []richText `json:"title"`
	}
	for _, raw := range props {
		var tp titleProperty
		if err := json.Unmarshal(raw, &tp); err != nil {
			continue
		}
		if tp.Type != "title" {
			continue
		}
		var b strings.Builder
		for _, seg := range tp.Title {
			if seg.PlainText != "" {
				b.WriteString(seg.PlainText)
				continue
			}
			if seg.Text != nil {
				b.WriteString(seg.Text.Content)
			}
		}
		return b.String()
	}
	return ""
}
