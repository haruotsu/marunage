package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/permission"
	"github.com/haruotsu/marunage/internal/store"
)

// workspaceDirs is the production WorkspaceDirs the dispatcher and the
// PR-43 completion watcher share. Rooted at ~/.marunage/workspaces so
// task <id>'s sentinel lives at ~/.marunage/workspaces/<id>/.exit_code,
// matching docs/requirement.md ファイルレイアウト.
type workspaceDirs struct {
	root string
}

func (w workspaceDirs) Dir(id int64) string {
	return filepath.Join(w.root, strconv.FormatInt(id, 10))
}

// dispatchRunner is the narrow surface newDispatchCmd needs from the
// dispatcher. Keeping it as an interface is the test seam: production
// wires the concrete *dispatch.Dispatcher, tests inject a fake via
// withDispatcherFactory. The method set is intentionally a subset of
// *dispatch.Dispatcher so the concrete type satisfies it implicitly.
type dispatchRunner interface {
	Run(ctx context.Context, opts dispatch.RunOptions) error
}

// dispatcherFactory builds a dispatchRunner from the resolved configPath
// and returns a closer the caller must run when done. Factory shape
// mirrors taskRepoFactory so the hook + override conventions are uniform
// across CLI files.
type dispatcherFactory func(ctx context.Context, configPath string) (dispatchRunner, func() error, error)

// dispatcherFactoryHook is the package-private slot tests use via
// withDispatcherFactory; production callers see nil and fall through to
// productionDispatcherFactory.
var dispatcherFactoryHook dispatcherFactory

func withDispatcherFactory(t interface{ Cleanup(func()) }, f dispatcherFactory) {
	prev := dispatcherFactoryHook
	dispatcherFactoryHook = f
	t.Cleanup(func() { dispatcherFactoryHook = prev })
}

func activeDispatcherFactory() dispatcherFactory {
	if dispatcherFactoryHook != nil {
		return dispatcherFactoryHook
	}
	return productionDispatcherFactory
}

// productionDispatcherFactory loads the config, opens the SQLite store,
// builds a real cmux client, and assembles a Dispatcher. The closer is
// the DB Close — cmux holds no long-lived resources, so its lifecycle
// piggy-backs on the DB.
//
// PR-42 deliberately reads the base skill from a constant string here
// (see baseExecutionSkill below) rather than from disk: the skills/ tree
// itself is PR-34's responsibility. When PR-34 lands, this factory will
// switch to reading ~/.claude/skills/marunage-execute/SKILL.md, but the
// fallback string here keeps `marunage dispatch` runnable end-to-end
// during PR-42 + PR-43 development without depending on a separate PR.
func productionDispatcherFactory(_ context.Context, configPath string) (dispatchRunner, func() error, error) {
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
	cm := newWorkspaceClient(cfg, true)

	// Open the audit log alongside the SQLite store so requirement.md
	// invariant #2 "No silent execution" is honoured for every dispatch.
	// A best-effort failure (e.g. the parent dir is not writable) falls
	// back to NopAuditor rather than blocking dispatch entirely — losing
	// audit visibility is bad, but dropping the queue would be worse.
	auditPath := filepath.Join(filepath.Dir(dbPath), "logs", "audit.log")
	var auditor config.Auditor = config.NopAuditor{}
	if al, alErr := logging.NewAuditLog(auditPath); alErr == nil {
		auditor = al
	}

	// Workspaces live alongside tasks.db so a single ~/.marunage tree
	// holds the queue (tasks.db) and the per-task control directories
	// (workspaces/<id>/) the PR-43 completion watcher polls.
	//
	// Pre-create the parent root with 0o700 so per-task subdirs inherit
	// the tight permission.
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

	d, err := dispatch.New(
		dispatch.WithStore(repo),
		dispatch.WithCmux(cm),
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
	closer := func() error {
		if al, ok := auditor.(*logging.AuditLog); ok {
			_ = al.Close()
		}
		return db.Close()
	}
	return d, closer, nil
}

// baseExecutionSkill is the placeholder text PR-42 ships before PR-34
// lands the real ~/.claude/skills/marunage-execute/SKILL.md. The
// dispatched Claude session reads this as the high-level guardrail
// section of every prompt; PR-34 will replace this constant with a disk
// read at the factory call site.
const baseExecutionSkill = "" +
	"You are a marunage worker session. Carry out the task described below.\n" +
	"Read CLAUDE.md / docs / configuration before acting.\n" +
	"Prefer minimal, reversible changes; surface uncertainty back to the user.\n"

// newDispatchCmd builds `marunage dispatch [<id>] [--max-parallel N]`
// per docs/requirement.md "コマンド `marunage`" row "dispatch".
func newDispatchCmd(configPath *string) *cobra.Command {
	var maxParallel int

	cmd := &cobra.Command{
		Use:          "dispatch [<id>]",
		Short:        "Dispatch one or more pending tasks into cmux/Claude sessions.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var id int64
			if len(args) == 1 {
				parsed, err := parseTaskID(args[0])
				if err != nil {
					return err
				}
				id = parsed
			}

			cfg, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}
			effectiveMax := cfg.Core.MaxParallel
			if cmd.Flags().Changed("max-parallel") {
				effectiveMax = maxParallel
			}

			d, closer, err := activeDispatcherFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			if err := d.Run(cmd.Context(), dispatch.RunOptions{
				MaxParallel: effectiveMax,
				ID:          id,
			}); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0,
		"Maximum number of pending tasks to dispatch in parallel (overrides core.max_parallel).")

	return cmd
}
