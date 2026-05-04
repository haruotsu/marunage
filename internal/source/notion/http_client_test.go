package notion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHTTPClientQueryDatabaseSendsAuthAndVersion locks in the request shape
// the production client emits. The Notion API requires Authorization: Bearer
// <token> AND a Notion-Version header; missing either yields a confusing
// 400/401 from upstream rather than a typed error, so we assert both here.
func TestHTTPClientQueryDatabaseSendsAuthAndVersion(t *testing.T) {
	t.Parallel()

	var (
		gotAuth    string
		gotVersion string
		gotPath    string
		gotMethod  string
		gotBody    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("Notion-Version")
		gotPath = r.URL.Path
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"results":[],"has_more":false}`))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "secret_xyz")
	if _, err := c.QueryDatabase(context.Background(), "db-123", QueryOptions{}); err != nil {
		t.Fatalf("QueryDatabase: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q", gotMethod)
	}
	if gotPath != "/v1/databases/db-123/query" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer secret_xyz" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotVersion == "" {
		t.Errorf("Notion-Version header missing")
	}
	if !strings.Contains(gotBody, "page_size") {
		t.Errorf("body missing page_size, got %q", gotBody)
	}
}

// TestHTTPClientQueryDatabaseSendsOnOrAfterFilter — when QueryOptions.OnOrAfter
// is set, the request body must include Notion's documented filter shape so
// the server prunes by last_edited_time. Without this assertion we could
// silently drop the filter and grow the response unboundedly.
func TestHTTPClientQueryDatabaseSendsOnOrAfterFilter(t *testing.T) {
	t.Parallel()

	var bodyDecoded map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&bodyDecoded)
		_, _ = w.Write([]byte(`{"results":[],"has_more":false}`))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "secret")
	_, err := c.QueryDatabase(context.Background(), "db", QueryOptions{OnOrAfter: "2025-01-01T00:00:00.000Z"})
	if err != nil {
		t.Fatalf("QueryDatabase: %v", err)
	}
	filter, _ := bodyDecoded["filter"].(map[string]any)
	if filter == nil {
		t.Fatalf("body missing filter: %+v", bodyDecoded)
	}
	if filter["timestamp"] != "last_edited_time" {
		t.Errorf("filter.timestamp = %v", filter["timestamp"])
	}
	editTime, _ := filter["last_edited_time"].(map[string]any)
	if editTime["on_or_after"] != "2025-01-01T00:00:00.000Z" {
		t.Errorf("on_or_after = %v", editTime["on_or_after"])
	}
}

// TestHTTPClientQueryDatabaseParsesPages decodes a representative Notion
// payload into Page values. The test pins the title-property extraction
// (Notion stores the title under whichever property has type="title") and
// asserts the URL / archived / last_edited_time pass through.
func TestHTTPClientQueryDatabaseParsesPages(t *testing.T) {
	t.Parallel()

	const body = `{
	  "results": [
	    {
	      "id": "11111111-1111-1111-1111-111111111111",
	      "object": "page",
	      "url": "https://notion.so/page-1",
	      "archived": false,
	      "last_edited_time": "2025-06-01T00:00:00.000Z",
	      "properties": {
	        "Status": {"type": "select"},
	        "Name": {"type": "title", "title": [{"plain_text": "hello"}, {"plain_text": " world"}]}
	      }
	    },
	    {
	      "id": "22222222-2222-2222-2222-222222222222",
	      "object": "page",
	      "url": "https://notion.so/page-2",
	      "archived": true,
	      "last_edited_time": "2025-06-02T00:00:00.000Z",
	      "properties": {
	        "Title": {"type": "title", "title": [{"plain_text": "archived item"}]}
	      }
	    }
	  ],
	  "has_more": false
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "secret")
	got, err := c.QueryDatabase(context.Background(), "db", QueryOptions{})
	if err != nil {
		t.Fatalf("QueryDatabase: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].ID != "11111111-1111-1111-1111-111111111111" || got[0].Title != "hello world" {
		t.Errorf("page[0] = %+v", got[0])
	}
	if got[0].URL != "https://notion.so/page-1" || got[0].Archived {
		t.Errorf("page[0] mapping = %+v", got[0])
	}
	if got[1].Title != "archived item" || !got[1].Archived {
		t.Errorf("page[1] = %+v", got[1])
	}
}

// TestHTTPClientQueryDatabaseFollowsCursor — the production client must walk
// has_more / next_cursor pagination so a database with thousands of pages
// returns one flat slice. Without this assertion an early implementation
// could silently truncate at 100 entries (Notion's max page size).
func TestHTTPClientQueryDatabaseFollowsCursor(t *testing.T) {
	t.Parallel()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"results":[{"id":"a","object":"page","properties":{"N":{"type":"title","title":[{"plain_text":"a"}]}}}],"has_more":true,"next_cursor":"cur-1"}`))
			return
		}
		if body["start_cursor"] != "cur-1" {
			t.Errorf("second call missing start_cursor=cur-1, body=%+v", body)
		}
		_, _ = w.Write([]byte(`{"results":[{"id":"b","object":"page","properties":{"N":{"type":"title","title":[{"plain_text":"b"}]}}}],"has_more":false}`))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "secret")
	got, err := c.QueryDatabase(context.Background(), "db", QueryOptions{})
	if err != nil {
		t.Fatalf("QueryDatabase: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("pagination broken: %+v (calls=%d)", got, calls)
	}
}

// TestHTTPClientUsersMeMapsUnauthorized — the smoke probe AuthStatus uses
// must surface 401 with code "unauthorized" as ErrUnauthorized.
func TestHTTPClientUsersMeMapsUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"object":"error","status":401,"code":"unauthorized","message":"API token is invalid."}`))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "bad")
	err := c.UsersMe(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

// TestHTTPClientUsersMeMapsTokenExpired — Notion's OAuth path returns a
// distinct 401 code for expired tokens so AuthStatus can answer "expired"
// rather than "revoked". The mapping is keyed off the documented error code.
func TestHTTPClientUsersMeMapsTokenExpired(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"object":"error","status":401,"code":"expired_token","message":"Token expired"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "expired")
	err := c.UsersMe(context.Background())
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err = %v, want ErrTokenExpired", err)
	}
}

