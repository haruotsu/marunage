package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutes_Healthz pins the contract for the docs/operability check
// path: a GET /healthz must always return 200 with the literal "ok"
// body so external probes (loadbalancers, smoke tests) can use a single
// fixed assertion.
func TestRoutes_Healthz(t *testing.T) {
	srv := newTestServer(t)
	rec := doGet(t, srv, "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
		t.Errorf("body = %q; want %q", got, "ok")
	}
}

// TestRoutes_IndexHTML pins the dashboard placeholder.  PR-63 will
// replace the "marunage" body but PR-62's success criterion is that
// http://localhost:7777 returns html with "marunage" in it.
func TestRoutes_IndexHTML(t *testing.T) {
	srv := newTestServer(t)
	rec := doGet(t, srv, "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; want text/html prefix", ct)
	}
	if !strings.Contains(rec.Body.String(), "marunage") {
		t.Errorf("body missing %q\nbody:\n%s", "marunage", rec.Body.String())
	}
}

// TestRoutes_SecurityHeaders pins the headers PR-62 promises on every
// response so a future handler addition can't silently regress the
// baseline.
func TestRoutes_SecurityHeaders(t *testing.T) {
	srv := newTestServer(t)
	rec := doGet(t, srv, "/")

	cases := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	}
	for header, want := range cases {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q; want %q", header, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
		t.Errorf("Content-Security-Policy header missing; want a non-empty CSP")
	}
}

// TestRoutes_StaticServesEmbeddedCSS pins that the embed.FS-backed
// /static/ tree is wired up.  The fixture style.css is part of the
// build so a packaging regression (lost embed directive) shows up here
// instead of at deploy time.
func TestRoutes_StaticServesEmbeddedCSS(t *testing.T) {
	srv := newTestServer(t)
	rec := doGet(t, srv, "/static/style.css")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q; want text/css prefix", ct)
	}
}

// TestRoutes_TestPostRejectedWithoutCSRF mirrors the brief's E2E POST
// example: a POST without CSRF material must return 403.  The
// /test-post echo handler is registered only when the test-only opt
// is enabled so production never exposes it.
func TestRoutes_TestPostRejectedWithoutCSRF(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/test-post", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403 (CSRF should reject token-less POST)", rec.Code)
	}
}

// TestRoutes_EventsServesSSE confirms the /events endpoint is wired
// to the SSE handler.  The full streaming behaviour is exercised in
// sse_test.go; here we only need the wire-up + Content-Type.
func TestRoutes_EventsServesSSE(t *testing.T) {
	srv := newTestServer(t)
	httpSrv := httptest.NewServer(srv.Routes())
	t.Cleanup(httpSrv.Close)

	resp, err := http.Get(httpSrv.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q; want text/event-stream", got)
	}
}

// newTestServer constructs a Server with the deterministic CSRF token
// source, a tight SSE heartbeat, and the test-only /test-post route
// enabled.  Centralising the wiring keeps each test focused on one
// behaviour.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25_000_000, // 25ms in nanoseconds
		EnableTestRoutes:  true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func doGet(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}
