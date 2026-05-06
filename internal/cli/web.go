package cli

import (
	"context"
	"fmt"
	"net"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/web"
)

// cmuxClientStreamer adapts cmux.Client into web.WorkspaceStreamer so the
// web layer does not import the cmux package directly.
type cmuxClientStreamer struct {
	client cmux.Client
}

func (s *cmuxClientStreamer) ReadOutput(ctx context.Context, workspaceID string) (string, error) {
	return s.client.ReadOutput(ctx, cmux.Workspace{ID: workspaceID})
}

func (s *cmuxClientStreamer) Send(ctx context.Context, workspaceID string, text string) error {
	return s.client.Send(ctx, cmux.Workspace{ID: workspaceID}, text)
}

// sqlLiveStreamProvider implements web.LiveStreamProvider by looking up
// the ws field of a task row via TaskDetailStore.
type sqlLiveStreamProvider struct {
	store web.TaskDetailStore
}

func (p *sqlLiveStreamProvider) WorkspaceIDForTask(ctx context.Context, taskID int64) (string, error) {
	task, err := p.store.TaskDetail(ctx, taskID)
	if err != nil {
		return "", err
	}
	if task.WS == "" {
		return "", fmt.Errorf("live stream: task %d has no workspace: %w", taskID, store.ErrNotFound)
	}
	return task.WS, nil
}

// webRunner is the narrow surface newWebCmd needs from the assembled
// web.Server.  Keeping it as an interface is the test seam: production
// wires a serverRunner around *web.Server (which calls Serve on a
// pre-bound listener inside Run), tests inject a fake via
// withWebFactory that returns immediately.
type webRunner interface {
	Run(ctx context.Context) error
}

// WebFactoryOptions is the resolved input the CLI hands to the web
// factory: the effective addr (after flag/config/--remote
// precedence) and the path to the active config.toml so the factory
// can locate sibling state — daemon.log lives at
// <configDir>/logs/daemon.log per docs/requirement.md ファイルレイア
// ウト.  --remote awareness lives in the CLI layer (it rewrites Addr
// to 0.0.0.0 + emits the warning banner) so the factory does not
// need to know about it.
type WebFactoryOptions struct {
	Addr       string
	ConfigPath string
}

// webFactory builds a webRunner from the resolved options and hands
// back a closer the CLI must defer.  Mirrors dispatcherFactory's
// (runner, closer, error) shape so both CLI files follow the same
// idiom: the factory holds the listener, log file, and any other
// long-lived resource, and the closer releases all of them in one
// shot regardless of whether Run() returned cleanly.
type webFactory func(ctx context.Context, opts WebFactoryOptions) (webRunner, func() error, error)

// webFactoryHook is the package-private slot tests use via
// withWebFactory; production callers see nil and fall through to
// productionWebFactory.
var webFactoryHook webFactory

func withWebFactory(t interface{ Cleanup(func()) }, f webFactory) {
	prev := webFactoryHook
	webFactoryHook = f
	t.Cleanup(func() { webFactoryHook = prev })
}

func activeWebFactory() webFactory {
	if webFactoryHook != nil {
		return webFactoryHook
	}
	return productionWebFactory
}

// daemonLogMaxBytes / daemonLogMaxBackups bound the rotating
// daemon.log so a long-running marunage web does not eat the disk.
// 8 MiB × 5 backups ≈ 40 MiB upper bound, generous for human-rate
// access traffic and tiny next to a typical Claude session log.
const (
	daemonLogMaxBytes   = 8 * 1024 * 1024
	daemonLogMaxBackups = 5
)

