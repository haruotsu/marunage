package cli

import (
	"context"
	"fmt"
	"net"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/web"
)

// webRunner is the narrow surface newWebCmd needs from the assembled
// web.Server.  Keeping it as an interface is the test seam: production
// wires the concrete *web.Server, tests inject a fake via
// withWebFactory.  The single Run(ctx) method is intentionally a
// subset of *web.Server so the concrete type satisfies it implicitly
// once we wrap ListenAndServe in the factory.
type webRunner interface {
	Run(ctx context.Context) error
}

// WebFactoryOptions is the resolved input the CLI hands to the web
// factory: the effective addr (after flag/config/--remote
// precedence).  --remote awareness lives in the CLI layer (it
// rewrites Addr to 0.0.0.0 + emits the warning banner) so the
// factory does not need to know about it; if a later PR needs the
// flag to switch behaviour (e.g. auth wiring) we can re-add the
// field with a real consumer.
type WebFactoryOptions struct {
	Addr string
}

// webFactory builds a webRunner from the resolved options.  Mirrors
// dispatcherFactory's hook+override convention so both CLI files
// follow the same idioms.
type webFactory func(ctx context.Context, opts WebFactoryOptions) (webRunner, error)

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

// productionWebFactory binds the listener up-front so a misconfigured
// addr (port already in use, invalid bind) fails the CLI at the
// factory step — before Run swallows the error inside the goroutine.
// The returned webRunner closes over the ready listener and serves
// from the assembled web.Server.
func productionWebFactory(_ context.Context, opts WebFactoryOptions) (webRunner, error) {
	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("web: listen %s: %w", opts.Addr, err)
	}
	srv, err := web.NewServer(web.Options{
		// Production CSRF entropy + 30s SSE heartbeat are the
		// zero-value defaults inside web.NewServer; explicitly
		// listing them here would just add noise.
	})
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	return &serverRunner{srv: srv, listener: listener}, nil
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

			runner, err := activeWebFactory()(cmd.Context(), WebFactoryOptions{
				Addr: addr,
			})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if effectiveRemote {
				// Loud, multi-line stderr banner: --remote opens
				// the dashboard to the network without auth (auth
				// itself ships in a later PR).  Operators must see
				// this before the listener starts so they can abort
				// if they meant to bind loopback.
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
