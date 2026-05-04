package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Test list:
// 1. formatPrometheus includes marunage_tasks (gauge, no _total suffix)
// 2. formatPrometheus includes marunage_tasks_by_status with labels
// 3. formatPrometheus includes marunage_tasks_by_source with labels
// 4. formatPrometheus includes marunage_task_success_ratio
// 5. formatPrometheus includes marunage_task_avg_duration_seconds
// 6. GET /prometheus returns 200
// 7. GET /prometheus Content-Type is text/plain; version=0.0.4; charset=utf-8
// 8. GET /prometheus returns 500 on provider error
// 9. GET /metrics with Accept: text/plain returns Prometheus format
// 10. GET /metrics without Accept header returns HTML (existing behavior)
// 11. acceptsTextPlain returns false when Accept is */* (browser wildcard)
// 12. GET /prometheus sets Cache-Control: no-store
// 13. prometheusLabelEscape escapes only Prometheus-spec chars (\, ", \n)
// 14. acceptsTextPlain returns true when Accept contains text/plain alongside */*
// 15. formatPrometheus does not panic when ByStatus or BySource is nil
// 16. formatPrometheus escapes label values containing double-quotes and backslashes

// 1. formatPrometheus includes marunage_tasks (gauge, no _total suffix)
func TestFormatPrometheus_TasksTotal(t *testing.T) {
	snap := sampleMetricsSnapshot()
	out := formatPrometheus(snap)

	if !strings.Contains(out, "marunage_tasks 42") {
		t.Errorf("output missing marunage_tasks 42:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE marunage_tasks gauge") {
		t.Errorf("output missing TYPE line for marunage_tasks:\n%s", out)
	}
	// Ensure the old _total name is not present (would conflict with counter convention)
	if strings.Contains(out, "marunage_tasks_total") {
		t.Errorf("output must not use _total suffix for a gauge metric:\n%s", out)
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

// 4. formatPrometheus includes marunage_task_success_ratio
func TestFormatPrometheus_SuccessRate(t *testing.T) {
	snap := sampleMetricsSnapshot()
	out := formatPrometheus(snap)

	if !strings.Contains(out, "marunage_task_success_ratio 0.857") {
		t.Errorf("output missing success_ratio:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE marunage_task_success_ratio gauge") {
		t.Errorf("output missing TYPE line for marunage_task_success_ratio:\n%s", out)
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
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/prometheus", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
}

// 7. GET /prometheus Content-Type is text/plain; version=0.0.4; charset=utf-8
func TestPrometheusHandler_ContentType(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

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
	srv := newMetricsServer(t, staticMetricsProvider{err: errMetricsTestFailed})

	req := httptest.NewRequest(http.MethodGet, "/prometheus", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", w.Code)
	}
}

// 9. GET /metrics with Accept: text/plain returns Prometheus format
func TestMetricsHandler_AcceptTextPlain_ReturnsPrometheus(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

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
	if !strings.Contains(w.Body.String(), "marunage_tasks") {
		t.Errorf("body missing Prometheus metrics:\n%s", w.Body.String())
	}
}

// 10. GET /metrics without Accept: text/plain returns HTML (existing behavior)
func TestMetricsHandler_NoAcceptHeader_ReturnsHTML(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

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

// 11. acceptsTextPlain returns false when Accept is */* (browser wildcard)
func TestAcceptsTextPlain_WildcardOnly_ReturnsFalse(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "*/*")
	if acceptsTextPlain(req) {
		t.Error("acceptsTextPlain should return false for Accept: */*")
	}
}

// acceptsTextPlain returns true when Accept contains text/plain alongside */*
func TestAcceptsTextPlain_MixedWithWildcard_ReturnsTrue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "text/plain, */*")
	if !acceptsTextPlain(req) {
		t.Error("acceptsTextPlain should return true for Accept: text/plain, */*")
	}
}

// 12. GET /prometheus sets Cache-Control: no-store
func TestPrometheusHandler_SetsCacheControlNoStore(t *testing.T) {
	srv := newMetricsServer(t, staticMetricsProvider{snap: sampleMetricsSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/prometheus", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

// 13. prometheusLabelEscape escapes only Prometheus-spec chars (\, ", \n).
func TestPrometheusLabelEscape(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{`with"quote`, `with\"quote`},
		{`back\slash`, `back\\slash`},
		{"new\nline", `new\nline`},
		{"tab\there", "tab\there"}, // tab passes through unchanged per Prometheus spec
	}
	for _, tt := range tests {
		got := prometheusLabelEscape(tt.in)
		if got != tt.want {
			t.Errorf("prometheusLabelEscape(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

// formatPrometheus does not panic when ByStatus or BySource is nil.
func TestFormatPrometheus_NilMaps_NoPanic(t *testing.T) {
	snap := MetricsSnapshot{
		TotalTasks:  0,
		ByStatus:    nil,
		BySource:    nil,
		SuccessRate: 0,
		AvgDuration: 0,
	}
	out := formatPrometheus(snap)
	if !strings.Contains(out, "# TYPE marunage_tasks gauge") {
		t.Errorf("output missing TYPE line even with nil maps:\n%s", out)
	}
	if strings.Contains(out, "marunage_tasks_by_status{") {
		t.Errorf("output must have no label lines when ByStatus is nil:\n%s", out)
	}
	if strings.Contains(out, "marunage_tasks_by_source{") {
		t.Errorf("output must have no label lines when BySource is nil:\n%s", out)
	}
}

// formatPrometheus escapes label values containing double-quotes and backslashes.
func TestFormatPrometheus_LabelEscaping(t *testing.T) {
	snap := MetricsSnapshot{
		TotalTasks:  1,
		ByStatus:    map[string]int{`done"test`: 1},
		BySource:    map[string]int{`back\slash`: 1},
		SuccessRate: 1,
		AvgDuration: 1,
	}
	out := formatPrometheus(snap)
	if strings.Contains(out, `status="done"test"`) {
		t.Errorf("unescaped double-quote in label value breaks Prometheus format:\n%s", out)
	}
	if strings.Contains(out, `source="back\slash"`) && !strings.Contains(out, `source="back\\slash"`) {
		t.Errorf("backslash in label value must be escaped:\n%s", out)
	}
}