// productionWebFactory binds the listener up-front so a misconfigured
// addr (port already in use, invalid bind) fails the CLI at the
// factory step — before Run swallows the error inside the goroutine.
// It also opens the rotating daemon.log writer next to config.toml
// so every request emits a JSON-Lines access record (brief
// requirement: "各リクエストのログを daemon.log に JSON Lines").
// The returned closer releases the listener and flushes the log
// regardless of whether Run completes cleanly.
func productionWebFactory(_ context.Context, opts WebFactoryOptions) (webRunner, func() error, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("web: load %s: %w", opts.ConfigPath, err)
	}
	dbPath, err := expandHome(cfg.Core.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("web: resolve core.db_path %q: %w", cfg.Core.DBPath, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("web: open %s: %w", dbPath, err)
	}

	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("web: listen %s: %w", opts.Addr, err)
	}

	logPath := daemonLogPathFor(opts.ConfigPath)
	rot, err := logging.NewRotatingFile(logPath, daemonLogMaxBytes, daemonLogMaxBackups)
	if err != nil {
		_ = listener.Close()
		_ = db.Close()
		return nil, nil, fmt.Errorf("web: open daemon log %s: %w", logPath, err)
	}
	logger := logging.NewLogger(rot, logging.LevelInfo)

	registry := buildWebSourceRegistry(cfg.Discovery.SourcesEnabled)
	// NewSQLDashboardStore returns the *sqlDashboardStore concrete type
	// wrapped in a DashboardStore interface.  The same concrete value also
	// implements TaskDetailStore (it has a TaskDetail method), so we
	// unwrap it via type assertion to avoid opening a second *sql.DB
	// connection just for task detail reads.
	dashboardStore := web.NewSQLDashboardStore(db)
	dashboardProvider := web.NewDashboardProvider(
		dashboardStore,
		web.RegistrySourceLister{Registry: registry},
		web.DashboardOptions{},
	)
	taskDetailStore, ok := dashboardStore.(web.TaskDetailStore)
	if !ok {
		// This is a programming error: sqlDashboardStore must implement
		// TaskDetailStore.  A compile-time check would be cleaner but
		// NewSQLDashboardStore returns the narrower DashboardStore
		// interface.  Fail loudly at startup rather than silently
		// serving 404 for every task detail request.
		return nil, nil, fmt.Errorf("web: sqlDashboardStore does not implement TaskDetailStore — wiring is broken")
	}
	auditReader := web.NewFileAuditReader(auditLogPathFor(opts.ConfigPath))
	taskDetailProvider := web.NewTaskDetailProvider(taskDetailStore, auditReader)
	taskRepo := store.NewTaskRepo(db)
	reviewProvider := web.NewReviewProvider(taskRepo)
	taskOps := web.NewSQLTaskOpsStore(db)
	liveStreamer := &cmuxClientStreamer{client: cmux.NewClient()}
	liveProvider := &sqlLiveStreamProvider{store: taskDetailStore}

	srv, err := web.NewServer(web.Options{
		AccessLogger: slogAccessLogger{logger: logger},
		Dashboard:    dashboardProvider,
		TaskDetail:   taskDetailProvider,
		AuditLog:     auditReader,
		Review:       reviewProvider,
		TaskOps:      taskOps,
		LiveStream: web.LiveStreamConfig{
			Streamer: liveStreamer,
			Provider: liveProvider,
		},
		// Production CSRF entropy + 30s SSE heartbeat are the
		// zero-value defaults inside web.NewServer; explicitly
		// listing them here would just add noise.
	})
	if err != nil {
		_ = rot.Close()
		_ = listener.Close()
		_ = db.Close()
		return nil, nil, err
	}

	closer := func() error {
		// Closing an already-closed listener returns "use of closed
		// network connection" — swallow it so the cleanup path is
		// idempotent and safe to call after a normal shutdown.
		_ = listener.Close()
		_ = db.Close()
		return rot.Close()
	}
	return &serverRunner{srv: srv, listener: listener}, closer, nil
}

// daemonLogPathFor mirrors auditLogPathFor: derives the daemon log
// location from the active config path so --config overrides flow
// through to the access trail.  configPath is always non-empty in
// production (the CLI's persistent flag preloads defaultConfigPath),
// so no nil-guard is needed here.
func daemonLogPathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "logs", "daemon.log")
}

