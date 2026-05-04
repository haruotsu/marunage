package web

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// MetricsOptions configures NewSQLMetricsProvider. Zero values fall back to
// production defaults so callers only need to set the knobs they care about.
type MetricsOptions struct {
	// Now is the clock the 30-day cutoff in queryDailyCounts derives from.
	// Zero defaults to time.Now. Tests inject a fixed time to avoid flaky
	// failures when real time drifts past the cutoff window.
	Now func() time.Time
}

// sqlMetricsProvider is the production MetricsProvider backed by SQLite.
// It reads from the same tasks table as the dashboard store.
type sqlMetricsProvider struct {
	db  *sql.DB
	now func() time.Time
}

// NewSQLMetricsProvider returns a MetricsProvider backed by db.
func NewSQLMetricsProvider(db *sql.DB, opts ...MetricsOptions) MetricsProvider {
	now := time.Now
	if len(opts) > 0 && opts[0].Now != nil {
		now = opts[0].Now
	}
	return &sqlMetricsProvider{db: db, now: now}
}

func (p *sqlMetricsProvider) Snapshot(ctx context.Context) (MetricsSnapshot, error) {
	byStatus, total, err := p.queryByStatus(ctx)
	if err != nil {
		return MetricsSnapshot{}, err
	}

	bySource, err := p.queryBySource(ctx)
	if err != nil {
		return MetricsSnapshot{}, err
	}

	done := byStatus[store.StatusDone]
	failed := byStatus[store.StatusFailed]
	var successRate float64
	if done+failed > 0 {
		successRate = float64(done) / float64(done+failed)
	}

	avgDuration, err := p.queryAvgDuration(ctx)
	if err != nil {
		return MetricsSnapshot{}, err
	}

	dailyCounts, err := p.queryDailyCounts(ctx)
	if err != nil {
		return MetricsSnapshot{}, err
	}

	return MetricsSnapshot{
		TotalTasks:  total,
		ByStatus:    byStatus,
		BySource:    bySource,
		SuccessRate: successRate,
		AvgDuration: avgDuration,
		DailyCounts: dailyCounts,
	}, nil
}

func (p *sqlMetricsProvider) queryByStatus(ctx context.Context) (map[string]int, int, error) {
	const q = `SELECT status, COUNT(*) FROM tasks GROUP BY status`
	rows, err := p.db.QueryContext(ctx, q)
	if err != nil {
		return nil, 0, fmt.Errorf("metrics: by_status query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byStatus := map[string]int{}
	total := 0
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, 0, fmt.Errorf("metrics: by_status scan: %w", err)
		}
		byStatus[status] = n
		total += n
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("metrics: by_status rows: %w", err)
	}
	return byStatus, total, nil
}

func (p *sqlMetricsProvider) queryBySource(ctx context.Context) (map[string]int, error) {
	const q = `SELECT source, COUNT(*) FROM tasks GROUP BY source`
	rows, err := p.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("metrics: by_source query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	bySource := map[string]int{}
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			return nil, fmt.Errorf("metrics: by_source scan: %w", err)
		}
		bySource[src] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metrics: by_source rows: %w", err)
	}
	return bySource, nil
}

func (p *sqlMetricsProvider) queryAvgDuration(ctx context.Context) (float64, error) {
	const q = `SELECT COALESCE(AVG((julianday(completed_at) - julianday(started_at)) * 86400), 0)
		FROM tasks
		WHERE status = ? AND completed_at IS NOT NULL AND started_at IS NOT NULL`
	var avg float64
	if err := p.db.QueryRowContext(ctx, q, store.StatusDone).Scan(&avg); err != nil {
		return 0, fmt.Errorf("metrics: avg_duration query: %w", err)
	}
	return avg, nil
}

func (p *sqlMetricsProvider) queryDailyCounts(ctx context.Context) ([]MetricsDailyCount, error) {
	cutoff := p.now().UTC().AddDate(0, 0, -30).Format("2006-01-02")
	const q = `
		SELECT
		  date(completed_at) AS day,
		  SUM(CASE WHEN status = ? THEN 1 ELSE 0 END) AS done_count,
		  SUM(CASE WHEN status = ? THEN 1 ELSE 0 END) AS failed_count
		FROM tasks
		WHERE status IN (?, ?) AND completed_at IS NOT NULL AND date(completed_at) >= ?
		GROUP BY day
		ORDER BY day ASC`
	rows, err := p.db.QueryContext(ctx, q,
		store.StatusDone, store.StatusFailed,
		store.StatusDone, store.StatusFailed,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("metrics: daily_counts query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []MetricsDailyCount
	for rows.Next() {
		var dc MetricsDailyCount
		if err := rows.Scan(&dc.Date, &dc.Done, &dc.Failed); err != nil {
			return nil, fmt.Errorf("metrics: daily_counts scan: %w", err)
		}
		out = append(out, dc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metrics: daily_counts rows: %w", err)
	}
	if out == nil {
		out = []MetricsDailyCount{}
	}
	return out, nil
}
