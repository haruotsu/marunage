package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// TestRoutes_CSPHardenings pins the CSP defence-in-depth additions
// the security review flagged: object-src 'none' blocks legacy
// plugin embeds, and form-action 'self' stops a compromised page
// from POSTing form data to a third-party origin.
func TestRoutes_CSPHardenings(t *testing.T) {
	srv := newTestServer(t)
	rec := doGet(t, srv, "/")

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"object-src 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q\nfull CSP:\n%s", want, csp)
		}
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

// TestRoutes_TestPostAcceptedWithCSRF is the brief's positive
// counterpart (#4 in the test list): a POST that carries the matching
// cookie + header is forwarded through the full middleware chain
// (security headers → access log → CSRF → mux) to the dummy handler
// and returns 200.  Without this test a regression that breaks the
// middleware order silently leaves only the negative path covered.
func TestRoutes_TestPostAcceptedWithCSRF(t *testing.T) {
	srv := newTestServer(t)

	const token = "fixed-test-token"
	req := httptest.NewRequest(http.MethodPost, "/test-post", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	req.Header.Set(CSRFHeaderName, token)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 with valid CSRF material", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q; want nosniff (security headers must wrap the success path too)", got)
	}
}

// TestRoutes_SecurityHeadersOnRejectedPost pins the middleware-order
// invariant the server.go comment promises: even a 403 from CSRF must
// carry the baseline security headers.  Without this check, swapping
// the chain order (e.g. CSRF outside of securityHeaders) would slip
// through unnoticed.
func TestRoutes_SecurityHeadersOnRejectedPost(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/test-post", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options on 403 = %q; want nosniff", got)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("Content-Security-Policy missing on 403; security headers must wrap CSRF rejection")
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
		HeartbeatInterval: 25 * time.Millisecond,
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
