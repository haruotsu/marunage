package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/exec"
	"github.com/haruotsu/marunage/internal/exec/backend"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/loop"
	"github.com/haruotsu/marunage/internal/manage"
	"github.com/haruotsu/marunage/internal/permission"
	"github.com/haruotsu/marunage/internal/reaper"
	"github.com/haruotsu/marunage/internal/render"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
)

// loopRunner is the narrow surface newLoopCmd needs from *loop.Loop.
// Keeping it as an interface is the test seam: production wires the
// concrete *loop.Loop, tests inject a fake via withLoopFactory. Subset
// of *loop.Loop so the concrete type satisfies it implicitly.
type loopRunner interface {
	RunOnce(ctx context.Context) error
	Run(ctx context.Context, interval time.Duration) error
}

// loopFactory builds a loopRunner from the resolved configPath and
// returns a closer the caller must run when done. Mirrors
// dispatcherFactory / reaperFactory so the hook + override conventions
// stay uniform across CLI files.
type loopFactory func(ctx context.Context, configPath string) (loopRunner, func() error, error)

// loopFactoryHook is the package-private slot tests use via
// withLoopFactory; production callers see nil and fall through to
// productionLoopFactory.
var loopFactoryHook loopFactory

func withLoopFactory(t interface{ Cleanup(func()) }, f loopFactory) {
	prev := loopFactoryHook
	loopFactoryHook = f
	t.Cleanup(func() { loopFactoryHook = prev })
}

func activeLoopFactory() loopFactory {
	if loopFactoryHook != nil {
		return loopFactoryHook
	}
	return productionLoopFactory
}

