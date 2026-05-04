package web_test

import (
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/web"
)

// TestTaskDetailStore_Get: sqlDashboardStore.TaskDetail() with real SQLite in-memory DB.
func TestTaskDetailStore_Get(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	id, err := f.taskRepo.Insert(f.ctx, store.Task{
		Source:         "markdown",
		ExternalID:     "ext-001",
		ExternalURL:    "https://example.com/task/1",
		Title:          "Detail Test Task",
		Body:           "body content",
		Notes:          `{"hint": "some notes"}`,
		Status:         store.StatusDone,
		JudgmentReason: "it was right",
		Priority:       3,
		WS:             "workspace:101",
		ResultSummary:  "done well",
		Reflection:     "learned a lot",
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.taskRepo.SetStartedAt(f.ctx, id, now.Add(time.Minute)); err != nil {
		t.Fatalf("SetStartedAt: %v", err)
	}
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, id, "done well", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("MarkDoneWithSummary: %v", err)
	}

	detailStore, ok := f.sqlStore.(web.TaskDetailStore)
	if !ok {
		t.Fatalf("sqlDashboardStore does not implement TaskDetailStore")
	}

	task, err := detailStore.TaskDetail(f.ctx, id)
	if err != nil {
		t.Fatalf("TaskDetail: %v", err)
	}
	if task.ID != id {
		t.Errorf("ID = %d; want %d", task.ID, id)
	}
	if task.Title != "Detail Test Task" {
		t.Errorf("Title = %q; want Detail Test Task", task.Title)
	}
	if task.Source != "markdown" {
		t.Errorf("Source = %q; want markdown", task.Source)
	}
	if task.WS != "workspace:101" {
		t.Errorf("WS = %q; want workspace:101", task.WS)
	}
	if task.ResultSummary != "done well" {
		t.Errorf("ResultSummary = %q; want done well", task.ResultSummary)
	}
	if task.Reflection != "learned a lot" {
		t.Errorf("Reflection = %q; want learned a lot", task.Reflection)
	}
}

// TestTaskDetailStore_GetNotFound: missing id returns store.ErrNotFound.
func TestTaskDetailStore_GetNotFound(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	detailStore, ok := f.sqlStore.(web.TaskDetailStore)
	if !ok {
		t.Fatalf("sqlDashboardStore does not implement TaskDetailStore")
	}

	_, err := detailStore.TaskDetail(f.ctx, 99999)
	if err == nil {
		t.Fatal("expected error for missing id; got nil")
	}
	if !isNotFound(err) {
		t.Errorf("error = %v; want store.ErrNotFound", err)
	}
}

func isNotFound(err error) bool {
	return err != nil && err.Error() == store.ErrNotFound.Error()
}