// buildWebSourceRegistry assembles the source.Registry the web
// dashboard's source-status panel reads from. Every known built-in
// whose name appears in discovery.sources_enabled is registered so the
// dashboard can show its auth status. Unknown names are skipped
// silently (lenient=true): the operator-facing surface for that error
// is the discover command, not the dashboard.
func buildWebSourceRegistry(enabled []string) *source.Registry {
	r := source.NewRegistry()
	for _, name := range enabled {
		_ = registerBuiltin(r, name, config.Config{}, nil, true)
	}
	return r
}

// slogAccessLogger adapts a slog.Logger to web.AccessLogger so
// AccessRecord values land as one JSON object per line, matching the
// daemon.log convention the rest of marunage already uses.
type slogAccessLogger struct {
	logger *logging.Logger
}

func (s slogAccessLogger) Log(rec web.AccessRecord) {
	s.logger.Info("web.access",
		"method", rec.Method,
		"path", rec.Path,
		"status", rec.Status,
		"duration_ms", rec.Duration.Milliseconds(),
	)
}

// serverRunner adapts the (server, listener) pair to webRunner.  The
// Serve method already handles graceful shutdown when ctx is done.
type serverRunner struct {
	srv      *web.Server
	listener net.Listener
}

func (r *serverRunner) Run(ctx context.Context) error {
	return r.srv.Serve(ctx, r.listener)
}

// newWebCmd builds `marunage web [--bind <host>] [--port <port>]
// [--remote]` per docs/requirement.md "Web UI" + the PR-62 brief.
//
// Flag precedence: CLI flags override [web] from --config; --remote
// further overrides --bind to 0.0.0.0 because the brief makes the
// opt-in semantics non-negotiable ("明示しないと外部公開しない").
func newWebCmd(configPath *string) *cobra.Command {
	var (
		bind   string
		port   int
		remote bool
	)

	cmd := &cobra.Command{
		Use:          "web",
		Short:        "Start the local Web UI (defaults to 127.0.0.1:7777).",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}

			effectiveBind := cfg.Web.Bind
			if cmd.Flags().Changed("bind") {
				effectiveBind = bind
			}
			effectivePort := cfg.Web.Port
			if cmd.Flags().Changed("port") {
				effectivePort = port
			}
			// CLI flag wins over [web].remote in either direction
			// (just like --bind / --port) so an operator can flip
			// behaviour without editing config.toml.  Without the
			// Changed() check the boolean OR would forever stick
			// remote=true after the config sets it once.
			effectiveRemote := cfg.Web.Remote
			if cmd.Flags().Changed("remote") {
				effectiveRemote = remote
			}
			if effectiveRemote {
				// Brief: --remote opts into 0.0.0.0 binding; auth
				// itself is a separate PR.  Override the bind
				// regardless of what --bind / [web] said so the
				// behaviour matches the user's intent.
				effectiveBind = "0.0.0.0"
			}
			addr := net.JoinHostPort(effectiveBind, strconv.Itoa(effectivePort))

			runner, closer, err := activeWebFactory()(cmd.Context(), WebFactoryOptions{
				Addr:       addr,
				ConfigPath: *configPath,
			})
			if err != nil {
				return err
			}
			defer func() {
				if closer != nil {
					_ = closer()
				}
			}()

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if effectiveRemote {
				// Loud, multi-line stderr banner: --remote opens
				// the dashboard to the network without auth (auth
				// itself ships in a later PR).  Emitted after the
				// factory has already bound 0.0.0.0 but before
				// Serve starts processing requests, so the operator
				// has a clear signal to ^C if they meant loopback.
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: --remote binds 0.0.0.0 with no authentication.")
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: anyone reachable on this network can read the dashboard and SSE stream.")
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: front this with a TLS-terminating reverse proxy + auth before exposing publicly.")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "marunage web listening on http://%s\n", addr)
			return runner.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&bind, "bind", "", "Host or IP to bind (overrides web.bind from --config; defaults to 127.0.0.1).")
	cmd.Flags().IntVar(&port, "port", 0, "TCP port to listen on (overrides web.port from --config; defaults to 7777).")
	cmd.Flags().BoolVar(&remote, "remote", false, "Bind to 0.0.0.0 to publish externally (auth lands in a later PR).")

	return cmd
}
