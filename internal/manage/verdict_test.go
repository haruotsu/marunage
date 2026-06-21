package manage

import (
	"testing"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/store"
)

func TestPolicyForKnownVerdicts(t *testing.T) {
	reg := DefaultRegistry()
	cases := []struct {
		verdict      collect.Verdict
		status       string
		dispatchable bool
		notify       bool
	}{
		{collect.VerdictReady, store.StatusPending, true, false},
		{collect.VerdictHold, store.StatusPending, false, false},
		{collect.VerdictDefer, store.StatusPending, false, false},
		{collect.VerdictNeedsHuman, store.StatusWaitingHuman, false, true},
		{collect.VerdictDrop, store.StatusSkipped, false, false},
	}
	for _, tc := range cases {
		got := reg.policyFor(tc.verdict)
		if got.Status != tc.status || got.Dispatchable != tc.dispatchable || got.Notify != tc.notify {
			t.Errorf("policyFor(%q) = %+v; want status=%q dispatchable=%v notify=%v",
				tc.verdict, got, tc.status, tc.dispatchable, tc.notify)
		}
	}
}

func TestPolicyForUnknownVerdictFallsBackToNeedsHuman(t *testing.T) {
	reg := DefaultRegistry()
	got := reg.policyFor(collect.Verdict("delegate"))
	want := reg.policyFor(collect.VerdictNeedsHuman)
	if got != want {
		t.Errorf("policyFor(unknown) = %+v; want needs-human policy %+v", got, want)
	}
}

func TestPolicyForEmptyVerdictFallsBackToNeedsHuman(t *testing.T) {
	reg := DefaultRegistry()
	got := reg.policyFor(collect.Verdict(""))
	want := reg.policyFor(collect.VerdictNeedsHuman)
	if got != want {
		t.Errorf("policyFor(\"\") = %+v; want needs-human policy %+v", got, want)
	}
}
