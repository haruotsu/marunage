//go:build noweb

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// staticReviewProvider is a fixed ReviewProvider for handler tests.
type staticReviewProvider struct {
	snap ReviewSnapshot
	err  error
}

func (s staticReviewProvider) ReviewSnapshot(_ context.Context, _ store.ListFilter) (ReviewSnapshot, error) {
	return s.snap, s.err
}

func newReviewServer(t *testing.T, prov ReviewProvider) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Review:            prov,
		TaskOps:           &fakeTasks{},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

// sampleSkippedTasks returns a fixed dataset for review page tests.
func sampleSkippedTasks() []store.Task {
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	return []store.Task{
		{
			ID: 1, Source: "slack", Title: "PR review request",
			Status:         store.StatusSkipped,
			JudgmentReason: "rule 2: addressed to others",
			CreatedAt:      base,
		},
		{
			ID: 2, Source: "gmail", Title: "Newsletter",
			Status:         store.StatusSkipped,
			JudgmentReason: "rule 4: FYI broadcast",
			CreatedAt:      base.Add(24 * time.Hour),
		},
		{
			ID: 3, Source: "slack", Title: "Team update",
			Status:         store.StatusSkipped,
			JudgmentReason: "rule 4: FYI broadcast",
			CreatedAt:      base.Add(48 * time.Hour),
		},
	}
}

// 1. GET /review returns 200 and contains skipped task titles.
func TestReviewHandler_Returns200WithSkippedTasks(t *testing.T) {
	snap := ReviewSnapshot{Tasks: sampleSkippedTasks()}
	srv := newReviewServer(t, staticReviewProvider{snap: snap})

	req := httptest.NewRequest(http.MethodGet, "/review", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /review status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "PR review request") {
		t.Errorf("expected 'PR review request' in body; got %q", body)
	}
	if !strings.Contains(body, "Newsletter") {
		t.Errorf("expected 'Newsletter' in body; got %q", body)
	}
}

// 2. GET /review shows judgment_reason in the page.
func TestReviewHandler_ShowsJudgmentReason(t *testing.T) {
	snap := ReviewSnapshot{Tasks: sampleSkippedTasks()}
	srv := newReviewServer(t, staticReviewProvider{snap: snap})

	req := httptest.NewRequest(http.MethodGet, "/review", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "rule 2: addressed to others") {
		t.Errorf("judgment_reason not in body; got %q", body)
	}
}

// 3. GET /review shows a frequency report section with recurring patterns.
func TestReviewHandler_ShowsFrequencyReport(t *testing.T) {
	snap := ReviewSnapshot{Tasks: sampleSkippedTasks()}
	srv := newReviewServer(t, staticReviewProvider{snap: snap})

	req := httptest.NewRequest(http.MethodGet, "/review", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	body := w.Body.String()
	// "rule 4: FYI broadcast" appears twice and must show in the report.
	if !strings.Contains(body, "FYI broadcast") {
		t.Errorf("frequency report missing recurring reason; got %q", body)
	}
}

// 4. GET /review when provider errors returns 500.
func TestReviewHandler_ProviderErrorReturns500(t *testing.T) {
	srv := newReviewServer(t, staticReviewProvider{err: errTestReviewFailed})

	req := httptest.NewRequest(http.MethodGet, "/review", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on provider error; got %d", w.Code)
	}
}

// 5. GET /api/review/skipped returns a JSON array of skipped tasks.
func TestReviewAPIHandler_ReturnsJSONArray(t *testing.T) {
	snap := ReviewSnapshot{Tasks: sampleSkippedTasks()}
	srv := newReviewServer(t, staticReviewProvider{snap: snap})

	req := httptest.NewRequest(http.MethodGet, "/api/review/skipped", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/review/skipped status=%d; want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q; want application/json", ct)
	}
	var arr []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &arr); err != nil {
		t.Fatalf("JSON decode: %v; body=%q", err, w.Body.String())
	}
	if len(arr) != 3 {
		t.Errorf("array length=%d; want 3", len(arr))
	}
}

// 6. GET /review with nil ReviewProvider falls back to the index handler
// (Go's net/http ServeMux routes unregistered paths to GET /).
func TestReviewHandler_NilProviderFallsBackToIndex(t *testing.T) {
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		// Review intentionally nil — route not registered
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/review", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	// The route is not registered so ServeMux falls through to GET /
	// which returns 200 with the index page. The important thing is that
	// it does NOT panic or expose server internals.
	if w.Code != http.StatusOK {
		t.Errorf("status=%d; want 200 (index page fallback)", w.Code)
	}
}

// 7. GET /review sets Cache-Control: no-store.
func TestReviewHandler_SetsCacheControlNoStore(t *testing.T) {
	snap := ReviewSnapshot{Tasks: sampleSkippedTasks()}
	srv := newReviewServer(t, staticReviewProvider{snap: snap})

	req := httptest.NewRequest(http.MethodGet, "/review", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

// 8. GET /api/review/skipped sets Cache-Control: no-store.
func TestReviewAPIHandler_SetsCacheControlNoStore(t *testing.T) {
	snap := ReviewSnapshot{Tasks: sampleSkippedTasks()}
	srv := newReviewServer(t, staticReviewProvider{snap: snap})

	req := httptest.NewRequest(http.MethodGet, "/api/review/skipped", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

// captureFilterProvider records the ListFilter passed to ReviewSnapshot.
type captureFilterProvider struct {
	snap    ReviewSnapshot
	filters []store.ListFilter
}

func (c *captureFilterProvider) ReviewSnapshot(_ context.Context, f store.ListFilter) (ReviewSnapshot, error) {
	c.filters = append(c.filters, f)
	return c.snap, nil
}

// 9. GET /review?since=7d passes CreatedAfter to the provider.
func TestReviewHandler_SinceQueryPassesCreatedAfter(t *testing.T) {
	prov := &captureFilterProvider{snap: ReviewSnapshot{Tasks: sampleSkippedTasks()}}
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Review:            prov,
		TaskOps:           &fakeTasks{},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/review?since=7d", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	if len(prov.filters) == 0 {
		t.Fatal("ReviewSnapshot not called")
	}
	if prov.filters[0].CreatedAfter.IsZero() {
		t.Errorf("CreatedAfter is zero; want non-zero for ?since=7d")
	}
}

// 10. GET /api/review/skipped?since=7d passes CreatedAfter to the provider.
func TestReviewAPIHandler_SinceQueryPassesCreatedAfter(t *testing.T) {
	prov := &captureFilterProvider{snap: ReviewSnapshot{Tasks: sampleSkippedTasks()}}
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Review:            prov,
		TaskOps:           &fakeTasks{},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/review/skipped?since=7d", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	if len(prov.filters) == 0 {
		t.Fatal("ReviewSnapshot not called")
	}
	if prov.filters[0].CreatedAfter.IsZero() {
		t.Errorf("CreatedAfter is zero; want non-zero for ?since=7d")
	}
}

// errTestReviewFailed is the sentinel error for review provider test failures.
var errTestReviewFailed = &testReviewError{}

type testReviewError struct{}

func (*testReviewError) Error() string { return "review provider test failure" }
