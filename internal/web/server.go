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

	// Skills wires the read-only PR-203 skill registry surface.
	// The zero value disables /skills and /api/skills/* so PR-62's
	// minimal index page keeps working.
	Skills SkillsConfig

	// TaskDetail wires the task detail page provider. Nil falls back to
	// a noop that returns 404 for all IDs so PR-62's handler tests keep
	// passing without having to supply a fake store.
	TaskDetail TaskDetailProvider

	// AuditLog wires the audit log reader for the task detail page.
	// Nil falls back to a noop reader that returns empty entries.
	AuditLog AuditReader

	// TaskOps wires the write-side store for task operation endpoints
	// (dispatch / promote / reopen / add / update-priority / delete).
	// Nil disables all /api/tasks/* mutating routes so existing tests
	// that do not care about task ops keep passing without wiring a store.
	TaskOps TaskOpsStore

	// Review wires the read-side provider for the /review page.
	// Nil disables GET /review and GET /api/review/skipped so servers
	// that do not care about review keep passing without wiring a store.
	Review ReviewProvider

	// Metrics wires the metrics provider for GET /metrics and GET /api/metrics.
	// Nil falls back to a noop provider that returns empty metrics.
	Metrics MetricsProvider

	// Journal wires the work journal provider for GET /journal and GET /api/journal.
	// Nil falls back to a noop provider that returns empty entries.
	Journal JournalProvider

	// Project wires the project board provider for GET /project and GET /api/project.
	// Nil falls back to a noop provider that returns empty phases.
	Project ProjectProvider
}

// Server is the assembled chi-style router + middlewares + SSE hub.
// Routes() returns the http.Handler the lifecycle methods (Serve, the
// CLI command) wrap with a *http.Server.
type Server struct {
	csrf       *CSRF
	hub        *Hub
	renderer   Renderer
	dashboard  DashboardProvider
	taskDetail TaskDetailProvider
	auditLog   AuditReader
	taskOps    TaskOpsStore
	review     ReviewProvider
	metrics    MetricsProvider
	journal    JournalProvider
	project    ProjectProvider
	opts       Options
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
	taskDetail := opts.TaskDetail
	if taskDetail == nil {
		taskDetail = noopTaskDetailProvider{}
	}
	auditLog := opts.AuditLog
	if auditLog == nil {
		auditLog = noopAuditReader{}
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = noopMetricsProvider{}
	}
	journal := opts.Journal
	if journal == nil {
		journal = noopJournalProvider{}
	}
	project := opts.Project
	if project == nil {
		project = noopProjectProvider{}
	}
	return &Server{
		csrf:       csrf,
		hub:        NewHub(),
		renderer:   renderer,
		dashboard:  dashboard,
		taskDetail: taskDetail,
		auditLog:   auditLog,
		taskOps:    opts.TaskOps,
		review:     opts.Review,
		metrics:    metrics,
		journal:    journal,
		project:    project,
		opts:       opts,
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
	mux.Handle("GET /tasks/{id}", newTaskDetailHandler(s.renderer, s.taskDetail, s.auditLog))
	mux.Handle("GET /skills", newSkillsHandler(s.renderer, s.csrf, s.opts.Skills))
	mux.Handle("GET /api/skills/installed", newInstalledSkillsAPIHandler(s.opts.Skills))
	mux.Handle("GET /api/skills/registry", newRegistrySearchAPIHandler(s.opts.Skills))

	if s.opts.EnableTestRoutes {
		mux.Handle("POST /test-post", newTestPostHandler())
	}

	// Review endpoints registered only when a ReviewProvider is wired.
	if s.review != nil {
		mux.Handle("GET /review", newReviewHandler(s.renderer, s.review))
		mux.Handle("GET /api/review/skipped", newReviewAPIHandler(s.review))
	}

	// Metrics, Journal, Project endpoints (PR-105). Always registered: unlike
	// Review (which has no meaningful empty state), these three pages provide
	// useful UI even when no real provider is wired — the noop fallback renders
	// an empty-but-valid dashboard so a fresh install looks functional rather
	// than missing pages. Review stays nil-gated because an empty skipped-tasks
	// page would be misleading without a real store.
	mux.Handle("GET /metrics", newMetricsHandler(s.renderer, s.metrics))
	mux.Handle("GET /api/metrics", newMetricsAPIHandler(s.metrics))
	mux.Handle("GET /prometheus", newPrometheusHandler(s.metrics))
	mux.Handle("GET /journal", newJournalHandler(s.renderer, s.journal))
	mux.Handle("GET /api/journal", newJournalAPIHandler(s.journal))
	mux.Handle("GET /project", newProjectHandler(s.renderer, s.project))
	mux.Handle("GET /api/project", newProjectAPIHandler(s.project))

	// Task operation endpoints (PR-65). Registered only when a TaskOpsStore
	// has been wired so servers without a store never expose /api/tasks/*.
	if s.taskOps != nil {
		mux.Handle("POST /api/tasks/{id}/dispatch", newDispatchTaskHandler(s.taskOps))
		mux.Handle("POST /api/tasks/{id}/promote", newPromoteTaskHandler(s.taskOps))
		mux.Handle("POST /api/tasks/{id}/reopen", newReopenTaskHandler(s.taskOps))
		mux.Handle("POST /api/tasks", newAddTaskHandler(s.taskOps))
		mux.Handle("PATCH /api/tasks/{id}/priority", newUpdatePriorityHandler(s.taskOps))
		mux.Handle("DELETE /api/tasks/{id}", newDeleteTaskHandler(s.taskOps))
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
