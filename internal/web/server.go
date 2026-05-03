package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// shutdownGracePeriod caps how long Server.Serve waits for in-flight
// requests to finish after the parent context is cancelled.  Aligns
// with the brief's "5 秒タイムアウト" requirement so PR-62 can
// integrate cleanly with the daemon supervisor's SIGTERM behaviour.
const shutdownGracePeriod = 5 * time.Second

// HTTP timeout defaults harden the server against slow-loris and
// idle keep-alive hoarding.  WriteTimeout is intentionally absent —
// /events SSE writes for the connection lifetime, so a non-zero
// WriteTimeout would trip the heartbeat loop.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

// Options configures NewServer.  Zero-valued fields fall back to
// production defaults: real CSRF entropy, 30s SSE heartbeat, no
// /test-post route, and no access logging.
type Options struct {
	// TokenSource overrides the CSRF token entropy source.  Production
	// leaves this nil so DefaultTokenSource (crypto/rand) is used;
	// tests inject a deterministic stub via testTokenSource.
	TokenSource TokenSource

	// HeartbeatInterval tunes the SSE handler.  Zero means
	// defaultHeartbeat (30s).
	HeartbeatInterval time.Duration

	// EnableTestRoutes opts into the brief's /test-post echo handler.
	// Disabled in production so the binary never exposes an
	// unauthenticated mutating endpoint.
	EnableTestRoutes bool

	// AccessLogger receives one AccessRecord per request.  Nil
	// disables access logging entirely (the default for unit tests).
	AccessLogger AccessLogger

	// Dashboard supplies the read-side aggregation the index +
	// /partials/dashboard handlers render.  Nil falls back to a
	// noop provider that emits an empty snapshot — handler tests
	// from PR-62 (TestRoutes_IndexHTML, etc.) keep passing without
	// having to wire a fake store, while the production CLI plugs
	// in a real sqlDashboardStore-backed provider via the
	// dashboard factory.
	Dashboard DashboardProvider
}

// Server is the assembled chi-style router + middlewares + SSE hub.
// Routes() returns the http.Handler the lifecycle methods (Serve, the
// CLI command) wrap with a *http.Server.
type Server struct {
	csrf      *CSRF
	hub       *Hub
	renderer  Renderer
	dashboard DashboardProvider
	opts      Options
}

// NewServer wires the renderer, CSRF middleware, and hub.  Returning
// the assembled struct (rather than a bare http.Handler) lets the CLI
// layer reach into Hub later — PR-91 will hook real dispatch events
// into the same hub instance.
func NewServer(opts Options) (*Server, error) {
	if opts.TokenSource == nil {
		opts.TokenSource = DefaultTokenSource
	}
	csrf, err := NewCSRF(opts.TokenSource)
	if err != nil {
		return nil, err
	}
	renderer, err := newHTMLTemplateRenderer(templatesFS())
	if err != nil {
		return nil, err
	}
	dashboard := opts.Dashboard
	if dashboard == nil {
		dashboard = noopDashboardProvider{now: time.Now}
	}
	return &Server{
		csrf:      csrf,
		hub:       NewHub(),
		renderer:  renderer,
		dashboard: dashboard,
		opts:      opts,
	}, nil
}

// Hub exposes the shared event hub so PR-91 (and any in-tree caller
// that wants to publish from the CLI side) can fan events into the
// same fan-out the SSE handler reads from.
func (s *Server) Hub() *Hub { return s.hub }

// Routes returns the wired-up http.Handler.  Order of middleware
// matters: securityHeaders → access log → CSRF → mux, so security
// headers land on every response (including 403 from CSRF) and the
// access log records the final status code.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", newHealthzHandler())
	mux.Handle("GET /", newIndexHandler(s.renderer, s.csrf, s.dashboard))
	mux.Handle("GET /partials/dashboard", newDashboardPartialHandler(s.renderer, s.dashboard))
	mux.Handle("GET /events", NewSSEHandler(s.hub, SSEOptions{HeartbeatInterval: s.opts.HeartbeatInterval}))
	mux.Handle("GET /static/", newStaticHandler())

	if s.opts.EnableTestRoutes {
		mux.Handle("POST /test-post", newTestPostHandler())
	}

	var h http.Handler = mux
	h = s.csrf.Middleware(h)
	h = newAccessLogger(h, s.opts.AccessLogger)
	h = securityHeaders(h)
	return h
}

// Serve runs the HTTP server on listener until ctx is done, then
// performs a graceful Shutdown bounded by shutdownGracePeriod.
//
// Returning the inner http.Server's error (other than ErrServerClosed,
// which is the normal shutdown signal) lets the CLI surface real
// listen failures — bind-address-in-use is the obvious example.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	httpSrv := s.newHTTPServer()

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(listener) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("web: shutdown: %w", err)
	}
	// Drain whatever Serve returned so the goroutine is observed
	// before Serve exits.
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newHTTPServer assembles the *http.Server Serve runs in production.
// Factoring the build out (rather than inlining inside Serve) lets
// the timeout regression test assert on the concrete Server fields
// — a refactor that silently drops one of the timeout assignments
// here will be caught by the test reading those exact fields.
func (s *Server) newHTTPServer() *http.Server {
	return &http.Server{
		Handler:           s.Routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}
}
