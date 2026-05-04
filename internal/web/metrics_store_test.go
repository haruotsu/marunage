package web_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/web"
)

type metricsFixture struct {
	ctx       context.Context
	taskRepo  *store.TaskRepo
	provider  web.MetricsProvider
	closeFunc func()
}

func newMetricsFixture(t *testing.T) *metricsFixture {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	clock := time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC)
	tasks := store.NewTaskRepo(db, store.WithClock(func() time.Time { return clock }))
	return &metricsFixture{
		ctx:       context.Background(),
		taskRepo:  tasks,
		provider:  web.NewSQLMetricsProvider(db),
		closeFunc: func() { _ = db.Close() },
	}
}

// TestSQLMetricsProvider_EmptyDB returns a zero snapshot on an empty database.
func TestSQLMetricsProvider_EmptyDB(t *testing.T) {
	f := newMetricsFixture(t)
	t.Cleanup(f.closeFunc)

	snap, err := f.provider.Snapshot(f.ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.TotalTasks != 0 {
		t.Errorf("TotalTasks=%d; want 0", snap.TotalTasks)
	}
	if snap.SuccessRate != 0 {
		t.Errorf("SuccessRate=%f; want 0", snap.SuccessRate)
	}
	if snap.DailyCounts == nil {
		t.Error("DailyCounts is nil; want empty slice")
	}
}

// TestSQLMetricsProvider_CountsByStatus counts tasks by status.
func TestSQLMetricsProvider_CountsByStatus(t *testing.T) {
	f := newMetricsFixture(t)
	t.Cleanup(f.closeFunc)

	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	id1, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "gmail", Title: "t1"})
	if err != nil {
		t.Fatalf("insert t1: %v", err)
	}
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, id1, "ok", now); err != nil {
		t.Fatalf("done t1: %v", err)
	}

	id2, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "slack", Title: "t2"})
	if err != nil {
		t.Fatalf("insert t2: %v", err)
	}
	if err := f.taskRepo.MarkFailedWithReason(f.ctx, id2, "boom"); err != nil {
		t.Fatalf("fail t2: %v", err)
	}

	if _, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "github", Title: "t3"}); err != nil {
		t.Fatalf("insert t3: %v", err)
	}

	snap, err := f.provider.Snapshot(f.ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if snap.TotalTasks != 3 {
		t.Errorf("TotalTasks=%d; want 3", snap.TotalTasks)
	}
	if snap.ByStatus[store.StatusDone] != 1 {
		t.Errorf("ByStatus[done]=%d; want 1", snap.ByStatus[store.StatusDone])
	}
	if snap.ByStatus[store.StatusFailed] != 1 {
		t.Errorf("ByStatus[failed]=%d; want 1", snap.ByStatus[store.StatusFailed])
	}
	if snap.ByStatus[store.StatusPending] != 1 {
		t.Errorf("ByStatus[pending]=%d; want 1", snap.ByStatus[store.StatusPending])
	}
}

// TestSQLMetricsProvider_SuccessRate computes done/(done+failed).
func TestSQLMetricsProvider_SuccessRate(t *testing.T) {
	f := newMetricsFixture(t)
	t.Cleanup(f.closeFunc)

	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		id, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "gmail", Title: "done"})
		if err != nil {
			t.Fatalf("insert done %d: %v", i, err)
		}
		if err := f.taskRepo.MarkDoneWithSummary(f.ctx, id, "ok", now); err != nil {
			t.Fatalf("done %d: %v", i, err)
		}
	}
	id, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "gmail", Title: "fail"})
	if err != nil {
		t.Fatalf("insert fail: %v", err)
	}
	if err := f.taskRepo.MarkFailedWithReason(f.ctx, id, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	snap, err := f.provider.Snapshot(f.ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	want := 0.75
	if snap.SuccessRate < want-0.01 || snap.SuccessRate > want+0.01 {
		t.Errorf("SuccessRate=%.4f; want ~%.4f", snap.SuccessRate, want)
	}
}

// TestSQLMetricsProvider_DailyCountsLastThirtyDays returns counts for last 30 days.
func TestSQLMetricsProvider_DailyCountsLastThirtyDays(t *testing.T) {
	f := newMetricsFixture(t)
	t.Cleanup(f.closeFunc)

	today := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	id, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "gmail", Title: "recent"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, id, "ok", today); err != nil {
		t.Fatalf("done: %v", err)
	}

	// Insert an old task (>30 days ago) — should not appear in daily counts.
	idOld, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "gmail", Title: "old"})
	if err != nil {
		t.Fatalf("insert old: %v", err)
	}
	oldTime := today.AddDate(0, 0, -31)
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, idOld, "ok", oldTime); err != nil {
		t.Fatalf("done old: %v", err)
	}

	snap, err := f.provider.Snapshot(f.ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if len(snap.DailyCounts) != 1 {
		t.Errorf("DailyCounts len=%d; want 1 (only recent task)", len(snap.DailyCounts))
	}
	if len(snap.DailyCounts) > 0 && snap.DailyCounts[0].Done != 1 {
		t.Errorf("DailyCounts[0].Done=%d; want 1", snap.DailyCounts[0].Done)
	}
}
