package project

import (
	"testing"
	"time"
)

func TestIsHumanTask(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"[human] Interview candidates", true},
		{"Fix the bug [human]", true},
		{"[HUMAN] Review PR", true},
		{"task [Human] mixed case", true},
		{"Regular task", false},
		{"task with human in it but no brackets", false},
		{"human", false},
		{"", false},
	}
	for _, tc := range tests {
		item := BoardItem{Title: tc.title}
		got := IsHumanTask(item)
		if got != tc.want {
			t.Errorf("IsHumanTask(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestExtractPhase(t *testing.T) {
	tests := []struct {
		title string
		want  int
	}{
		{"Phase 1: Setup infrastructure", 1},
		{"Phase 2 - Deploy service", 2},
		{"phase 3 final", 3},
		{"PHASE 10 advanced", 10},
		{"No phase here", 0},
		{"", 0},
		{"Phase : no number", 0},
	}
	for _, tc := range tests {
		got := extractPhase(tc.title)
		if got != tc.want {
			t.Errorf("extractPhase(%q) = %d, want %d", tc.title, got, tc.want)
		}
	}
}

func TestSortByPhaseDate(t *testing.T) {
	t1 := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 16, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2024, 1, 17, 0, 0, 0, 0, time.UTC)

	t.Run("phase then date order", func(t *testing.T) {
		items := []BoardItem{
			{ID: "c", Title: "Phase 2: Second phase task", UpdatedAt: t3},
			{ID: "a", Title: "Phase 1: First task", UpdatedAt: t1},
			{ID: "b", Title: "Phase 1: Second task", UpdatedAt: t2},
		}
		sorted := SortByPhaseDate(items)
		wantOrder := []string{"a", "b", "c"}
		for i, want := range wantOrder {
			if sorted[i].ID != want {
				t.Errorf("sorted[%d].ID = %q, want %q", i, sorted[i].ID, want)
			}
		}
	})

	t.Run("no phase sorts by date only", func(t *testing.T) {
		items := []BoardItem{
			{ID: "b", Title: "Task B", UpdatedAt: t2},
			{ID: "a", Title: "Task A", UpdatedAt: t1},
		}
		sorted := SortByPhaseDate(items)
		if sorted[0].ID != "a" || sorted[1].ID != "b" {
			t.Errorf("sort by date: got [%s, %s], want [a, b]", sorted[0].ID, sorted[1].ID)
		}
	})

	t.Run("does not mutate original slice", func(t *testing.T) {
		items := []BoardItem{
			{ID: "b", Title: "Task B", UpdatedAt: t2},
			{ID: "a", Title: "Task A", UpdatedAt: t1},
		}
		_ = SortByPhaseDate(items)
		if items[0].ID != "b" {
			t.Errorf("original slice mutated: items[0].ID = %q, want b", items[0].ID)
		}
	})
}

func TestNextTask(t *testing.T) {
	t1 := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)

	t.Run("all done returns ActionAllDone", func(t *testing.T) {
		items := []BoardItem{
			{ID: "a", Title: "Task A", Status: StatusDone, UpdatedAt: t1},
			{ID: "b", Title: "Task B", Status: StatusDone, UpdatedAt: t1},
		}
		item, action := NextTask(items)
		if item != nil {
			t.Errorf("item = %+v, want nil", item)
		}
		if action != ActionAllDone {
			t.Errorf("action = %v, want ActionAllDone", action)
		}
	})

	t.Run("empty slice returns ActionAllDone", func(t *testing.T) {
		item, action := NextTask(nil)
		if item != nil {
			t.Errorf("item = %+v, want nil", item)
		}
		if action != ActionAllDone {
			t.Errorf("action = %v, want ActionAllDone", action)
		}
	})

	t.Run("first todo item dispatched", func(t *testing.T) {
		items := []BoardItem{
			{ID: "a", Title: "Task A", Status: "Todo", UpdatedAt: t1},
		}
		item, action := NextTask(items)
		if item == nil || item.ID != "a" {
			t.Fatalf("item = %v, want item a", item)
		}
		if action != ActionDispatch {
			t.Errorf("action = %v, want ActionDispatch", action)
		}
	})

	t.Run("human task blocks and returns ActionWaitHuman", func(t *testing.T) {
		items := []BoardItem{
			{ID: "a", Title: "[human] Interview", Status: "Todo", UpdatedAt: t1},
			{ID: "b", Title: "Task B", Status: "Todo", UpdatedAt: t1},
		}
		item, action := NextTask(items)
		if item == nil || item.ID != "a" {
			t.Fatalf("item = %v, want item a", item)
		}
		if action != ActionWaitHuman {
			t.Errorf("action = %v, want ActionWaitHuman", action)
		}
	})

	t.Run("done item skipped second item dispatched", func(t *testing.T) {
		items := []BoardItem{
			{ID: "a", Title: "Task A", Status: StatusDone, UpdatedAt: t1},
			{ID: "b", Title: "Task B", Status: "Todo", UpdatedAt: t1},
		}
		item, action := NextTask(items)
		if item == nil || item.ID != "b" {
			t.Fatalf("item = %v, want item b", item)
		}
		if action != ActionDispatch {
			t.Errorf("action = %v, want ActionDispatch", action)
		}
	})

	t.Run("in-progress item returns ActionWaitRunning", func(t *testing.T) {
		items := []BoardItem{
			{ID: "a", Title: "Task A", Status: StatusInProgress, UpdatedAt: t1},
		}
		item, action := NextTask(items)
		if item == nil || item.ID != "a" {
			t.Fatalf("item = %v, want item a", item)
		}
		if action != ActionWaitRunning {
			t.Errorf("action = %v, want ActionWaitRunning", action)
		}
	})

	t.Run("done human item does not block", func(t *testing.T) {
		items := []BoardItem{
			{ID: "a", Title: "[human] Done interview", Status: StatusDone, UpdatedAt: t1},
			{ID: "b", Title: "Task B", Status: "Todo", UpdatedAt: t1},
		}
		item, action := NextTask(items)
		if item == nil || item.ID != "b" {
			t.Fatalf("item = %v, want item b", item)
		}
		if action != ActionDispatch {
			t.Errorf("action = %v, want ActionDispatch", action)
		}
	})
}

func TestNextActionString(t *testing.T) {
	tests := []struct {
		action NextAction
		want   string
	}{
		{ActionDispatch, "dispatch"},
		{ActionWaitHuman, "wait_human"},
		{ActionWaitRunning, "wait_running"},
		{ActionAllDone, "all_done"},
		{NextAction(99), "unknown(99)"},
	}
	for _, tc := range tests {
		if got := tc.action.String(); got != tc.want {
			t.Errorf("NextAction(%d).String() = %q, want %q", tc.action, got, tc.want)
		}
	}
}
