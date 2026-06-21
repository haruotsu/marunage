package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

func TestSetPlanPersistsColumns(t *testing.T) {
	db := openTempDB(t)
	repo := store.NewTaskRepo(db)
	ctx := context.Background()

	id, err := repo.Insert(ctx, store.Task{Source: "markdown", Title: "do it"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	plannedAt := time.Date(2026, 6, 21, 9, 30, 0, 0, time.UTC)
	if err := repo.SetPlan(ctx, id, "ready", "rule engine: cleared for scoring", 12.5, 3, plannedAt); err != nil {
		t.Fatalf("SetPlan: %v", err)
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PlanLabel != "ready" {
		t.Errorf("PlanLabel = %q; want ready", got.PlanLabel)
	}
	if got.PlanReason != "rule engine: cleared for scoring" {
		t.Errorf("PlanReason = %q", got.PlanReason)
	}
	if got.PlanScore != 12.5 {
		t.Errorf("PlanScore = %v; want 12.5", got.PlanScore)
	}
	if got.PlanRank != 3 {
		t.Errorf("PlanRank = %d; want 3", got.PlanRank)
	}
	if !got.PlannedAt.Equal(plannedAt) {
		t.Errorf("PlannedAt = %v; want %v", got.PlannedAt, plannedAt)
	}
}

// A non-ready decision (rank 0) stores plan_rank as NULL so the dispatch
// ordering sorts it after genuinely ranked ready rows; it reads back as 0.
func TestSetPlanZeroRankReadsBackZero(t *testing.T) {
	db := openTempDB(t)
	repo := store.NewTaskRepo(db)
	ctx := context.Background()

	id, err := repo.Insert(ctx, store.Task{Source: "markdown", Title: "noise", Status: store.StatusSkipped})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.SetPlan(ctx, id, "drop", "rule (cwd-violation)", 0, 0, time.Date(2026, 6, 21, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("SetPlan: %v", err)
	}
	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PlanLabel != "drop" || got.PlanRank != 0 {
		t.Errorf("PlanLabel=%q PlanRank=%d; want drop/0", got.PlanLabel, got.PlanRank)
	}
}

func TestSetPlanMissingIDReturnsNotFound(t *testing.T) {
	db := openTempDB(t)
	repo := store.NewTaskRepo(db)
	ctx := context.Background()
	err := repo.SetPlan(ctx, 999, "ready", "x", 0, 1, time.Now())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetPlan on missing id = %v; want ErrNotFound", err)
	}
}

func TestGetBySourceExternalID(t *testing.T) {
	db := openTempDB(t)
	repo := store.NewTaskRepo(db)
	ctx := context.Background()

	id, err := repo.Insert(ctx, store.Task{Source: "markdown", ExternalID: "abc123", Title: "t"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := repo.GetBySourceExternalID(ctx, "markdown", "abc123")
	if err != nil {
		t.Fatalf("GetBySourceExternalID: %v", err)
	}
	if got.ID != id {
		t.Errorf("ID = %d; want %d", got.ID, id)
	}

	if _, err := repo.GetBySourceExternalID(ctx, "markdown", "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing lookup = %v; want ErrNotFound", err)
	}
}

// DispatchableOnly orders ready rows by plan_rank ascending so the dispatcher
// pulls them in the management layer's intended execution order. Legacy rows
// with no plan_rank (NULL) sort after the ranked ones, preserving the old
// priority/created_at order among themselves.
func TestListDispatchableOrdersByPlanRank(t *testing.T) {
	db := openTempDB(t)
	repo := store.NewTaskRepo(db)
	ctx := context.Background()

	plannedAt := time.Date(2026, 6, 21, 9, 30, 0, 0, time.UTC)
	mk := func(title string, rank int) {
		id, err := repo.Insert(ctx, store.Task{Source: "markdown", ExternalID: title, Title: title})
		if err != nil {
			t.Fatalf("insert %s: %v", title, err)
		}
		if err := repo.SetPlan(ctx, id, "ready", "x", float64(10-rank), rank, plannedAt); err != nil {
			t.Fatalf("SetPlan %s: %v", title, err)
		}
	}
	// Insert out of rank order to prove the ORDER BY, not insertion order, wins.
	mk("third", 3)
	mk("first", 1)
	mk("second", 2)

	rows, err := repo.List(ctx, store.ListFilter{
		Statuses:         []string{store.StatusPending},
		DispatchableOnly: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"first", "second", "third"}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d; want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i].Title != w {
			t.Errorf("rows[%d] = %q; want %q (plan_rank order)", i, rows[i].Title, w)
		}
	}
}

// A ranked ready row sorts ahead of a legacy (NULL plan_rank) row.
func TestListDispatchableRankedBeforeLegacy(t *testing.T) {
	db := openTempDB(t)
	repo := store.NewTaskRepo(db)
	ctx := context.Background()

	legacyID, err := repo.Insert(ctx, store.Task{Source: "markdown", ExternalID: "legacy", Title: "legacy"})
	if err != nil {
		t.Fatalf("insert legacy: %v", err)
	}
	rankedID, err := repo.Insert(ctx, store.Task{Source: "markdown", ExternalID: "ranked", Title: "ranked"})
	if err != nil {
		t.Fatalf("insert ranked: %v", err)
	}
	if err := repo.SetPlan(ctx, rankedID, "ready", "x", 1, 1, time.Now()); err != nil {
		t.Fatalf("SetPlan: %v", err)
	}

	rows, err := repo.List(ctx, store.ListFilter{
		Statuses:         []string{store.StatusPending},
		DispatchableOnly: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d; want 2", len(rows))
	}
	if rows[0].ID != rankedID || rows[1].ID != legacyID {
		t.Errorf("order = [%d,%d]; want ranked(%d) before legacy(%d)", rows[0].ID, rows[1].ID, rankedID, legacyID)
	}
}
