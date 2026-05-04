package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type staticJournalProvider struct {
	snap JournalSnapshot
	err  error
}

func (s staticJournalProvider) JournalSnapshot(_ context.Context, _ string) (JournalSnapshot, error) {
	return s.snap, s.err
}

func newJournalServer(t *testing.T, prov JournalProvider) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Journal:           prov,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func sampleJournalSnapshot() JournalSnapshot {
	return JournalSnapshot{
		Date: "2026-05-04",
		Entries: []JournalEntry{
			{Time: "14:30", Source: "marunage", Summary: "Task completed"},
			{Time: "15:00", Source: "gmail", Summary: "Email processed"},
		},
	}
}

// 4. /api/journal returns entries list.
func TestJournalAPIHandler_ReturnsEntries(t *testing.T) {
	srv := newJournalServer(t, staticJournalProvider{snap: sampleJournalSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/journal", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	entries, ok := got["entries"].([]any)
	if !ok {
		t.Fatalf("entries missing or wrong type: %T", got["entries"])
	}
	if len(entries) != 2 {
		t.Errorf("entries len=%d; want 2", len(entries))
	}
}

// 5. /api/journal?date=YYYY-MM-DD passes date to the provider.
func TestJournalAPIHandler_DateFilterPassesDate(t *testing.T) {
	prov := &captureJournalProvider{snap: sampleJournalSnapshot()}
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Journal:           prov,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/journal?date=2026-05-04", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	if len(prov.dates) == 0 {
		t.Fatal("JournalSnapshot not called")
	}
	if prov.dates[0] != "2026-05-04" {
		t.Errorf("date passed=%q; want 2026-05-04", prov.dates[0])
	}
}

type captureJournalProvider struct {
	snap  JournalSnapshot
	dates []string
}

func (c *captureJournalProvider) JournalSnapshot(_ context.Context, date string) (JournalSnapshot, error) {
	c.dates = append(c.dates, date)
	return c.snap, nil
}

// GET /journal page returns 200.
func TestJournalHandler_Returns200(t *testing.T) {
	srv := newJournalServer(t, staticJournalProvider{snap: sampleJournalSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/journal", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type=%q; want text/html", ct)
	}
}

// /api/journal returns 500 when provider errors.
func TestJournalAPIHandler_ProviderErrorReturns500(t *testing.T) {
	srv := newJournalServer(t, staticJournalProvider{err: errJournalTestFailed})

	req := httptest.NewRequest(http.MethodGet, "/api/journal", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", w.Code)
	}
}

// /api/journal sets Cache-Control: no-store.
func TestJournalAPIHandler_SetsCacheControlNoStore(t *testing.T) {
	srv := newJournalServer(t, staticJournalProvider{snap: sampleJournalSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/journal", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

// /api/journal returns application/json content type.
func TestJournalAPIHandler_ReturnsJSON(t *testing.T) {
	srv := newJournalServer(t, staticJournalProvider{snap: sampleJournalSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/journal", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q; want application/json", ct)
	}
}

// /api/journal?date=invalid-format returns 400.
func TestJournalAPIHandler_InvalidDateReturns400(t *testing.T) {
	srv := newJournalServer(t, staticJournalProvider{snap: sampleJournalSnapshot()})

	for _, bad := range []string{"notadate", "2026-99-99", "2026/05/04", "yesterday"} {
		t.Run(bad, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/journal?date="+bad, nil)
			w := httptest.NewRecorder()
			srv.Routes().ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("date=%q: status=%d; want 400", bad, w.Code)
			}
		})
	}
}

// /api/journal?date= (empty) is OK — means today.
func TestJournalAPIHandler_EmptyDateIsOK(t *testing.T) {
	srv := newJournalServer(t, staticJournalProvider{snap: sampleJournalSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/journal", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("empty date: status=%d; want 200", w.Code)
	}
}

var errJournalTestFailed = journalTestSentinel("journal provider test failure")

type journalTestSentinel string

func (e journalTestSentinel) Error() string { return string(e) }