// TestHTTPClientUsersMeOK — happy path for AuthStatus authenticated branch.
func TestHTTPClientUsersMeOK(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users/me" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"object":"user","id":"abc"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "good")
	if err := c.UsersMe(context.Background()); err != nil {
		t.Fatalf("UsersMe: %v", err)
	}
}

// TestHTTPClientCreatePagePostsToPagesEndpoint — Adder path: POST /v1/pages
// with parent.database_id and a single title property. We synthesise the
// title property name "Name" by default; a future PR that adds
// WithTitleProperty would override it.
func TestHTTPClientCreatePagePostsToPagesEndpoint(t *testing.T) {
	t.Parallel()

	var (
		gotPath string
		gotBody map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"new-page","object":"page","url":"https://notion.so/new-page","properties":{"Name":{"type":"title","title":[{"plain_text":"hi"}]}}}`))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "tok")
	pg, err := c.CreatePage(context.Background(), "db-123", "hi")
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if gotPath != "/v1/pages" {
		t.Errorf("path = %q", gotPath)
	}
	parent, _ := gotBody["parent"].(map[string]any)
	if parent["database_id"] != "db-123" {
		t.Errorf("parent = %+v", parent)
	}
	if pg.ID != "new-page" || pg.Title != "hi" {
		t.Errorf("returned page = %+v", pg)
	}
}

// TestHTTPClientUpdatePagePatchesArchived — Completer / Deleter path: PATCH
// /v1/pages/<id> with {"archived": true}. Neither Complete nor Delete have
// a permanent delete on Notion, so this is the user-visible "remove from
// the active list" call.
func TestHTTPClientUpdatePagePatchesArchived(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		gotBody   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"page-9","object":"page","archived":true}`))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(srv.Client(), srv.URL, "tok")
	if err := c.UpdatePage(context.Background(), "page-9", true); err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q", gotMethod)
	}
	if gotPath != "/v1/pages/page-9" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["archived"] != true {
		t.Errorf("body = %+v", gotBody)
	}
}
