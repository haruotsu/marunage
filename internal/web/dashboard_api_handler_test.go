package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type staticDashboardAPIProvider struct {
	snap DashboardSnapshot
	err  error
}

func (s staticDashboardAPIProvider) Snapshot(_ context.Context) (DashboardSnapshot, error) {
	return s.snap, s.err
}

var errDashboardAPITestFailed = errors.New("dashboard api provider test failure")

func sampleDashboardSnapshot() DashboardSnapshot {
	started := time.Date(2024, 1, 1, 11, 0, 0, 0, time.UTC)
	created := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	listed := time.Date(2024, 1, 1, 11, 30, 0, 0, time.UTC)
	return DashboardSnapshot{
		GeneratedAt: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Running: []DashboardRunning{
			{ID: 1, Source: "github", Title: "Fix bug", WS: "workspace:5", StartedAt: started, OutputPreview: "Running tests..."},
		},
		Pending: []DashboardPending{
			{ID: 2, Source: "slack", Title: "Review PR", Priority: 5, CreatedAt: created},
		},
		PendingCount: 5,
		Recent24h: DashboardRecent{
			DoneCount:    3,
			FailedCount:  1,
			SkippedCount: 2,
		},
		Sources: []DashboardSource{
			{Name: "github", AuthStatus: "authenticated", LastListedAt: listed},
		},
	}
}

func newDashboardAPIServer(t *testing.T, prov DashboardProvider) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Dashboard:         prov,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func TestDashboardAPIHandler_ReturnsJSON(t *testing.T) {
	srv := newDashboardAPIServer(t, staticDashboardAPIProvider{snap: sampleDashboardSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q; want application/json", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if got["generated_at"] == nil {
		t.Error("generated_at missing from response")
	}
	if got["pending_count"] != float64(5) {
		t.Errorf("pending_count=%v; want 5", got["pending_count"])
	}
	running, ok := got["running"].([]any)
	if !ok {
		t.Fatalf("running missing or wrong type: %T", got["running"])
	}
	if len(running) != 1 {
		t.Errorf("running len=%d; want 1", len(running))
	}
	row, ok := running[0].(map[string]any)
	if !ok {
		t.Fatalf("running[0] wrong type: %T", running[0])
	}
	if row["id"] != float64(1) {
		t.Errorf("running[0].id=%v; want 1", row["id"])
	}
	if row["source"] != "github" {
		t.Errorf("running[0].source=%v; want github", row["source"])
	}
	if row["ws"] != "workspace:5" {
		t.Errorf("running[0].ws=%v; want workspace:5", row["ws"])
	}
}

func TestDashboardAPIHandler_ReturnsPendingAndRecent(t *testing.T) {
	srv := newDashboardAPIServer(t, staticDashboardAPIProvider{snap: sampleDashboardSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	pending, ok := got["pending"].([]any)
	if !ok {
		t.Fatalf("pending missing or wrong type: %T", got["pending"])
	}
	if len(pending) != 1 {
		t.Errorf("pending len=%d; want 1", len(pending))
	}
	recent, ok := got["recent_24h"].(map[string]any)
	if !ok {
		t.Fatalf("recent_24h missing or wrong type: %T", got["recent_24h"])
	}
	if recent["done_count"] != float64(3) {
		t.Errorf("recent_24h.done_count=%v; want 3", recent["done_count"])
	}
	if recent["failed_count"] != float64(1) {
		t.Errorf("recent_24h.failed_count=%v; want 1", recent["failed_count"])
	}
	sources, ok := got["sources"].([]any)
	if !ok {
		t.Fatalf("sources missing or wrong type: %T", got["sources"])
	}
	if len(sources) != 1 {
		t.Errorf("sources len=%d; want 1", len(sources))
	}
}

func TestDashboardAPIHandler_SetsCacheControlNoStore(t *testing.T) {
	srv := newDashboardAPIServer(t, staticDashboardAPIProvider{snap: sampleDashboardSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

func TestDashboardAPIHandler_ProviderError(t *testing.T) {
	srv := newDashboardAPIServer(t, staticDashboardAPIProvider{err: errDashboardAPITestFailed})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), errDashboardAPITestFailed.Error()) {
		t.Errorf("response leaks raw error detail: %q", w.Body.String())
	}
}
