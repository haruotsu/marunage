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
}

// Server is the assembled chi-style router + middlewares + SSE hub.
// Routes() returns the http.Handler the lifecycle methods (Serve, the
// CLI command) wrap with a *http.Server.
type Server struct {
	csrf     *CSRF
	hub      *Hub
	renderer Renderer
	opts     Options
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
	return &Server{
		csrf:     csrf,
		hub:      NewHub(),
		renderer: renderer,
		opts:     opts,
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
	mux.Handle("GET /", newIndexHandler(s.renderer, s.csrf))
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
	httpSrv := &http.Server{
		Handler:           s.Routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}

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

// httpServerSettings is the snapshot the timeout regression test
// inspects.  Keeping it as a separate small struct (rather than
// exporting fields on *http.Server) avoids leaking the full server
// surface to tests.
type httpServerSettings struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	IdleTimeout       time.Duration
}

func serverHTTPSettingsForTest() httpServerSettings {
	return httpServerSettings{
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}
}
