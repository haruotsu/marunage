package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/config"
)

// oidcHTTPClient is used for token endpoint calls; the 15s timeout prevents
// a slow OIDC provider from blocking a login indefinitely.
var oidcHTTPClient = &http.Client{Timeout: 15 * time.Second}

const (
	oidcStateCookie   = "oidc_state"
	oidcSessionCookie = "oidc_session"
)

// OIDCMiddleware returns middleware that protects routes with OIDC authentication.
// If cfg.Issuer is empty, the middleware is a transparent noop.
//
// Authenticated state is stored as a signed cookie (HMAC-SHA256 with a
// per-process random key generated via crypto/rand).  The session secret is
// generated once at middleware creation time and is never written to disk.
func OIDCMiddleware(cfg config.OIDCConfig) func(http.Handler) http.Handler {
	if cfg.Issuer == "" {
		return func(next http.Handler) http.Handler { return next }
	}

	sessionSecret := make([]byte, 32)
	if _, err := rand.Read(sessionSecret); err != nil {
		// crypto/rand failure is catastrophic — return deny-all rather than
		// silently using a weak secret.
		return func(_ http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "oidc: failed to initialise session secret", http.StatusInternalServerError)
			})
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/auth/login":
				oidcHandleLogin(w, r, cfg)
			case "/auth/callback":
				oidcHandleCallback(w, r, cfg, sessionSecret)
			default:
				if !oidcHasValidSession(r, sessionSecret) {
					http.Redirect(w, r, "/auth/login", http.StatusFound)
					return
				}
				next.ServeHTTP(w, r)
			}
		})
	}
}

// oidcHandleLogin starts the OIDC authorization flow: it generates a random
// state value, stores it in a cookie, and redirects to the OIDC provider.
func oidcHandleLogin(w http.ResponseWriter, r *http.Request, cfg config.OIDCConfig) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		http.Error(w, "oidc: failed to generate state", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})

	authURL := cfg.Issuer + "/authorize"
	params := url.Values{
		"response_type": {"code"},
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURL},
		"scope":         {"openid"},
		"state":         {state},
	}
	http.Redirect(w, r, authURL+"?"+params.Encode(), http.StatusFound)
}

func oidcHandleCallback(
	w http.ResponseWriter, r *http.Request,
	cfg config.OIDCConfig, sessionSecret []byte,
) {
	qstate := r.URL.Query().Get("state")
	cookieState, err := r.Cookie(oidcStateCookie)
	if err != nil || cookieState.Value == "" || cookieState.Value != qstate {
		http.Error(w, "oidc: state mismatch", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "oidc: missing code", http.StatusBadRequest)
		return
	}

	// Exchange the code for tokens via the provider's token endpoint.
	if err := oidcExchangeCode(cfg, code); err != nil {
		http.Error(w, fmt.Sprintf("oidc: token exchange: %v", err), http.StatusInternalServerError)
		return
	}

	// Issue a signed session cookie.
	sessionVal := oidcSignSession(sessionSecret, "authenticated")
	http.SetCookie(w, &http.Cookie{
		Name:     oidcSessionCookie,
		Value:    sessionVal,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})

	// Clear the state cookie.
	http.SetCookie(w, &http.Cookie{
		Name:   oidcStateCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

func oidcExchangeCode(cfg config.OIDCConfig, code string) error {
	tokenURL := cfg.Issuer + "/token"
	resp, err := oidcHTTPClient.PostForm(tokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURL},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}
	if errVal, ok := body["error"].(string); ok && errVal != "" {
		return fmt.Errorf("provider error: %s", errVal)
	}
	return nil
}

// oidcSignSession returns a base64-encoded HMAC-SHA256 MAC over payload using
// sessionSecret.  The format is "<payload>.<mac>" so the verifier can split on
// ".".
func oidcSignSession(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// oidcHasValidSession returns true when r carries a signed oidc_session cookie
// whose MAC matches sessionSecret.
func oidcHasValidSession(r *http.Request, sessionSecret []byte) bool {
	c, err := r.Cookie(oidcSessionCookie)
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	expected := oidcSignSession(sessionSecret, string(payload))
	return hmac.Equal([]byte(c.Value), []byte(expected))
}
