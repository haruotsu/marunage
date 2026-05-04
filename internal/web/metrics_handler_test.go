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

type staticMetricsProvider struct {
	snap MetricsSnapshot
	err  error
}

func (s staticMetricsProvider) Snapshot(_ context.Context) (MetricsSnapshot, error) {
	return s.snap, s.err
}

func newMetricsServer(t *testing.T, prov MetricsProvider) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Metrics:           prov,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func sampleMetricsSnapshot() MetricsSnapshot {
	return MetricsSnapshot{
		TotalTasks:  42,
		ByStatus:    map[string]int{"done": 30, "failed": 5, "pending": 7},
		BySource:    map[string]int{"gmail": 10, "slack": 15, "github": 17},
		SuccessRate: 0.857,
		AvgDuration: 320.5,
		DailyCounts: []MetricsDailyCount{
			{Date: "2026-05-01", Done: 5, Failed: 1},
			{Date: "2026-05-02", Done: 8, Failed: 2},
		},
	}
}

// 1. /api/metrics returns total_tasks and daily_counts.
func TestMetricsAPIHandler_ReturnsTotalAndDailyCounts(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if got["total_tasks"] != float64(42) {
		t.Errorf("total_tasks=%v; want 42", got["total_tasks"])
	}
	daily, ok := got["daily_counts"].([]any)
	if !ok {
		t.Fatalf("daily_counts missing or wrong type: %T", got["daily_counts"])
	}
	if len(daily) != 2 {
		t.Errorf("daily_counts len=%d; want 2", len(daily))
	}
}

// 2. /api/metrics returns by_source breakdown.
func TestMetricsAPIHandler_ReturnsBySource(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	bySource, ok := got["by_source"].(map[string]any)
	if !ok {
		t.Fatalf("by_source missing or wrong type: %T", got["by_source"])
	}
	if bySource["gmail"] != float64(10) {
		t.Errorf("by_source.gmail=%v; want 10", bySource["gmail"])
	}
	if bySource["slack"] != float64(15) {
		t.Errorf("by_source.slack=%v; want 15", bySource["slack"])
	}
}

// 3. /api/metrics returns success_rate and avg_duration_seconds.
func TestMetricsAPIHandler_ReturnsSuccessRateAndAvgDuration(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if got["success_rate"] == nil {
		t.Error("success_rate missing from response")
	}
	if got["avg_duration_seconds"] == nil {
		t.Error("avg_duration_seconds missing from response")
	}
	if got["success_rate"].(float64) < 0.8 || got["success_rate"].(float64) > 1.0 {
		t.Errorf("success_rate=%v; want ~0.857", got["success_rate"])
	}
}

// 4. /api/metrics returns 500 when provider errors.
func TestMetricsAPIHandler_ProviderErrorReturns500(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{err: errMetricsTestFailed})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), errMetricsTestFailed.Error()) {
		t.Errorf("response leaks raw error detail: %q", w.Body.String())
	}
}

// 5. GET /metrics page returns 200 and contains metric data.
func TestMetricsHandler_Returns200WithData(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type=%q; want text/html", ct)
	}
}

// 6. GET /metrics sets Cache-Control: no-store.
func TestMetricsHandler_SetsCacheControlNoStore(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

// 7. /api/metrics sets Cache-Control: no-store.
func TestMetricsAPIHandler_SetsCacheControlNoStore(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

// 8. /api/metrics returns application/json content type.
func TestMetricsAPIHandler_ReturnsJSON(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q; want application/json", ct)
	}
}

// GET /metrics with provider error still returns 200 with error banner
// (matches the dashboard.go graceful-degradation pattern — provider errors
// degrade to a banner rather than a hard 500 so the page remains accessible).
func TestMetricsHandler_ProviderErrorRendersErrorBanner(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{err: errMetricsTestFailed})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200 (graceful degradation)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unavailable") {
		t.Errorf("error banner missing 'unavailable' in body: %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), errMetricsTestFailed.Error()) {
		t.Errorf("error banner leaks raw error: %q", w.Body.String())
	}
}

var errMetricsTestFailed = metricsTestSentinel("metrics provider test failure")

type metricsTestSentinel string

func (e metricsTestSentinel) Error() string { return string(e) }
