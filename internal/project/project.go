package project

import (
	"fmt"
	"sort"
	"strings"
)

// NextAction is the verdict NextTask returns to the caller.
type NextAction int

const (
	// ActionDispatch means the returned item should be dispatched now.
	ActionDispatch NextAction = iota
	// ActionWaitHuman means the returned item is a [human] task that
	// blocks progress; the caller should wait and poll.
	ActionWaitHuman
	// ActionWaitRunning means the returned item is already in progress;
	// the caller should wait for completion before dispatching the next.
	ActionWaitRunning
	// ActionAllDone means every item on the board is Done; the project is
	// complete.
	ActionAllDone
)

// String aids debug output and test failure messages.
func (a NextAction) String() string {
	switch a {
	case ActionDispatch:
		return "dispatch"
	case ActionWaitHuman:
		return "wait_human"
	case ActionWaitRunning:
		return "wait_running"
	case ActionAllDone:
		return "all_done"
	}
	return fmt.Sprintf("unknown(%d)", int(a))
}

// IsHumanTask returns true when item.Title contains the "[human]" marker
// (case-insensitive). Human tasks are milestones that require a person to
// act (e.g. a stakeholder interview or a sign-off meeting); the project
// runner skips dispatching them and waits until they are marked Done.
func IsHumanTask(item BoardItem) bool {
	return strings.Contains(strings.ToLower(item.Title), "[human]")
}

// extractPhase returns the phase number embedded in title (e.g.
// "Phase 2: Deploy service" → 2) or 0 when no "phase N" pattern is found.
// The phase is used as the primary SortByPhaseDate key so all phase-1 items
// are processed before phase-2 items regardless of their UpdatedAt.
func extractPhase(title string) int {
	lower := strings.ToLower(title)
	idx := strings.Index(lower, "phase ")
	if idx < 0 {
		return 0
	}
	rest := lower[idx+len("phase "):]
	var num int
	for _, c := range rest {
		if c >= '0' && c <= '9' {
			num = num*10 + int(c-'0')
		} else {
			break
		}
	}
	return num
}

// SortByPhaseDate returns a new slice sorted ascending by (phase, UpdatedAt).
// Items that carry no "phase N" pattern in their title sort as phase 0,
// ahead of all explicitly-phased items. The original slice is never mutated.
func SortByPhaseDate(items []BoardItem) []BoardItem {
	sorted := make([]BoardItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi := extractPhase(sorted[i].Title)
		pj := extractPhase(sorted[j].Title)
		if pi != pj {
			return pi < pj
		}
		return sorted[i].UpdatedAt.Before(sorted[j].UpdatedAt)
	})
	return sorted
}

// NextTask walks items in the order provided and returns the first item
// that needs action together with the recommended action. Callers should
// sort items with SortByPhaseDate before calling NextTask.
//
// Decision matrix (first non-Done item wins):
//
//	[human] and not Done  → ActionWaitHuman  (blocks; do not skip past it)
//	"In Progress"         → ActionWaitRunning (already dispatched)
//	any other non-Done    → ActionDispatch
//	all Done (or empty)   → ActionAllDone
func NextTask(items []BoardItem) (*BoardItem, NextAction) {
	for i := range items {
		item := &items[i]
		if item.Status == "Done" {
			continue
		}
		if IsHumanTask(*item) {
			return item, ActionWaitHuman
		}
		if item.Status == "In Progress" {
			return item, ActionWaitRunning
		}
		return item, ActionDispatch
	}
	return nil, ActionAllDone
}
