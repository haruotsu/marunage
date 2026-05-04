package web

import (
	"context"
	"errors"
	"fmt"

	"github.com/haruotsu/marunage/internal/store"
)

// TaskDetailProvider is the seam the task detail handler consumes.
// Production wires an sqlDashboardStore-backed implementation;
// tests inject a static fake.
type TaskDetailProvider interface {
	// TaskDetail returns the full Task record for the given id.
	// Returns store.ErrNotFound when the row does not exist.
	TaskDetail(ctx context.Context, id int64) (store.Task, error)
}

// TaskDetailStore is the narrow store-side surface TaskDetailProvider
// needs. sqlDashboardStore implements this alongside DashboardStore so
// there is a single *sql.DB connection for all web reads.
type TaskDetailStore interface {
	TaskDetail(ctx context.Context, id int64) (store.Task, error)
}

// TaskDetailData bundles the task record and its associated audit entries
// for a single handler invocation. Keeping them together avoids a second
// round-trip to assemble the view in the handler.
type TaskDetailData struct {
	Task         store.Task
	AuditEntries []AuditEntry
}

// taskDetailProvider is the production assembler: it fetches the task
// row via TaskDetailStore and the related audit entries via AuditReader.
type taskDetailProvider struct {
	store  TaskDetailStore
	audits AuditReader
}

// NewTaskDetailProvider wires a provider against a store + audit reader.
// A nil audits is patched to the noop reader so callers need not handle
// the nil case.
func NewTaskDetailProvider(s TaskDetailStore, audits AuditReader) TaskDetailProvider {
	if audits == nil {
		audits = noopAuditReader{}
	}
	return &taskDetailProvider{store: s, audits: audits}
}

func (p *taskDetailProvider) TaskDetail(ctx context.Context, id int64) (store.Task, error) {
	task, err := p.store.TaskDetail(ctx, id)
	if err != nil {
		return store.Task{}, err
	}
	return task, nil
}

// noopTaskDetailProvider is the fallback used when TaskDetail is not wired
// in Options. It always returns ErrNotFound so the handler returns 404
// rather than a 500 for any id.
type noopTaskDetailProvider struct{}

func (noopTaskDetailProvider) TaskDetail(_ context.Context, _ int64) (store.Task, error) {
	return store.Task{}, fmt.Errorf("task detail: %w", store.ErrNotFound)
}

// isErrNotFound reports whether err wraps store.ErrNotFound.
func isErrNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}
