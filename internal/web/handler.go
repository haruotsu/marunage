package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

// Renderer abstracts html/template behind a swappable interface so a
// later PR can swap to templ without touching every handler.  The
// minimal surface — Render(w, name, data) — is all the PR-62
// dashboard placeholder needs.
type Renderer interface {
	Render(w http.ResponseWriter, name string, data any) error
}

// htmlTemplateRenderer is the default Renderer.  It parses every
// embedded *.html template once at startup and renders them by name.
type htmlTemplateRenderer struct {
	tmpl *template.Template
}

func newHTMLTemplateRenderer(filesystem fs.FS) (*htmlTemplateRenderer, error) {
	t, err := template.ParseFS(filesystem, "*.html")
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}
	return &htmlTemplateRenderer{tmpl: t}, nil
}

func (r *htmlTemplateRenderer) Render(w http.ResponseWriter, name string, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.tmpl.ExecuteTemplate(w, name, data); err != nil {
		return fmt.Errorf("web: render %s: %w", name, err)
	}
	return nil
}

// indexData is the typed payload the index template binds against.
// Keeping the struct here (rather than in a templates/ package) makes
// the contract obvious at the handler call site.
type indexData struct {
	CSRFToken     string
	CSRFFieldName string
}

// newIndexHandler returns the GET / handler.  The handler issues the
// CSRF cookie via TokenFor so a brand-new visitor always leaves with
// a token cookie, then renders the dashboard placeholder.
func newIndexHandler(renderer Renderer, csrf *CSRF) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := csrf.TokenFor(w, r)
		if err != nil {
			http.Error(w, "csrf token issue failed", http.StatusInternalServerError)
			return
		}
		if err := renderer.Render(w, "index.html", indexData{
			CSRFToken:     token,
			CSRFFieldName: CSRFFormField,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

// newHealthzHandler returns the always-200 "ok" probe handler.
func newHealthzHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})
}

// newStaticHandler returns the /static/* file server.  http.FS turns
// the embedded fs.FS into something http.FileServer can serve.
func newStaticHandler() http.Handler {
	return http.StripPrefix("/static/", http.FileServer(http.FS(staticFS())))
}

// newTestPostHandler is the /test-post echo target the brief uses to
// validate the CSRF middleware.  It is registered only when
// Options.EnableTestRoutes is true so production never exposes an
// unauthenticated mutating endpoint.
func newTestPostHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})
}

// securityHeaders wraps next, adding the baseline headers PR-62
// promises (X-Content-Type-Options / X-Frame-Options / a strict CSP).
// HTMX needs script-src 'unsafe-inline' for its hx-on attributes; we
// intentionally do NOT allow that here since the placeholder page
// ships without HTMX — PR-63 can relax CSP if it actually needs it.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"img-src 'self' data:; "+
				"style-src 'self'; "+
				"script-src 'self'; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// newAccessLogger wraps next with a single-line access log.  PR-62
// uses the package-level discard logger by default; CLI wiring will
// inject the real daemon.log writer.
func newAccessLogger(next http.Handler, logger AccessLogger) http.Handler {
	if logger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Log(AccessRecord{
			Method:   r.Method,
			Path:     r.URL.Path,
			Status:   rec.status,
			Duration: time.Since(started),
		})
	})
}

// AccessRecord is one row of the JSON-Lines access log.  Kept simple
// on purpose: structured logging integration with internal/logging
// lands when PR-63's dashboard needs richer context.
type AccessRecord struct {
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Status   int           `json:"status"`
	Duration time.Duration `json:"duration_ns"`
}

// AccessLogger is the narrow seam the server uses for request logging.
// Defaulting Server.Options.AccessLogger to nil disables logging
// entirely so unit tests stay quiet.
type AccessLogger interface {
	Log(AccessRecord)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
