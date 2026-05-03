package web

import (
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
