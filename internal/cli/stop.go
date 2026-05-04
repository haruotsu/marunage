package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/store"
)

const stopReason = "stopped by marunage stop"

// StopStore is the store surface the stop command needs.
type StopStore interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
	Get(ctx context.Context, id int64) (store.Task, error)
	MarkFailedWithReason(ctx context.Context, id int64, reason string) error
}

// WorkspaceStopper sends a stop signal (Ctrl-C) to a cmux workspace.
type WorkspaceStopper interface {
	Stop(ctx context.Context, workspaceID string) error
}

type stopDepsFactory func(ctx context.Context, configPath string) (StopStore, WorkspaceStopper, config.Auditor, func() error, error)

var stopDepsFactoryHook stopDepsFactory

func withStopDepsFactory(t interface{ Cleanup(func()) }, f stopDepsFactory) {
	prev := stopDepsFactoryHook
	stopDepsFactoryHook = f
	t.Cleanup(func() { stopDepsFactoryHook = prev })
}

func activeStopDepsFactory() stopDepsFactory {
	if stopDepsFactoryHook != nil {
		return stopDepsFactoryHook
	}
	return productionStopDepsFactory
}

func productionStopDepsFactory(_ context.Context, configPath string) (StopStore, WorkspaceStopper, config.Auditor, func() error, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load %s: %w", configPath, err)
	}
	dbPath, err := expandHome(cfg.Core.DBPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("resolve core.db_path %q: %w", cfg.Core.DBPath, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	repo := store.NewTaskRepo(db)
	cm := cmux.NewClient()

	auditPath := filepath.Join(filepath.Dir(dbPath), "logs", "audit.log")
	var auditor config.Auditor = config.NopAuditor{}
	if al, alErr := logging.NewAuditLog(auditPath); alErr == nil {
		auditor = al
	}

	closer := func() error {
		if al, ok := auditor.(*logging.AuditLog); ok {
			_ = al.Close()
		}
		return db.Close()
	}
	return repo, &cmuxWorkspaceStopper{client: cm}, auditor, closer, nil
}

type cmuxWorkspaceStopper struct {
	client cmux.Client
}

func (c *cmuxWorkspaceStopper) Stop(ctx context.Context, workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	return c.client.Send(ctx, cmux.Workspace{ID: workspaceID}, "\x03")
}

func newStopCmd(configPath *string) *cobra.Command {
	var (
		stopAll bool
		taskID  int64
	)
	cmd := &cobra.Command{
		Use:          "stop [--all | --task <id>]",
		Short:        "Force-stop one or all running tasks.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !stopAll && !cmd.Flags().Changed("task") {
				return fmt.Errorf("stop: specify --all to stop all running tasks or --task <id> for a single task")
			}
			if cmd.Flags().Changed("task") && taskID <= 0 {
				return fmt.Errorf("stop: --task requires a positive integer id")
			}

			ss, ws, auditor, closer, err := activeStopDepsFactory()(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			defer func() { _ = closer() }()

			if stopAll {
				return runStopAll(cmd, ss, ws, auditor)
			}
			return runStopOne(cmd, taskID, ss, ws, auditor)
		},
	}
	cmd.Flags().BoolVar(&stopAll, "all", false, "Stop all running tasks.")
	cmd.Flags().Int64Var(&taskID, "task", 0, "Stop a specific task by ID.")
	return cmd
}

func runStopAll(cmd *cobra.Command, ss StopStore, ws WorkspaceStopper, auditor config.Auditor) error {
	tasks, err := ss.List(cmd.Context(), store.ListFilter{Statuses: []string{store.StatusRunning}})
	if err != nil {
		return fmt.Errorf("list running tasks: %w", err)
	}
	var errCount, count int
	for _, t := range tasks {
		if err := stopOneTask(cmd.Context(), t, ss, ws, auditor); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "stop task #%d: %v\n", t.ID, err)
			errCount++
			continue
		}
		count++
	}
	fmt.Fprintf(cmd.OutOrStdout(), "stopped %d task(s)\n", count)
	if errCount > 0 && count == 0 {
		return errTaskCommandFailed
	}
	return nil
}

func runStopOne(cmd *cobra.Command, id int64, ss StopStore, ws WorkspaceStopper, auditor config.Auditor) error {
	t, err := ss.Get(cmd.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			printNotFoundAndExit(cmd, id)
			return errTaskCommandFailed
		}
		return err
	}
	if t.Status != store.StatusRunning {
		return fmt.Errorf("task #%d: status is %q; only running tasks can be stopped", t.ID, t.Status)
	}
	if err := stopOneTask(cmd.Context(), t, ss, ws, auditor); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Task #%d stopped.\n", t.ID)
	return nil
}

func stopOneTask(ctx context.Context, t store.Task, ss StopStore, ws WorkspaceStopper, auditor config.Auditor) error {
	if t.WS != "" {
		if err := ws.Stop(ctx, t.WS); err != nil {
			return fmt.Errorf("send stop signal: %w", err)
		}
	}
	if err := ss.MarkFailedWithReason(ctx, t.ID, stopReason); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	auditor.Record(config.AuditEvent{
		Action: "task.stop",
		Key:    "task:" + strconv.FormatInt(t.ID, 10),
		Value:  stopReason,
	})
	return nil
}
