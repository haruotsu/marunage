package web

import (
	"io/fs"
	"net/http"
	"strings"
)

// newNextJSHandler serves a Next.js static export.
// For paths that don't match a static file, it serves index.html (SPA fallback).
func newNextJSHandler(njs fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := njs.Open(path); err != nil {
			path = "index.html"
		}
		http.ServeFileFS(w, r, njs, path)
	})
}
