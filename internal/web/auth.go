package web

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuthMiddleware returns a middleware that enforces Bearer token
// authentication when token is non-empty. An empty token disables auth so
// existing single-user loopback deployments are unaffected.
//
// Failures return 401 with WWW-Authenticate: Bearer realm="marunage".
// The comparison is constant-time to prevent timing attacks.
func BearerAuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := bearerToken(r)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="marunage"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the token from "Authorization: Bearer <token>".
// Returns empty string for any other scheme or malformed header.
func bearerToken(r *http.Request) string {
	hdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(hdr, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(hdr, "Bearer ")
}
