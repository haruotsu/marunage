package web

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCSRF_GetIsAllowed pins the contract that read methods bypass the
// double-submit check entirely. GET / HEAD / OPTIONS must always succeed
// and the middleware must hand back the canonical token cookie so the
// client can echo it on the next mutating request.
func TestCSRF_GetIsAllowed(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200", rec.Code)
			}
			cookie := findCookie(rec.Result().Cookies(), CSRFCookieName)
			if cookie == nil {
				t.Fatalf("response missing %q cookie; got %v", CSRFCookieName, rec.Result().Cookies())
			}
			if cookie.Value == "" {
				t.Errorf("%q cookie value empty", CSRFCookieName)
			}
		})
	}
}

// TestCSRF_PostWithoutTokenIs403 pins the negative side of the
// double-submit contract. A POST that lacks both the cookie and the
// header / form field must be refused with 403 — silently letting it
// through would defeat the protection.
func TestCSRF_PostWithoutTokenIs403(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", rec.Code)
	}
}

// TestCSRF_PostWithMatchingTokenIs200 pins the positive side: when
// cookie and header carry the same token the request is forwarded to
// the handler unchanged.
func TestCSRF_PostWithMatchingTokenIs200(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	token := "fixed-test-token"
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	req.Header.Set(CSRFHeaderName, token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
}

// TestCSRF_PostWithMismatchingTokenIs403 closes the obvious bypass: a
// request that sends *some* cookie + header but not the matching pair
// must still fail. Without this the middleware could accept any header
// value as long as both fields were merely present.
func TestCSRF_PostWithMismatchingTokenIs403(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "cookie-token"})
	req.Header.Set(CSRFHeaderName, "header-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", rec.Code)
	}
}

// TestCSRF_CookieReusedOnSubsequentGET pins the early-return path in
// ensureCookie: a request that already carries a valid cookie must
// keep that exact value rather than receive a freshly minted token on
// every read.  Rotating the token per-request would break long-lived
// HTMX pages whose hidden field was rendered minutes earlier.
func TestCSRF_CookieReusedOnSubsequentGET(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	const existing = "previously-issued-token"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: existing})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	cookie := findCookie(rec.Result().Cookies(), CSRFCookieName)
	if cookie == nil {
		t.Fatalf("missing %q cookie", CSRFCookieName)
	}
	if cookie.Value != existing {
		t.Errorf("cookie value = %q; want %q (existing token must NOT rotate per-request)", cookie.Value, existing)
	}
}

// TestCSRF_CookieSecureMirrorsTLS pins the contract that the CSRF
// cookie carries the Secure attribute exactly when the request rode
// in over TLS — without this guard the cookie would be readable in
// transit on any plain-HTTP --remote deployment, defeating the
// double-submit defence under MITM. Plain-HTTP requests must NOT set
// Secure or browsers refuse the cookie entirely (which would break
// local dev).
func TestCSRF_CookieSecureMirrorsTLS(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	t.Run("plain HTTP does not set Secure", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		cookie := findCookie(rec.Result().Cookies(), CSRFCookieName)
		if cookie == nil {
			t.Fatalf("missing %q cookie", CSRFCookieName)
		}
		if cookie.Secure {
			t.Errorf("Secure = true for plain-HTTP request; would break local dev")
		}
	})

	t.Run("TLS request sets Secure", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://localhost/", nil)
		req.TLS = &tls.ConnectionState{}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		cookie := findCookie(rec.Result().Cookies(), CSRFCookieName)
		if cookie == nil {
			t.Fatalf("missing %q cookie", CSRFCookieName)
		}
		if !cookie.Secure {
			t.Errorf("Secure = false for TLS request; cookie can leak over a downgraded session")
		}
	})
}

// TestCSRF_PostAcceptsFormField mirrors the templ form helper path: a
// classic <form method=POST> can carry the token via a hidden field
// instead of a header. Either path must be accepted.
func TestCSRF_PostAcceptsFormField(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	token := "form-token"
	body := strings.NewReader(CSRFFormField + "=" + token)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
}

// TestCSRF_DoesNotParseNonFormBodyWhenRejecting pins the middleware
// behaviour when a request lacks the CSRF header and the Content-Type
// is NOT form-encoded: the middleware must reject (no token found)
// without consuming r.Body.  Without this guard a future swap to
// manual body reading inside CSRF would silently drain the JSON of
// any logging / replay middleware that runs further out.
//
// The body is wrapped in a counting reader so the assertion is on
// "bytes read by middleware" — a zero count means the request
// reached the handler chain with the body fully intact.
func TestCSRF_DoesNotParseNonFormBodyWhenRejecting(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	const payload = `{"hello":"world"}`
	counter := &countingReader{src: strings.NewReader(payload)}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	// Cookie present but header / form field both absent — middleware
	// must reject without touching the body.
	req := httptest.NewRequest(http.MethodPost, "/", counter)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "cookie-token"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", rec.Code)
	}
	if counter.bytesRead != 0 {
		t.Fatalf("CSRF middleware read %d bytes from a non-form body; expected 0 (any read here would drain JSON for downstream readers)", counter.bytesRead)
	}
	// Sanity: the body must still be fully readable post-middleware.
	remaining, _ := io.ReadAll(req.Body)
	if string(remaining) != payload {
		t.Errorf("body after middleware = %q; want %q (CSRF must leave the body byte-for-byte)", remaining, payload)
	}
}

// TestCSRF_GetResponseIncludesCSRFTokenHeader pins the contract that GET
// responses carry the X-CSRF-Token header so JS fetch() can cache the token
// without relying on document.cookie (which privacy extensions may block).
func TestCSRF_GetResponseIncludesCSRFTokenHeader(t *testing.T) {
	csrf, err := NewCSRF(testTokenSource)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	h := csrf.Middleware(http.HandlerFunc(okHandler))

	t.Run("fresh request: header matches issued cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		header := rec.Header().Get(CSRFHeaderName)
		if header == "" {
			t.Fatalf("response missing %q header", CSRFHeaderName)
		}
		cookie := findCookie(rec.Result().Cookies(), CSRFCookieName)
		if cookie == nil {
			t.Fatalf("missing %q cookie", CSRFCookieName)
		}
		if header != cookie.Value {
			t.Errorf("header %q = %q; want cookie value %q", CSRFHeaderName, header, cookie.Value)
		}
	})

	t.Run("existing cookie: header echoes existing token", func(t *testing.T) {
		const existing = "previously-issued-token"
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: existing})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		header := rec.Header().Get(CSRFHeaderName)
		if header != existing {
			t.Errorf("header %q = %q; want %q", CSRFHeaderName, header, existing)
		}
	})

	t.Run("HEAD with existing cookie: header echoes token", func(t *testing.T) {
		const existing = "head-method-token"
		req := httptest.NewRequest(http.MethodHead, "/", nil)
		req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: existing})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		header := rec.Header().Get(CSRFHeaderName)
		if header != existing {
			t.Errorf("header %q = %q; want %q", CSRFHeaderName, header, existing)
		}
	})
}

// countingReader wraps an io.Reader and records how many bytes have
// been pulled.  Used to make "did this layer read the body?" a
// directly-assertable property rather than an inference from leftover
// bytes (the previous version's assertion was a logical hole — see
// the test docstring).
type countingReader struct {
	src       io.Reader
	bytesRead int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.bytesRead += n
	return n, err
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// testTokenSource returns a deterministic token so GET-issued cookies
// are predictable in tests without giving up the real entropy source
// in production.
func testTokenSource() (string, error) {
	return "fixed-test-token", nil
}
