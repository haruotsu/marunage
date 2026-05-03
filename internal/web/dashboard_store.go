package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// sqlDashboardStore is the production DashboardStore implementation.
// It runs targeted SELECTs against the same SQLite *sql.DB the
// TaskRepo / KVStateRepo own, so reads cost one round-trip per panel
// rather than rehydrating Task / KVEntry slices into Go.
//
// Why direct SQL instead of layering on TaskRepo / KVStateRepo:
//
//   - The dashboard's aggregations (24h status×source breakdown,
//     `<source>_*` checkpoint scan) do not exist on the existing
//     repos and would otherwise pull every row into memory.
//   - Keeping the queries here means PR-63's read shape can evolve
//     without churning tasks.go (which other parallel PRs are
//     touching) — the repo stays the canonical write path.
type sqlDashboardStore struct {
	db *sql.DB
}

// NewSQLDashboardStore returns the production DashboardStore.
func NewSQLDashboardStore(db *sql.DB) DashboardStore {
	return &sqlDashboardStore{db: db}
}

// runningTimestampLayout matches the millisecond ISO8601 layout
// store.formatTime emits. Reproduced here (rather than imported from
// store) so this file does not pull a cross-package private helper —
// the layout itself is documented in store/store.go.
const runningTimestampLayout = "2006-01-02T15:04:05.000Z"

func parseStoreTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(runningTimestampLayout, s)
}

func formatStoreTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(runningTimestampLayout)
}

func (s *sqlDashboardStore) Running(ctx context.Context, limit, previewBytes int) ([]DashboardRunning, error) {
	const q = `SELECT id, source, title, COALESCE(ws, ''), COALESCE(started_at, ''), COALESCE(body, '')
		FROM tasks
		WHERE status = 'running'
		ORDER BY started_at ASC, id ASC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("running query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DashboardRunning
	for rows.Next() {
		var r DashboardRunning
		var startedAt, body string
		if err := rows.Scan(&r.ID, &r.Source, &r.Title, &r.WS, &startedAt, &body); err != nil {
			return nil, fmt.Errorf("running scan: %w", err)
		}
		t, err := parseStoreTime(startedAt)
		if err != nil {
			return nil, fmt.Errorf("running parse started_at: %w", err)
		}
		r.StartedAt = t
		r.OutputPreview = truncatePreview(body, previewBytes)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("running rows: %w", err)
	}
	return out, nil
}

func (s *sqlDashboardStore) PendingTop(ctx context.Context, limit int) ([]DashboardPending, error) {
	const q = `SELECT id, source, title, priority, created_at
		FROM tasks
		WHERE status = 'pending'
		ORDER BY priority DESC, created_at ASC, id ASC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("pending top query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DashboardPending
	for rows.Next() {
		var r DashboardPending
		var createdAt string
		if err := rows.Scan(&r.ID, &r.Source, &r.Title, &r.Priority, &createdAt); err != nil {
			return nil, fmt.Errorf("pending top scan: %w", err)
		}
		t, err := parseStoreTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("pending top parse created_at: %w", err)
		}
		r.CreatedAt = t
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending top rows: %w", err)
	}
	return out, nil
}

func (s *sqlDashboardStore) PendingCount(ctx context.Context) (int, error) {
	const q = `SELECT COUNT(*) FROM tasks WHERE status = 'pending'`
	var n int
	if err := s.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("pending count: %w", err)
	}
	return n, nil
}

func (s *sqlDashboardStore) Recent(ctx context.Context, since time.Time) (DashboardRecent, error) {
	// done/failed transitions stamp completed_at; skipped uses
	// updated_at (no skipped_at column exists, and Skipped is
	// terminal so updated_at on the skip transition is the
	// effective entry time).
	const q = `
		SELECT
		  source,
		  status,
		  COUNT(*)
		FROM tasks
		WHERE
		  (status IN ('done', 'failed') AND completed_at IS NOT NULL AND completed_at >= ?)
		  OR (status = 'skipped' AND updated_at >= ?)
		GROUP BY source, status`
	cutoff := formatStoreTime(since.UTC())
	rows, err := s.db.QueryContext(ctx, q, cutoff, cutoff)
	if err != nil {
		return DashboardRecent{}, fmt.Errorf("recent query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	totals := DashboardRecent{}
	bySource := map[string]*DashboardSourceCount{}
	for rows.Next() {
		var (
			src    string
			status string
			n      int
		)
		if err := rows.Scan(&src, &status, &n); err != nil {
			return DashboardRecent{}, fmt.Errorf("recent scan: %w", err)
		}
		bucket, ok := bySource[src]
		if !ok {
			bucket = &DashboardSourceCount{Source: src}
			bySource[src] = bucket
		}
		switch status {
		case "done":
			totals.DoneCount += n
			bucket.Done += n
		case "failed":
			totals.FailedCount += n
			bucket.Failed += n
		case "skipped":
			totals.SkippedCount += n
			bucket.Skipped += n
		}
	}
	if err := rows.Err(); err != nil {
		return DashboardRecent{}, fmt.Errorf("recent rows: %w", err)
	}

	names := make([]string, 0, len(bySource))
	for k := range bySource {
		names = append(names, k)
	}
	sort.Strings(names)
	totals.BySource = make([]DashboardSourceCount, 0, len(names))
	for _, name := range names {
		totals.BySource = append(totals.BySource, *bySource[name])
	}
	return totals, nil
}

func (s *sqlDashboardStore) SourceCheckpoints(ctx context.Context) (map[string]time.Time, error) {
	const q = `SELECT key, updated_at FROM kv_state`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("source checkpoints query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]time.Time{}
	for rows.Next() {
		var key, updatedAt string
		if err := rows.Scan(&key, &updatedAt); err != nil {
			return nil, fmt.Errorf("source checkpoints scan: %w", err)
		}
		// kv_state keys follow the convention `<source>_<rest>`
		// (gmail_last_id, slack_last_ts, ...). Anything without
		// a `_` is left out — it cannot be ascribed to a known
		// source and would otherwise produce an empty-string
		// row in the dashboard.
		idx := strings.Index(key, "_")
		if idx <= 0 {
			continue
		}
		source := key[:idx]
		t, err := parseStoreTime(updatedAt)
		if err != nil {
			return nil, fmt.Errorf("source checkpoints parse updated_at: %w", err)
		}
		// Keep the most recent checkpoint per source: a plugin
		// may store multiple state rows (e.g. last_id +
		// last_seen), and the dashboard wants the freshest one.
		if existing, ok := out[source]; !ok || t.After(existing) {
			out[source] = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source checkpoints rows: %w", err)
	}
	return out, nil
}
