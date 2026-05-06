// Package web hosts the `marunage web` HTTP server: chi-style router (built
// on net/http with Go 1.22 pattern matching), html/template-driven SSR
// pages, an SSE hub, and the CSRF + security-header middlewares.
// The Renderer interface allows the templating layer to be swapped from
// html/template to templ without a churny redesign.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
)

// CSRF cookie/header/form-field names.  These are part of the public
// browser-facing contract: the templ form helper writes the same
// `_csrf` field name, and the future fetch() call in HTMX has to send
// the same `X-CSRF-Token` header.  Pinning them as exported constants
// keeps the names from drifting silently as the code grows.
const (
	CSRFCookieName = "marunage_csrf"
	CSRFHeaderName = "X-CSRF-Token"
	CSRFFormField  = "_csrf"
)

// TokenSource generates a fresh CSRF token. Production wires this to
// crypto/rand; tests can plug in a deterministic source so cookie
// values do not change across runs.
type TokenSource func() (string, error)

// DefaultTokenSource returns 32 bytes of crypto/rand hex-encoded. 32
// bytes is the standard size for a CSRF token — large enough that
// guessing is infeasible, small enough to fit comfortably in a cookie.
func DefaultTokenSource() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("csrf: read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// CSRF is the double-submit cookie middleware.  GET / HEAD / OPTIONS
// always pass and are guaranteed to leave a token cookie on the
// response so the next mutating request from the same client has the
// material it needs.  Mutating methods are accepted only when the
// cookie value matches either the X-CSRF-Token header or the _csrf
// form field — anything else returns 403.
type CSRF struct {
	source TokenSource
}

// NewCSRF builds a CSRF middleware bound to source.  Returning an
// error rather than panicking lets the server fail loudly at startup
// if the caller passes a nil source.
func NewCSRF(source TokenSource) (*CSRF, error) {
	if source == nil {
		return nil, fmt.Errorf("csrf: nil TokenSource")
	}
	return &CSRF{source: source}, nil
}

// Middleware wraps next with the double-submit check.
func (c *CSRF) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secure := r.TLS != nil
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			if err := c.ensureCookie(w, r, secure); err != nil {
				http.Error(w, "csrf: token issue failed", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(CSRFCookieName)
		if err != nil || cookie.Value == "" {
			http.Error(w, "csrf: missing cookie", http.StatusForbidden)
			return
		}
		submitted := r.Header.Get(CSRFHeaderName)
		if submitted == "" {
			// ParseForm safely no-ops on non-form Content-Types
			// (Go stdlib gates body reads on
			// application/x-www-form-urlencoded), so we can call it
			// here without consuming a JSON / multipart body the
			// downstream handler may want to read itself.  Pinned
			// by TestCSRF_DoesNotParseNonFormBodyWhenRejecting.
			_ = r.ParseForm()
			submitted = r.Form.Get(CSRFFormField)
		}
		if submitted == "" {
			http.Error(w, "csrf: missing token", http.StatusForbidden)
			return
		}
		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(submitted)) != 1 {
			http.Error(w, "csrf: token mismatch", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// TokenFor returns the token attached to r — issuing a fresh one if
// the cookie is absent.  Templates use this to embed the hidden
// _csrf field in <form> elements without running the full middleware
// dance themselves.
func (c *CSRF) TokenFor(w http.ResponseWriter, r *http.Request) (string, error) {
	if cookie, err := r.Cookie(CSRFCookieName); err == nil && cookie.Value != "" {
		return cookie.Value, nil
	}
	if err := c.ensureCookie(w, r, r.TLS != nil); err != nil {
		return "", err
	}
	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil {
		return "", err
	}
	return cookie.Value, nil
}

func (c *CSRF) ensureCookie(w http.ResponseWriter, r *http.Request, secure bool) error {
	if cookie, err := r.Cookie(CSRFCookieName); err == nil && cookie.Value != "" {
		// Re-set the cookie on the response so subsequent handlers
		// (and TokenFor) can read it back via r.Cookie before the
		// browser round-trip — required by tests that issue a single
		// request and then inspect the recorder.
		http.SetCookie(w, newCSRFCookie(cookie.Value, secure))
		return nil
	}
	token, err := c.source()
	if err != nil {
		return err
	}
	r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	http.SetCookie(w, newCSRFCookie(token, secure))
	return nil
}

// newCSRFCookie tracks the request's TLS state via secure: setting the
// flag on a plain-HTTP response would make the browser refuse the
// cookie entirely (breaking local dev), while leaving it off on a
// TLS response lets a downgraded session leak the token in cleartext.
// The middleware passes r.TLS != nil so the cookie always matches the
// transport that issued it.
func newCSRFCookie(value string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     CSRFCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: false, // readable by JS so fetch() can echo it as X-CSRF-Token
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	}
}
