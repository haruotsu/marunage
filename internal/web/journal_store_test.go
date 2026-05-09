package web_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/web"
)

var journalFixedClock = time.Date(2026, 5, 9, 15, 30, 0, 0, time.UTC)

type journalFixture struct {
	ctx       context.Context
	taskRepo  *store.TaskRepo
	provider  web.JournalProvider
	closeFunc func()
}

func newJournalFixture(t *testing.T) *journalFixture {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	tasks := store.NewTaskRepo(db, store.WithClock(func() time.Time { return journalFixedClock }))
	return &journalFixture{
		ctx:      context.Background(),
		taskRepo: tasks,
		provider: web.NewSQLJournalProvider(db),
		closeFunc: func() { _ = db.Close() },
	}
}

func TestSQLJournalProvider_EmptyDB(t *testing.T) {
	f := newJournalFixture(t)
	t.Cleanup(f.closeFunc)

	snap, err := f.provider.JournalSnapshot(f.ctx, "2026-05-09")
	if err != nil {
		t.Fatalf("JournalSnapshot: %v", err)
	}
	if snap.Entries == nil {
		t.Error("Entries is nil; want empty slice")
	}
	if len(snap.Entries) != 0 {
		t.Errorf("Entries len=%d; want 0", len(snap.Entries))
	}
	if snap.Date != "2026-05-09" {
		t.Errorf("Date=%q; want 2026-05-09", snap.Date)
	}
}

func TestSQLJournalProvider_EntriesOnDate(t *testing.T) {
	f := newJournalFixture(t)
	t.Cleanup(f.closeFunc)

	id1, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "gmail", Title: "task one"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	completedAt := time.Date(2026, 5, 9, 14, 5, 0, 0, time.UTC)
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, id1, "done result", completedAt); err != nil {
		t.Fatalf("MarkDoneWithSummary: %v", err)
	}

	id2, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "slack", Title: "task two"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := f.taskRepo.MarkFailedWithReason(f.ctx, id2, "something broke"); err != nil {
		t.Fatalf("MarkFailedWithReason: %v", err)
	}
	failedAt := time.Date(2026, 5, 9, 16, 0, 0, 0, time.UTC)
	if err := f.taskRepo.SetCompletedAt(f.ctx, id2, failedAt); err != nil {
		t.Fatalf("SetCompletedAt: %v", err)
	}

	snap, err := f.provider.JournalSnapshot(f.ctx, "2026-05-09")
	if err != nil {
		t.Fatalf("JournalSnapshot: %v", err)
	}

	if len(snap.Entries) != 2 {
		t.Fatalf("Entries len=%d; want 2", len(snap.Entries))
	}
	if snap.Entries[0].Time != "14:05" {
		t.Errorf("Entries[0].Time=%q; want 14:05", snap.Entries[0].Time)
	}
	if snap.Entries[0].Source != "gmail" {
		t.Errorf("Entries[0].Source=%q; want gmail", snap.Entries[0].Source)
	}
	if snap.Entries[0].Summary != "done result" {
		t.Errorf("Entries[0].Summary=%q; want done result", snap.Entries[0].Summary)
	}
	if snap.Entries[1].Time != "16:00" {
		t.Errorf("Entries[1].Time=%q; want 16:00", snap.Entries[1].Time)
	}
	if snap.Entries[1].Source != "slack" {
		t.Errorf("Entries[1].Source=%q; want slack", snap.Entries[1].Source)
	}
	if snap.Entries[1].Summary != "task two" {
		t.Errorf("Entries[1].Summary=%q; want task two (fallback to title)", snap.Entries[1].Summary)
	}
}

func TestSQLJournalProvider_ExcludesOtherDates(t *testing.T) {
	f := newJournalFixture(t)
	t.Cleanup(f.closeFunc)

	id, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "github", Title: "yesterday task"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	yesterday := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, id, "old", yesterday); err != nil {
		t.Fatalf("MarkDoneWithSummary: %v", err)
	}

	snap, err := f.provider.JournalSnapshot(f.ctx, "2026-05-09")
	if err != nil {
		t.Fatalf("JournalSnapshot: %v", err)
	}
	if len(snap.Entries) != 0 {
		t.Errorf("Entries len=%d; want 0 (task on different date excluded)", len(snap.Entries))
	}
}

func TestSQLJournalProvider_ExcludesPendingTasks(t *testing.T) {
	f := newJournalFixture(t)
	t.Cleanup(f.closeFunc)

	if _, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "github", Title: "still pending"}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	snap, err := f.provider.JournalSnapshot(f.ctx, "2026-05-09")
	if err != nil {
		t.Fatalf("JournalSnapshot: %v", err)
	}
	if len(snap.Entries) != 0 {
		t.Errorf("Entries len=%d; want 0 (pending task excluded)", len(snap.Entries))
	}
}

func TestSQLJournalProvider_EmptyDateUsesInjectedClock(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pastDate := time.Date(2020, 1, 15, 12, 0, 0, 0, time.UTC)
	tasks := store.NewTaskRepo(db, store.WithClock(func() time.Time { return pastDate }))

	id, err := tasks.Insert(context.Background(), store.Task{Source: "test", Title: "past task"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tasks.MarkDoneWithSummary(context.Background(), id, "done", pastDate); err != nil {
		t.Fatalf("MarkDoneWithSummary: %v", err)
	}

	provider := web.NewSQLJournalProvider(db, web.JournalOptions{Now: func() time.Time { return pastDate }})

	snap, err := provider.JournalSnapshot(context.Background(), "")
	if err != nil {
		t.Fatalf("JournalSnapshot: %v", err)
	}
	if len(snap.Entries) != 1 {
		t.Fatalf("Entries len=%d; want 1 (clock-injected date should match task date)", len(snap.Entries))
	}
	if snap.Date != "2020-01-15" {
		t.Errorf("Date=%q; want 2020-01-15", snap.Date)
	}
}
