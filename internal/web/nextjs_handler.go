package web

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

// newNextJSHandler serves a Next.js static export.
// For paths that don't match a regular file, it serves index.html (SPA fallback).
func newNextJSHandler(njs fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		path = strings.TrimSuffix(path, "/")
		if path == "" {
			path = "index.html"
		}
		// Only serve the path if it resolves to a regular file.
		// Missing entries and directories both fall back to index.html so that
		// Next.js client-side routing handles the request.
		fi, err := fs.Stat(njs, path)
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		case err != nil || fi.IsDir():
			path = "index.html"
		}
		http.ServeFileFS(w, r, njs, path)
	})
}
