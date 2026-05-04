package cli

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// openTestDB opens the SQLite file at path using the same store.Open
// the production factory uses, which ensures all migrations have run.
// The returned *sql.DB must be closed by the caller.
func openTestDB(t *testing.T, path string) (*sql.DB, error) {
	t.Helper()
	return store.Open(path)
}

// insertMinimalTask inserts the bare-minimum pending task (source +
// title are required by the store invariant) and returns the new row id.
// It leaves all optional fields at their zero values so the test stays
// focused on the wiring concern rather than data completeness.
func insertMinimalTask(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	repo := store.NewTaskRepo(db)
	id, err := repo.Insert(context.Background(), store.Task{
		Source:    "markdown",
		Title:     "test-task-for-detail-wiring",
		Status:    store.StatusPending,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("insertMinimalTask: %v", err)
	}
	return id
}
