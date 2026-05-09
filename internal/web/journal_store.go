package web

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

type sqlJournalProvider struct {
	db *sql.DB
}

// NewSQLJournalProvider returns a JournalProvider backed by db.
func NewSQLJournalProvider(db *sql.DB) JournalProvider {
	return &sqlJournalProvider{db: db}
}

func (p *sqlJournalProvider) JournalSnapshot(ctx context.Context, date string) (JournalSnapshot, error) {
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
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
