package web

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// JournalOptions configures NewSQLJournalProvider. Zero values fall back to
// production defaults so callers only need to set the knobs they care about.
type JournalOptions struct {
	// Now is the clock used to determine today's date when date is empty.
	// Zero defaults to time.Now. Tests inject a fixed time to avoid flaky
	// failures when real time drifts past midnight.
	Now func() time.Time
}

// sqlJournalProvider is the production JournalProvider backed by SQLite.
type sqlJournalProvider struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLJournalProvider(db *sql.DB, opts ...JournalOptions) JournalProvider {
	now := time.Now
	if len(opts) > 0 && opts[0].Now != nil {
		now = opts[0].Now
	}
	return &sqlJournalProvider{db: db, now: now}
}

func (p *sqlJournalProvider) JournalSnapshot(ctx context.Context, date string) (JournalSnapshot, error) {
	if date == "" {
		date = p.now().UTC().Format("2006-01-02")
	}

	const q = `
		SELECT
		  strftime('%H:%M', completed_at) AS time,
		  source,
		  COALESCE(NULLIF(result_summary, ''), title) AS summary
		FROM tasks
		WHERE status IN (?, ?) AND completed_at IS NOT NULL AND date(completed_at) = ?
		ORDER BY completed_at ASC`

	rows, err := p.db.QueryContext(ctx, q, store.StatusDone, store.StatusFailed, date)
	if err != nil {
		return JournalSnapshot{}, fmt.Errorf("journal: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries := []JournalEntry{}
	for rows.Next() {
		var e JournalEntry
		if err := rows.Scan(&e.Time, &e.Source, &e.Summary); err != nil {
			return JournalSnapshot{}, fmt.Errorf("journal: scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return JournalSnapshot{}, fmt.Errorf("journal: rows: %w", err)
	}

	return JournalSnapshot{Date: date, Entries: entries}, nil
}
