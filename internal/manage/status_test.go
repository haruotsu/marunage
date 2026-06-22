package manage

import (
	"context"
	"testing"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/store"
)

// Plan resolves each decision's target DB status from the verdict registry so
// the cmd pipeline (PR-R05) persists status without re-deriving the mapping.
// The verdict vocabulary is open but the status enum is stable (原則1), so the
// status lands on the registry's value, not a hardcoded switch.
func TestPlanResolvesStatusFromVerdict(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "ready row", Body: "x"},                                    // ready -> pending
		{Title: "drop row", Body: "x", Verdict: collect.VerdictDrop},       // drop -> skipped
		{Title: "escalate", Body: "x", Verdict: collect.VerdictNeedsHuman}, // needs-human -> waiting_human
		{Title: "weird", Body: "x", Verdict: collect.Verdict("delegate")},  // unknown -> needs-human fallback
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := map[string]string{
		"ready row": store.StatusPending,
		"drop row":  store.StatusSkipped,
		"escalate":  store.StatusWaitingHuman,
		"weird":     store.StatusWaitingHuman, // safe fallback (原則2)
	}
	for title, wantStatus := range want {
		if got := decisionFor(t, plan, title).Status; got != wantStatus {
			t.Errorf("%q Status = %q; want %q", title, got, wantStatus)
		}
	}
}