// productionLoopFactory loads config, opens SQLite + audit.log, builds
// the source registry (markdown + every other built-in registered in
// builtins), assembles a Dispatcher mirroring productionDispatcherFactory,
// builds a closure-based render hook, and returns a *loop.Loop. The
// closer chains the audit.log Close + DB Close so a partial loop tick
// cannot leak the file handles.
func productionLoopFactory(_ context.Context, configPath string) (loopRunner, func() error, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: %w", configPath, err)
	}
	dbPath, err := expandHome(cfg.Core.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve core.db_path %q: %w", cfg.Core.DBPath, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	repo := store.NewTaskRepo(db)
	kv := store.NewKVStateRepo(db)
	// Config-driven backend selection (redesign §4/§6): one executor instance
	// serves both dispatch and the reaper's session lister.
	executor, err := backend.New(cfg.Execution.Executor)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("build executor: %w", err)
	}

	auditPath := filepath.Join(filepath.Dir(dbPath), "logs", "audit.log")
	var auditor config.Auditor = config.NopAuditor{}
	if al, alErr := logging.NewAuditLog(auditPath); alErr == nil {
		auditor = al
	}

	wsRoot := filepath.Join(filepath.Dir(dbPath), "workspaces")
	if err := os.MkdirAll(wsRoot, 0o700); err == nil {
		_ = os.Chmod(wsRoot, 0o700)
	}
	dirs := workspaceDirs{root: wsRoot}

	matcher, err := permission.New(cfg.Execution.AutoAcceptTools)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("permission.New: %w", err)
	}

	expandedCwdPrefixes := make([]string, 0, len(cfg.Execution.AllowedCwdPrefixes))
	for _, p := range cfg.Execution.AllowedCwdPrefixes {
		exp, expErr := expandHome(p)
		if expErr != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("resolve allowed_cwd_prefix %q: %w", p, expErr)
		}
		expandedCwdPrefixes = append(expandedCwdPrefixes, exp)
	}

	defaultCwd, err := expandHome(cfg.Core.DefaultCwd)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("resolve core.default_cwd %q: %w", cfg.Core.DefaultCwd, err)
	}

	disp, err := dispatch.New(
		dispatch.WithStore(repo),
		dispatch.WithExecutor(executor),
		dispatch.WithBaseSkill(baseExecutionSkill),
		dispatch.WithClaudeCommand(cfg.Execution.ClaudeCommand),
		dispatch.WithLockKeys(cfg.Execution.LockKeys),
		dispatch.WithAllowedCwdPrefixes(expandedCwdPrefixes),
		dispatch.WithDefaultCwd(defaultCwd),
		dispatch.WithAuditor(auditor),
		dispatch.WithWorkspaceDirs(dirs),
		dispatch.WithPermissionMatcher(matcher),
		dispatch.WithOnUnknownPermission(cfg.Execution.OnUnknownPermission),
		dispatch.WithPermissionMode(cfg.Execution.PermissionMode),
	)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("build dispatcher: %w", err)
	}

	registry := source.NewRegistry()
	if err := registerEnabledSources(registry, cfg); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	viewPath, err := activeViewPath()
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("resolve view path: %w", err)
	}
	rend := &fileRenderer{repo: repo, dest: viewPath}

	threshold, err := time.ParseDuration(cfg.Execution.ReaperStuckThreshold)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("parse execution.reaper_stuck_threshold %q: %w",
			cfg.Execution.ReaperStuckThreshold, err)
	}
	// Orphan recovery needs the session-lister capability. cmux/tmux/herdr
	// provide it; a capability-poor backend (e.g. local) does not. Rather than
	// refuse to run — which would block the "fire-and-forget execution works on
	// any backend" guarantee (redesign §4.2 / §8 PR-R08) — we skip the reaper
	// phase and warn loudly so the lost orphan recovery is never silent.
	loopOpts := []loop.Option{
		loop.WithRegistry(registry),
		loop.WithTaskRepo(repo),
		loop.WithKVStateRepo(kv),
		loop.WithDispatcher(disp),
		loop.WithRender(rend),
		loop.WithAuditor(auditor),
		loop.WithMaxParallel(cfg.Core.MaxParallel),
		loop.WithLockKey("default"),
	}
	if lister, ok := executor.(exec.Lister); ok {
		reap, rerr := reaper.New(
			reaper.WithStore(repo),
			reaper.WithExecutor(lister),
			reaper.WithStuckThreshold(threshold),
			reaper.WithAuditor(auditor),
		)
		if rerr != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("build reaper: %w", rerr)
		}
		loopOpts = append(loopOpts, loop.WithReaper(reap))
	} else {
		fmt.Fprintf(os.Stderr, "warning: orphan recovery (reaper) disabled — executor %q has no session listing\n", cfg.Execution.Executor)
		auditor.Record(config.AuditEvent{Action: "loop.reaper.disabled", Value: cfg.Execution.Executor})
	}
	if cfg.Discovery.DispatchInterval != "" {
		di, diErr := time.ParseDuration(cfg.Discovery.DispatchInterval)
		if diErr != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("parse discovery.dispatch_interval %q: %w",
				cfg.Discovery.DispatchInterval, diErr)
		}
		if di > 0 {
			loopOpts = append(loopOpts, loop.WithDispatchInterval(di))
		}
	}

	// Turn on the collect→manage→persist pipeline when the management layer is
	// enabled (redesign §2/§8 PR-R05). The planner reuses the same cwd
	// allowlist / default cwd / lock_keys the dispatcher enforces so a row the
	// manager clears as ready cannot then fail the dispatcher's cwd gate, and
	// the verdict→status mapping comes from [manage.verdicts] (原則1). When
	// disabled, the loop keeps the legacy discover-and-insert path.
	if cfg.Manage.Enabled {
		manageOpts, moErr := manageOptions(cfg, expandedCwdPrefixes, defaultCwd)
		if moErr != nil {
			_ = db.Close()
			return nil, nil, moErr
		}
		loopOpts = append(loopOpts,
			loop.WithManageStore(repo),
			loop.WithManageOptions(manageOpts...),
		)
	}

	l, err := loop.New(loopOpts...)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("build loop: %w", err)
	}

	closer := func() error {
		if al, ok := auditor.(*logging.AuditLog); ok {
			_ = al.Close()
		}
		return db.Close()
	}
	return l, closer, nil
}

// manageOptions assembles the planner options the loop hands to manage.Plan
// each tick. It is a standalone function (not inlined in buildLoop) so the
// llm_scoring on/off wiring — the core PR-R06 switch — is unit-testable
// without standing up a whole loop.
//
// llm_scoring is the off switch (redesign §9.1). The default is ON
// (config.Default / redesign §6): on injects the Claude-backed scorer, which
// loads the user's marunage-manage SKILL.md and batches one `claude -p` call
// per tick. Off leaves the planner on the deterministic stub scorer —
// byte-identical to PR-R03/R05.
func manageOptions(cfg config.Config, cwdPrefixes []string, defaultCwd string) ([]manage.Option, error) {
	rules, err := manage.RulesFromConfig(cfg.Manage.Rules)
	if err != nil {
		return nil, fmt.Errorf("build manage rules: %w", err)
	}
	opts := []manage.Option{
		manage.WithRules(rules),
		manage.WithRegistry(manage.VerdictPoliciesFromConfig(cfg.Manage.Verdicts)),
		manage.WithAllowedCwdPrefixes(cwdPrefixes),
		manage.WithDefaultCwd(defaultCwd),
		manage.WithLockKeys(cfg.Execution.LockKeys),
	}
	if cfg.Manage.LLMScoring {
		opts = append(opts, manage.WithLLMScorer(manage.NewClaudeScorer(
			manage.WithScorerSkillPath(manageSkillPath()),
		)))
	}
	return opts, nil
}

