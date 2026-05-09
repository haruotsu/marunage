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
		fi, err := fs.Stat(njs, path)
		// fs.Stat guarantees: err != nil implies fi == nil, and err == nil implies fi != nil.
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		case fi != nil && fi.IsDir():
			// Next.js static export stores each route as <route>/index.html.
			// Try the directory index before falling back to the SPA root.
			if _, err2 := fs.Stat(njs, path+"/index.html"); err2 == nil {
				path += "/index.html"
			} else {
				path = "index.html"
			}
		case err != nil:
			path = "index.html"
		}
		http.ServeFileFS(w, r, njs, path)
	})
}
