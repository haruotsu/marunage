package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBearerAuth_NoTokenConfigured_Passthrough: when no token is set the
// middleware must be a noop — deny-by-default would break existing single-user
// deployments that haven't set auth yet.
func TestBearerAuth_NoTokenConfigured_Passthrough(t *testing.T) {
	h := BearerAuthMiddleware("")(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (no token configured — must pass through)", rec.Code)
	}
}

// TestBearerAuth_TokenSet_NoHeader_Returns401: when a token is configured and
// the request carries no Authorization header the server must refuse with 401
// and set the WWW-Authenticate challenge so clients know how to authenticate.
func TestBearerAuth_TokenSet_NoHeader_Returns401(t *testing.T) {
	h := BearerAuthMiddleware("secret")(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", rec.Code)
	}
	wwwAuth := rec.Header().Get("WWW-Authenticate")
	if wwwAuth == "" {
		t.Error("WWW-Authenticate header missing on 401")
	}
	if wwwAuth != `Bearer realm="marunage"` {
		t.Errorf("WWW-Authenticate = %q; want %q", wwwAuth, `Bearer realm="marunage"`)
	}
}

// TestBearerAuth_WrongToken_Returns401: a request with an Authorization header
// that carries a wrong token must be refused — accepting any non-empty value
// would defeat the entire protection.
func TestBearerAuth_WrongToken_Returns401(t *testing.T) {
	h := BearerAuthMiddleware("secret")(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 (wrong token)", rec.Code)
	}
}

// TestBearerAuth_CorrectToken_Passthrough: the positive path — a request whose
// Authorization: Bearer <token> matches the configured token must be forwarded.
func TestBearerAuth_CorrectToken_Passthrough(t *testing.T) {
	h := BearerAuthMiddleware("secret")(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (correct token)", rec.Code)
	}
}

// TestBearerAuth_MalformedAuthHeader_Returns401: "Bearer" prefix without a
// token, or a non-Bearer scheme, must be rejected — silently treating
// malformed headers as unauthenticated is the safe default.
func TestBearerAuth_MalformedAuthHeader_Returns401(t *testing.T) {
	h := BearerAuthMiddleware("secret")(http.HandlerFunc(okHandler))

	for _, hdr := range []string{"Basic dXNlcjpwYXNz", "Bearer", "token secret"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", hdr)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization=%q: status = %d; want 401", hdr, rec.Code)
		}
	}
}
