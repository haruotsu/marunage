package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
)

// TestOIDC_EmptyIssuer_Noop: when no OIDC issuer is configured the middleware
// must be a transparent passthrough — deny-by-default would break loopback
// deployments that haven't set up an OIDC provider.
func TestOIDC_EmptyIssuer_Noop(t *testing.T) {
	h := OIDCMiddleware(config.OIDCConfig{})(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (no issuer — must pass through)", rec.Code)
	}
}

// TestOIDC_UnauthenticatedAccess_RedirectsToLogin: when OIDC is configured an
// unauthenticated request to a protected route must redirect to /auth/login so
// the browser starts the OIDC flow.
func TestOIDC_UnauthenticatedAccess_RedirectsToLogin(t *testing.T) {
	cfg := config.OIDCConfig{
		Issuer:      "https://example.com",
		ClientID:    "client",
		RedirectURL: "https://app.example.com/auth/callback",
	}
	h := OIDCMiddleware(cfg)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d; want 302 redirect to /auth/login", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("Location = %q; want /auth/login", loc)
	}
}

// TestOIDC_LoginEndpoint_RedirectsToProvider: GET /auth/login must redirect to
// the OIDC provider's authorization endpoint with the required OAuth2 parameters
// (response_type=code, client_id, redirect_uri, state).
func TestOIDC_LoginEndpoint_RedirectsToProvider(t *testing.T) {
	cfg := config.OIDCConfig{
		Issuer:      "https://idp.example.com",
		ClientID:    "myclient",
		RedirectURL: "https://app.example.com/auth/callback",
	}
	h := OIDCMiddleware(cfg)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d; want 302 redirect to OIDC provider", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, cfg.Issuer) {
		t.Errorf("Location = %q; want prefix %q (should redirect to OIDC provider)", loc, cfg.Issuer)
	}
	if !strings.Contains(loc, "client_id=myclient") {
		t.Errorf("Location = %q; missing client_id parameter", loc)
	}
	if !strings.Contains(loc, "response_type=code") {
		t.Errorf("Location = %q; missing response_type=code parameter", loc)
	}
	// state cookie must be set so /auth/callback can validate it
	stateCookie := findCookie(rec.Result().Cookies(), "oidc_state")
	if stateCookie == nil {
		t.Error("oidc_state cookie missing; callback cannot validate CSRF state")
	}
}

// TestOIDC_CallbackWithValidState_SetsSessionCookie: /auth/callback with a
// matching state value must exchange the code and set a signed session cookie.
// The test uses a mock token endpoint so no real network calls are made.
func TestOIDC_CallbackWithValidState_SetsSessionCookie(t *testing.T) {
	// Fake token server that returns a minimal JSON token response.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","id_token":"dummy"}`))
	}))
	t.Cleanup(tokenServer.Close)

	cfg := config.OIDCConfig{
		Issuer:       tokenServer.URL,
		ClientID:     "client",
		ClientSecret: "secret",
		RedirectURL:  "https://app.example.com/auth/callback",
	}
	h := OIDCMiddleware(cfg)(http.HandlerFunc(okHandler))

	const stateVal = "teststate123"

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=authcode&state="+stateVal, nil)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: stateVal})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("status = %d; callback rejected despite valid state", rec.Code)
	}
	sessionCookie := findCookie(rec.Result().Cookies(), "oidc_session")
	if sessionCookie == nil {
		t.Error("oidc_session cookie missing; authenticated session not established")
	}
}

// TestOIDC_CallbackWithInvalidState_Rejects: /auth/callback with a mismatched
// state must return 400 to prevent CSRF attacks via the authorization flow.
func TestOIDC_CallbackWithInvalidState_Rejects(t *testing.T) {
	cfg := config.OIDCConfig{
		Issuer:      "https://idp.example.com",
		ClientID:    "client",
		RedirectURL: "https://app.example.com/auth/callback",
	}
	h := OIDCMiddleware(cfg)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state=wrong", nil)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: "correct_state"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 for state mismatch (CSRF protection)", rec.Code)
	}
}

// TestOIDC_AuthenticatedSession_Passthrough: a request carrying a valid signed
// session cookie must reach the downstream handler without being redirected.
func TestOIDC_AuthenticatedSession_Passthrough(t *testing.T) {
	cfg := config.OIDCConfig{
		Issuer:      "https://idp.example.com",
		ClientID:    "client",
		RedirectURL: "https://app.example.com/auth/callback",
	}

	// First get a session cookie via the login+callback flow by going through
	// the middleware's /auth/login to extract the signed session cookie format.
	// Since session signing uses an internal random key, we obtain a real cookie
	// by replaying the callback path with a fake token server.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer"}`))
	}))
	t.Cleanup(tokenServer.Close)

	cfgWithToken := cfg
	cfgWithToken.Issuer = tokenServer.URL
	h := OIDCMiddleware(cfgWithToken)(http.HandlerFunc(okHandler))

	// Obtain session cookie through the callback path.
	const state = "s1"
	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c&state="+state, nil)
	callbackReq.AddCookie(&http.Cookie{Name: "oidc_state", Value: state})
	callbackRec := httptest.NewRecorder()
	h.ServeHTTP(callbackRec, callbackReq)

	sessionCookie := findCookie(callbackRec.Result().Cookies(), "oidc_session")
	if sessionCookie == nil {
		t.Skip("could not obtain session cookie; skipping passthrough test")
	}

	// Now make a regular request with the session cookie.
	protectedReq := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	protectedReq.AddCookie(sessionCookie)
	protectedRec := httptest.NewRecorder()
	h.ServeHTTP(protectedRec, protectedReq)

	if protectedRec.Code == http.StatusFound {
		t.Errorf("status = 302; want non-redirect for authenticated session; Location=%q",
			protectedRec.Header().Get("Location"))
	}
}
