package cli

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/reaper"
	"github.com/haruotsu/marunage/internal/store"
)

// reaperRunner is the narrow surface newReaperCmd needs from
// *reaper.Reaper. Keeping it as an interface is the test seam:
// production wires the concrete *reaper.Reaper, tests inject a fake via
// withReaperFactory. Subset of *reaper.Reaper so the concrete type
// satisfies it implicitly.
type reaperRunner interface {
	Run(ctx context.Context) error
}

// reaperFactory builds a reaperRunner from the resolved configPath and
// returns a closer the caller must run when done. Mirrors
// dispatcherFactory so the hook + override conventions stay uniform.
type reaperFactory func(ctx context.Context, configPath string) (reaperRunner, func() error, error)

// reaperFactoryHook is the package-private slot tests use via
// withReaperFactory; production callers see nil and fall through to
// productionReaperFactory.
var reaperFactoryHook reaperFactory

func withReaperFactory(t interface{ Cleanup(func()) }, f reaperFactory) {
	prev := reaperFactoryHook
	reaperFactoryHook = f
	t.Cleanup(func() { reaperFactoryHook = prev })
}

func activeReaperFactory() reaperFactory {
	if reaperFactoryHook != nil {
		return reaperFactoryHook
	}
	return productionReaperFactory
}

// productionReaperFactory loads config, opens SQLite + audit.log,
// builds a real cmux client, and assembles a Reaper. The closer is the
// DB Close (cmux holds no long-lived resources, so its lifecycle
// piggy-backs on the DB) plus the audit.log Close so a partial reaper
// pass cannot leak the file handle.
//
// audit.log open failures fall back to NopAuditor: losing audit
// visibility is bad, but skipping the sweep entirely would be worse —
// the orphan rows the reaper would have caught would silently leak
// instead. The doctor / startup wiring should surface the disk problem
// independently.
func productionReaperFactory(_ context.Context, configPath string) (reaperRunner, func() error, error) {
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
	cm := cmux.NewClient()

	// Validate already gates this — this re-parse is just to surface a
	// typed time.Duration to the reaper. Empty / malformed values
	// cannot reach here without Validate having rejected them at Load.
	threshold, err := time.ParseDuration(cfg.Execution.ReaperStuckThreshold)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("parse execution.reaper_stuck_threshold %q: %w",
			cfg.Execution.ReaperStuckThreshold, err)
	}

	auditPath := filepath.Join(filepath.Dir(dbPath), "logs", "audit.log")
	var auditor config.Auditor = config.NopAuditor{}
	if al, alErr := logging.NewAuditLog(auditPath); alErr == nil {
		auditor = al
	} else {
		// requirement.md invariant #2 "No silent execution": never let
		// reaper run without surfacing the lost audit channel. We still
		// fall back to NopAuditor so the orphan rows the sweep would
		// catch are not stranded by the same disk problem, but the
		// operator (or daemon log scraper) gets a structured warn here.
		slog.Warn("reaper: audit.log open failed; continuing without audit",
			"path", auditPath, "err", alErr)
	}

	r, err := reaper.New(
		reaper.WithStore(repo),
		reaper.WithCmux(cm),
		reaper.WithStuckThreshold(threshold),
		reaper.WithAuditor(auditor),
	)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("build reaper: %w", err)
	}
	closer := func() error {
		if al, ok := auditor.(*logging.AuditLog); ok {
			_ = al.Close()
		}
		return db.Close()
	}
	return r, closer, nil
}

// newReaperCmd builds `marunage reaper`. PR-44's responsibility is
// detecting orphan workspaces and 24h-stuck running rows; loop /
// daemon scheduling is PR-71's job.
func newReaperCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "reaper",
		Short: "Mark rows whose cmux workspace disappeared as failed and warn on 24h-stuck running.",
		Long: "marunage reaper runs one orphan-recovery sweep:\n" +
			"  - status=running rows whose ws is not in `cmux list-workspaces`\n" +
			"    transition to failed with judgment_reason='workspace disappeared (reaper)'.\n" +
			"  - status=running rows whose started_at is older than\n" +
			"    execution.reaper_stuck_threshold (default 24h) get a\n" +
			"    '[reaper] stuck running over <threshold>' note appended to\n" +
			"    judgment_reason and an audit warn — status is left for human judgement.\n" +
			"\n" +
			"Note: `marunage clean` is a separate, narrower utility that only nulls\n" +
			"the ws column of orphan rows without touching status (see PR-22).",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, closer, err := activeReaperFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()
			return r.Run(cmd.Context())
		},
	}
}
