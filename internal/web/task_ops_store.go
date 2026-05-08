package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// sqlTaskOpsStore is the production TaskOpsStore implementation.
// It issues direct SQL against the shared *sql.DB (the same pattern
// sqlDashboardStore uses) rather than going through TaskRepo so task_ops.go
// can evolve its write shape without touching the canonical read/write repo.
type sqlTaskOpsStore struct {
	db *sql.DB
}

// NewSQLTaskOpsStore returns the production TaskOpsStore.
func NewSQLTaskOpsStore(db *sql.DB) TaskOpsStore {
	return &sqlTaskOpsStore{db: db}
}

// Dispatch transitions a pending task to running and stamps started_at.
// Uses a conditional UPDATE so the state check and the write are atomic.
// After RowsAffected == 0 it probes for existence to distinguish 404 vs 409.
func (s *sqlTaskOpsStore) Dispatch(ctx context.Context, id int64) error {
	const q = `UPDATE tasks
		SET status = 'running',
		    started_at = strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'
		WHERE id = ? AND status = 'pending'`
	res, err := s.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("task ops dispatch: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("task ops dispatch rows: %w", err)
	}
	if n == 1 {
		return nil
	}
	return s.probeExistence(ctx, id)
}

// Promote transitions a skipped task back to pending.
func (s *sqlTaskOpsStore) Promote(ctx context.Context, id int64) error {
	const q = `UPDATE tasks SET status = 'pending' WHERE id = ? AND status = 'skipped'`
	res, err := s.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("task ops promote: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("task ops promote rows: %w", err)
	}
	if n == 1 {
		return nil
	}
	return s.probeExistence(ctx, id)
}

// Reopen transitions a done or failed task back to pending.
func (s *sqlTaskOpsStore) Reopen(ctx context.Context, id int64) error {
	const q = `UPDATE tasks SET status = 'pending' WHERE id = ? AND status IN ('done', 'failed')`
	res, err := s.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("task ops reopen: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("task ops reopen rows: %w", err)
	}
	if n == 1 {
		return nil
	}
	return s.probeExistence(ctx, id)
}

// Add inserts a new manual task with source="manual" and status="pending".
// cwd is stored as-is; an empty string maps to SQL NULL.
func (s *sqlTaskOpsStore) Add(ctx context.Context, title, body, cwd string, priority int) (int64, error) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	const q = `INSERT INTO tasks
		(source, title, body, cwd, status, priority, created_at, updated_at)
		VALUES ('manual', ?, ?, ?, 'pending', ?, ?, ?)`
	res, err := s.db.ExecContext(ctx, q, title, nullableStr(body), nullableStr(cwd), priority, now, now)
	if err != nil {
		return 0, fmt.Errorf("task ops add: %w", err)
	}
	return res.LastInsertId()
}

// UpdatePriority changes the priority field of an existing task.
// Returns ErrTaskNotFound when the row does not exist.
func (s *sqlTaskOpsStore) UpdatePriority(ctx context.Context, id int64, priority int) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE tasks SET priority = ? WHERE id = ?", priority, id)
	if err != nil {
		return fmt.Errorf("task ops update priority: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("task ops update priority rows: %w", err)
	}
	if n == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// Delete removes a task row regardless of its current status.
// Returns ErrTaskNotFound when the row does not exist.
func (s *sqlTaskOpsStore) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("task ops delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("task ops delete rows: %w", err)
	}
	if n == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// probeExistence checks whether a row with the given id exists.
// Used after a conditional UPDATE reports RowsAffected == 0 to disambiguate
// "task not found" (404) from "task exists but wrong status" (409).
func (s *sqlTaskOpsStore) probeExistence(ctx context.Context, id int64) error {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tasks WHERE id = ?", id).Scan(&count)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task ops probe: %w", err)
	}
	if count == 0 {
		return ErrTaskNotFound
	}
	return ErrTaskInvalidTransition
}

// nullableStr converts an empty string to nil so the SQL driver stores NULL
// rather than an empty string. Mirrors the same helper in dashboard_store.go.
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
