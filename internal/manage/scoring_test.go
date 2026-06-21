package manage

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/collect"
)

// fakeScorer is the deterministic test double for the LLM scoring pass: tests
// drive its output and assert on how Plan batched the calls, so the real
// Claude invocation is never exercised in unit tests.
type fakeScorer struct {
	fn         func(items []ScoreItem) ([]ScoreResult, error)
	calls      int
	batchSizes []int
}

func (f *fakeScorer) Score(_ context.Context, items []ScoreItem) ([]ScoreResult, error) {
	f.calls++
	f.batchSizes = append(f.batchSizes, len(items))
	return f.fn(items)
}

// byTitle returns a scorer that assigns each candidate the score its title
// maps to; titles absent from the map score 0.
func byTitle(scores map[string]float64) *fakeScorer {
	return &fakeScorer{fn: func(items []ScoreItem) ([]ScoreResult, error) {
		out := make([]ScoreResult, len(items))
		for i, it := range items {
			out[i] = ScoreResult{Score: scores[it.Candidate.Title]}
		}
		return out, nil
	}}
}

func TestPlanWithoutScorerKeepsStubOrdering(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "low", Body: "x", Priority: "1"},
		{Title: "high", Body: "x", Priority: "10"},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Ready) != 2 {
		t.Fatalf("ready=%d, want 2", len(plan.Ready))
	}
	if plan.Ready[0].Candidate.Title != "high" {
		t.Fatalf("stub ordering: ready[0]=%q, want high", plan.Ready[0].Candidate.Title)
	}
}

func TestPlanLLMScorerReordersReady(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "low", Body: "x", Priority: "1"},
		{Title: "high", Body: "x", Priority: "10"},
	}
	// Stub would rank "high" first; the LLM inverts that.
	scorer := byTitle(map[string]float64{"low": 100, "high": 1})
	plan, err := Plan(context.Background(), cands, st, WithLLMScorer(scorer))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Ready[0].Candidate.Title != "low" {
		t.Fatalf("llm ordering: ready[0]=%q, want low", plan.Ready[0].Candidate.Title)
	}
	if got := decisionFor(t, plan, "low").Score; got != 100 {
		t.Fatalf("low score=%v, want 100", got)
	}
}

func TestPlanLLMScorerDemotesToDefer(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "now", Body: "x", Priority: "1"},
		{Title: "later", Body: "x", Priority: "1"},
	}
	scorer := &fakeScorer{fn: func(items []ScoreItem) ([]ScoreResult, error) {
		out := make([]ScoreResult, len(items))
		for i, it := range items {
			if it.Candidate.Title == "later" {
				out[i] = ScoreResult{Defer: true, Reason: "deadline is far off"}
			} else {
				out[i] = ScoreResult{Score: 5}
			}
		}
		return out, nil
	}}
	plan, err := Plan(context.Background(), cands, st, WithLLMScorer(scorer))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Ready) != 1 || plan.Ready[0].Candidate.Title != "now" {
		t.Fatalf("ready=%v, want only [now]", titles(plan.Ready))
	}
	d := decisionFor(t, plan, "later")
	if d.Verdict != collect.VerdictDefer {
		t.Fatalf("later verdict=%q, want defer", d.Verdict)
	}
	if d.Status != "pending" {
		t.Fatalf("later status=%q, want pending", d.Status)
	}
	if d.Rank != 0 {
		t.Fatalf("deferred candidate keeps rank %d, want 0", d.Rank)
	}
}

func TestPlanLLMScorerErrorFallsBackToStub(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "low", Body: "x", Priority: "1"},
		{Title: "high", Body: "x", Priority: "10"},
	}
	scorer := &fakeScorer{fn: func([]ScoreItem) ([]ScoreResult, error) {
		return nil, errors.New("claude exploded")
	}}
	plan, err := Plan(context.Background(), cands, st, WithLLMScorer(scorer))
	if err != nil {
		t.Fatalf("Plan must not surface scorer errors: %v", err)
	}
	if len(plan.Ready) != 2 || plan.Ready[0].Candidate.Title != "high" {
		t.Fatalf("fallback ordering: ready=%v, want stub order [high, low]", titles(plan.Ready))
	}
}

func TestPlanLLMScorerLengthMismatchFallsBack(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "low", Body: "x", Priority: "1"},
		{Title: "high", Body: "x", Priority: "10"},
	}
	scorer := &fakeScorer{fn: func([]ScoreItem) ([]ScoreResult, error) {
		return []ScoreResult{{Score: 1}}, nil // one result for two items
	}}
	plan, err := Plan(context.Background(), cands, st, WithLLMScorer(scorer))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Ready[0].Candidate.Title != "high" {
		t.Fatalf("length-mismatch must fall back to stub: ready=%v", titles(plan.Ready))
	}
}

func TestPlanLLMScorerOnlyScoresReadyCandidates(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "ready1", Body: "x"},
		{Title: "escalated", Body: ""}, // empty body -> needs-human, never scored
		{Title: "ready2", Body: "x"},
	}
	var seen []string
	scorer := &fakeScorer{fn: func(items []ScoreItem) ([]ScoreResult, error) {
		for _, it := range items {
			seen = append(seen, it.Candidate.Title)
		}
		return make([]ScoreResult, len(items)), nil
	}}
	if _, err := Plan(context.Background(), cands, st, WithLLMScorer(scorer)); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(seen) != 2 || !inSlice(seen, "ready1") || !inSlice(seen, "ready2") {
		t.Fatalf("scorer saw %v, want only the two ready candidates", seen)
	}
}

func titles(pcs []PlannedCandidate) []string {
	out := make([]string, len(pcs))
	for i, p := range pcs {
		out[i] = p.Candidate.Title
	}
	return out
}