// manageSkillPath resolves the on-disk marunage-manage SKILL.md the LLM
// scorer prepends to its prompt. Best-effort: an unresolvable HOME yields the
// empty string, and the scorer degrades to its built-in instruction rather
// than failing the loop — installing the skill (`setup --skills`) restores the
// user-customised criteria.
func manageSkillPath() string {
	dir, err := skillsTargetDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "marunage-manage", "SKILL.md")
}

// registerEnabledSources walks discovery.sources_enabled and attaches
// every built-in plugin the user opted into. An enabled source with no
// built-in registrar is treated as a config error rather than a silent
// skip — typoing "markdwn" should fail loud instead of producing an
// empty discover phase.
func registerEnabledSources(r *source.Registry, cfg config.Config) error {
	for _, name := range cfg.Discovery.SourcesEnabled {
		if err := registerBuiltin(r, name, cfg, nil, false); err != nil {
			return fmt.Errorf("loop: %w", err)
		}
	}
	return nil
}

// fileRenderer adapts internal/render.Render + atomicWriteViewFile to
// the loop.Render interface. The struct holds the repository + the
// resolved destination path so the loop tick does not have to recompute
// either on every iteration.
type fileRenderer struct {
	repo taskRepoLister
	dest string
}

// taskRepoLister is the narrow read surface fileRenderer needs against
// the tasks table. *store.TaskRepo satisfies it implicitly. Kept
// unexported because no caller outside internal/cli needs to reference
// it.
type taskRepoLister interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
}

func (r *fileRenderer) Render(ctx context.Context) error {
	rows, err := r.repo.List(ctx, store.ListFilter{})
	if err != nil {
		return fmt.Errorf("loop render: list: %w", err)
	}
	body := render.Render(rows, activeRenderClock())
	parent := filepath.Dir(r.dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("loop render: mkdir: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("loop render: chmod: %w", err)
	}
	if err := atomicWriteViewFile(r.dest, []byte(body)); err != nil {
		return fmt.Errorf("loop render: write: %w", err)
	}
	return nil
}

// newLoopCmd builds `marunage loop`. Three modes:
//
//	--once                  : run RunOnce exactly once and exit.
//	--interval <duration>   : run Run with the parsed duration until
//	                          ctx is cancelled (SIGINT / SIGTERM).
//	bare                    : interval defaults to discovery.interval
//	                          from config (10m by default).
//
// --once and --interval are mutually exclusive: silently picking one
// would leave the operator unsure which mode they were in.
func newLoopCmd(configPath *string) *cobra.Command {
	var (
		once     bool
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "loop",
		Short: "Periodically run discover -> dispatch -> render.",
		Long: "marunage loop drives one OODA iteration per tick:\n" +
			"  - discover: walk the configured Discovery sources and upsert tasks\n" +
			"  - dispatch: invoke the dispatcher honouring core.max_parallel\n" +
			"  - render:   refresh ~/.marunage/view.md\n" +
			"\n" +
			"--once runs a single iteration and exits; --interval keeps ticking\n" +
			"until SIGINT/SIGTERM. With neither flag, the interval defaults to\n" +
			"discovery.interval from config (10m by default).",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			intervalSet := cmd.Flags().Changed("interval")
			if once && intervalSet {
				return fmt.Errorf("--once and --interval are mutually exclusive")
			}

			l, closer, err := activeLoopFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			if once {
				return l.RunOnce(cmd.Context())
			}

			effective := interval
			if !intervalSet {
				cfg, err := config.Load(*configPath)
				if err != nil {
					return fmt.Errorf("load %s: %w", *configPath, err)
				}
				effective, err = time.ParseDuration(cfg.Discovery.Interval)
				if err != nil {
					return fmt.Errorf("parse discovery.interval %q: %w", cfg.Discovery.Interval, err)
				}
			}

			ctx, stop := loopSignalContext(cmd.Context())
			defer stop()
			return l.Run(ctx, effective)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run exactly one iteration and exit.")
	cmd.Flags().DurationVar(&interval, "interval", 0, "Interval between ticks (defaults to discovery.interval).")
	return cmd
}

// loopSignalContext wraps parent in a SIGINT/SIGTERM handler so a
// `marunage loop` foreground invocation responds to Ctrl+C and to the
// daemon's stop signal. The hook lets the loop CLI tests substitute
// their own cancellable ctx without invoking signal.NotifyContext.
var loopSignalContextHook func(parent context.Context) (context.Context, func())

func loopSignalContext(parent context.Context) (context.Context, func()) {
	if loopSignalContextHook != nil {
		return loopSignalContextHook(parent)
	}
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}

// executeForTest threads ctx through cobra so loop tests can cancel a
// long-running Run without resorting to signal injection. The bridge
// mirrors what main.go does in production but adds the SetContext hook.
func executeForTest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetContext(ctx)
	if err := cmd.Execute(); err != nil {
		return 1
	}
	return 0
}
