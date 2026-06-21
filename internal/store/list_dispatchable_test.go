package store_test

import (
	"context"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// TestListDispatchableOnlyIncludesReadyAndLegacy is the strangler-fig pin
// (redesign §8 PR-R04): with DispatchableOnly set, the dispatcher must see
// rows the manager marked ready AND legacy rows nobody has evaluated yet
// (plan_label IS NULL), while hold/defer/needs-human/drop rows are excluded.
// Until the management layer starts populating plan_label (PR-R05+), every
// real row is legacy, so dispatch behaviour is unchanged.
func TestListDispatchableOnlyIncludesReadyAndLegacy(t *testing.T) {
	db := openTempDB(t)
	repo := store.NewTaskRepo(db)
	ctx := context.Background()

	legacyID, err := repo.Insert(ctx, store.Task{Source: "manual", Title: "legacy"})
	if err != nil {
		t.Fatalf("insert legacy: %v", err)
	}

	mk := func(title, label string) int64 {
		id, err := repo.Insert(ctx, store.Task{Source: "manual", Title: title})
		if err != nil {
			t.Fatalf("insert %s: %v", title, err)
		}
		if _, err := db.Exec("UPDATE tasks SET plan_label=? WHERE id=?", label, id); err != nil {
			t.Fatalf("set plan_label=%s: %v", label, err)
		}
		return id
	}
	readyID := mk("ready row", "ready")
	holdID := mk("hold row", "hold")
	deferID := mk("defer row", "defer")
	dropID := mk("drop row", "drop")

	rows, err := repo.List(ctx, store.ListFilter{
		Statuses:         []string{store.StatusPending},
		DispatchableOnly: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	got := map[int64]bool{}
	for _, r := range rows {
		got[r.ID] = true
	}
	if !got[legacyID] {
		t.Errorf("legacy row (plan_label IS NULL) must still dispatch")
	}
	if !got[readyID] {
		t.Errorf("ready row must dispatch")
	}
	for name, id := range map[string]int64{"hold": holdID, "defer": deferID, "drop": dropID} {
		if got[id] {
			t.Errorf("%s row must NOT be dispatchable", name)
		}
	}
}

// TestListDispatchableOnlyOffReturnsEverything pins that the filter is opt-in:
// the zero value of DispatchableOnly leaves List unchanged, so every existing
// caller (review, render, status) keeps its current behaviour.
func TestListDispatchableOnlyOffReturnsEverything(t *testing.T) {
	db := openTempDB(t)
	repo := store.NewTaskRepo(db)
	ctx := context.Background()

	if _, err := repo.Insert(ctx, store.Task{Source: "manual", Title: "legacy"}); err != nil {
		t.Fatalf("insert legacy: %v", err)
	}
	id, err := repo.Insert(ctx, store.Task{Source: "manual", Title: "held"})
	if err != nil {
		t.Fatalf("insert held: %v", err)
	}
	if _, err := db.Exec("UPDATE tasks SET plan_label='hold' WHERE id=?", id); err != nil {
		t.Fatalf("set plan_label: %v", err)
	}

	rows, err := repo.List(ctx, store.ListFilter{Statuses: []string{store.StatusPending}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("List without DispatchableOnly returned %d rows; want 2 (filter must be opt-in)", len(rows))
	}
}
