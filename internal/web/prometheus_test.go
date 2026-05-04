package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Test list:
// 1. formatPrometheus includes marunage_tasks_total
// 2. formatPrometheus includes marunage_tasks_by_status with labels
// 3. formatPrometheus includes marunage_tasks_by_source with labels
// 4. formatPrometheus includes marunage_task_success_rate
// 5. formatPrometheus includes marunage_task_avg_duration_seconds
// 6. GET /prometheus returns 200
// 7. GET /prometheus Content-Type is text/plain; version=0.0.4; charset=utf-8
// 8. GET /prometheus returns 500 on provider error
// 9. GET /metrics with Accept: text/plain returns Prometheus format
// 10. GET /metrics without Accept header returns HTML (existing behavior)

func newPrometheusServer(t *testing.T, prov MetricsProvider) *Server {
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

// 1. formatPrometheus includes marunage_tasks_total
func TestFormatPrometheus_TasksTotal(t *testing.T) {
	snap := sampleMetricsSnapshot()
	out := formatPrometheus(snap)

	if !strings.Contains(out, "marunage_tasks_total 42") {
		t.Errorf("output missing marunage_tasks_total 42:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE marunage_tasks_total gauge") {
		t.Errorf("output missing TYPE line for marunage_tasks_total:\n%s", out)
	}
}

// 2. formatPrometheus includes marunage_tasks_by_status with labels
func TestFormatPrometheus_TasksByStatus(t *testing.T) {
	snap := sampleMetricsSnapshot()
	out := formatPrometheus(snap)

	if !strings.Contains(out, `marunage_tasks_by_status{status="done"} 30`) {
		t.Errorf("output missing done status:\n%s", out)
	}
	if !strings.Contains(out, `marunage_tasks_by_status{status="failed"} 5`) {
		t.Errorf("output missing failed status:\n%s", out)
	}
	if !strings.Contains(out, `marunage_tasks_by_status{status="pending"} 7`) {
		t.Errorf("output missing pending status:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE marunage_tasks_by_status gauge") {
		t.Errorf("output missing TYPE line for marunage_tasks_by_status:\n%s", out)
	}
}

// 3. formatPrometheus includes marunage_tasks_by_source with labels
func TestFormatPrometheus_TasksBySource(t *testing.T) {
	snap := sampleMetricsSnapshot()
	out := formatPrometheus(snap)

	if !strings.Contains(out, `marunage_tasks_by_source{source="gmail"} 10`) {
		t.Errorf("output missing gmail source:\n%s", out)
	}
	if !strings.Contains(out, `marunage_tasks_by_source{source="slack"} 15`) {
		t.Errorf("output missing slack source:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE marunage_tasks_by_source gauge") {
		t.Errorf("output missing TYPE line for marunage_tasks_by_source:\n%s", out)
	}
}

// 4. formatPrometheus includes marunage_task_success_rate
func TestFormatPrometheus_SuccessRate(t *testing.T) {
	snap := sampleMetricsSnapshot()
	out := formatPrometheus(snap)

	if !strings.Contains(out, "marunage_task_success_rate 0.857") {
		t.Errorf("output missing success_rate:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE marunage_task_success_rate gauge") {
		t.Errorf("output missing TYPE line for marunage_task_success_rate:\n%s", out)
	}
}

// 5. formatPrometheus includes marunage_task_avg_duration_seconds
func TestFormatPrometheus_AvgDuration(t *testing.T) {
	snap := sampleMetricsSnapshot()
	out := formatPrometheus(snap)

	if !strings.Contains(out, "marunage_task_avg_duration_seconds 320.5") {
		t.Errorf("output missing avg_duration_seconds:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE marunage_task_avg_duration_seconds gauge") {
		t.Errorf("output missing TYPE line for marunage_task_avg_duration_seconds:\n%s", out)
	}
}

// 6. GET /prometheus returns 200
func TestPrometheusHandler_Returns200(t *testing.T) {
	srv := newPrometheusServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/prometheus", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
}

// 7. GET /prometheus Content-Type is text/plain; version=0.0.4; charset=utf-8
func TestPrometheusHandler_ContentType(t *testing.T) {
	srv := newPrometheusServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/prometheus", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	want := "text/plain; version=0.0.4; charset=utf-8"
	if ct != want {
		t.Errorf("Content-Type=%q; want %q", ct, want)
	}
}

// 8. GET /prometheus returns 500 on provider error
func TestPrometheusHandler_ProviderErrorReturns500(t *testing.T) {
	srv := newPrometheusServer(t, staticMetricsProvider{err: errMetricsTestFailed})

	req := httptest.NewRequest(http.MethodGet, "/prometheus", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", w.Code)
	}
}

// 9. GET /metrics with Accept: text/plain returns Prometheus format
func TestMetricsHandler_AcceptTextPlain_ReturnsPrometheus(t *testing.T) {
	srv := newPrometheusServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "text/plain")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	want := "text/plain; version=0.0.4; charset=utf-8"
	if ct != want {
		t.Errorf("Content-Type=%q; want %q", ct, want)
	}
	if !strings.Contains(w.Body.String(), "marunage_tasks_total") {
		t.Errorf("body missing Prometheus metrics:\n%s", w.Body.String())
	}
}

// 10. GET /metrics without Accept: text/plain returns HTML (existing behavior)
func TestMetricsHandler_NoAcceptHeader_ReturnsHTML(t *testing.T) {
	srv := newPrometheusServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type=%q; want text/html", ct)
	}
}
