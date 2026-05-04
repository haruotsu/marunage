package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/web"
)

// fakeTaskDetailStore is a minimal web.TaskDetailStore for adapter tests.
type fakeTaskDetailStore struct {
	task store.Task
	err  error
}

func (f *fakeTaskDetailStore) TaskDetail(_ context.Context, _ int64) (store.Task, error) {
	return f.task, f.err
}

var _ web.TaskDetailStore = (*fakeTaskDetailStore)(nil)

// TestSQLLiveStreamProvider_WorkspaceIDForTask pins the three-path contract:
// task with ws set → return ws, task with empty ws → ErrNotFound, store error → propagate.
func TestSQLLiveStreamProvider_WorkspaceIDForTask(t *testing.T) {
	t.Run("task with workspace returns workspace ID", func(t *testing.T) {
		p := &sqlLiveStreamProvider{
			store: &fakeTaskDetailStore{task: store.Task{WS: "workspace:42"}},
		}
		id, err := p.WorkspaceIDForTask(context.Background(), 1)
		if err != nil {
			t.Fatalf("WorkspaceIDForTask: %v", err)
		}
		if id != "workspace:42" {
			t.Errorf("workspaceID = %q; want %q", id, "workspace:42")
		}
	})

	t.Run("task with empty WS returns ErrNotFound", func(t *testing.T) {
		p := &sqlLiveStreamProvider{
			store: &fakeTaskDetailStore{task: store.Task{WS: ""}},
		}
		_, err := p.WorkspaceIDForTask(context.Background(), 1)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v; want store.ErrNotFound", err)
		}
	})

	t.Run("store error propagates", func(t *testing.T) {
		p := &sqlLiveStreamProvider{
			store: &fakeTaskDetailStore{err: store.ErrNotFound},
		}
		_, err := p.WorkspaceIDForTask(context.Background(), 999)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v; want store.ErrNotFound", err)
		}
	})
}
