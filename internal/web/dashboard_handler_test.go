package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// staticDashboardProvider is a fixed snapshot the handler tests can
// assert HTML rendering against without standing up the SQL fixture.
type staticDashboardProvider struct {
	snap DashboardSnapshot
	err  error
}

func (s staticDashboardProvider) Snapshot(_ context.Context) (DashboardSnapshot, error) {
	return s.snap, s.err
}

func newDashboardServer(t *testing.T, prov DashboardProvider) *Server {
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

func sampleSnapshot() DashboardSnapshot {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	return DashboardSnapshot{
		GeneratedAt: now,
		Running: []DashboardRunning{
			{ID: 1, Source: "markdown", Title: "running task", WS: "workspace:101", StartedAt: now.Add(-30 * time.Minute), OutputPreview: "preview text"},
		},
		Pending: []DashboardPending{
			{ID: 2, Source: "markdown", Title: "pending task", Priority: 5, CreatedAt: now.Add(-time.Hour)},
		},
		PendingCount: 7,
		Recent24h: DashboardRecent{
			DoneCount:    3,
			FailedCount:  1,
			SkippedCount: 2,
			BySource: []DashboardSourceCount{
				{Source: "markdown", Done: 3, Failed: 1, Skipped: 2},
			},
		},
		Sources: []DashboardSource{
			{Name: "markdown", AuthStatus: "authenticated", LastListedAt: now.Add(-15 * time.Minute)},
			{Name: "gmail", AuthStatus: "expired"},
		},
	}
}

func TestRoutes_IndexRendersDashboardPanels(t *testing.T) {
	srv := newDashboardServer(t, staticDashboardProvider{snap: sampleSnapshot()})

	rec := doGet(t, srv, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"running task",
		"workspace:101",
		"pending task",
		"7", // pending total
		"markdown",
		"expired",
		"authenticated",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestRoutes_PartialDashboardReturnsFragmentOnly(t *testing.T) {
	srv := newDashboardServer(t, staticDashboardProvider{snap: sampleSnapshot()})

	rec := doGet(t, srv, "/partials/dashboard")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") {
		t.Errorf("partial unexpectedly contains <html>; should be fragment only\n%s", body)
	}
	if !strings.Contains(body, "running task") {
		t.Errorf("partial body missing dashboard data\n%s", body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q; want no-store so polling never reads stale data", cc)
	}
}

func TestRoutes_PartialDashboardPropagatesProviderError(t *testing.T) {
	srv := newDashboardServer(t, staticDashboardProvider{err: errProviderTest})

	req := httptest.NewRequest(http.MethodGet, "/partials/dashboard", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500 when provider errors", rec.Code)
	}
}

// errProviderTest is a sentinel kept package-private so the
// staticDashboardProvider can simulate failures without exposing test
// scaffolding to production code.
var errProviderTest = providerSentinelError("dashboard test provider failure")

type providerSentinelError string

func (e providerSentinelError) Error() string { return string(e) }
