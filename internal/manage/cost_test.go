package manage

import (
	"context"
	"testing"

	"github.com/haruotsu/marunage/internal/collect"
)

func TestPlanScoresReadyInOneBatch(t *testing.T) {
	st := &fakeStore{}
	cands := make([]collect.Candidate, 5)
	for i := range cands {
		cands[i] = collect.Candidate{Title: string(rune('a' + i)), Body: "x"}
	}
	scorer := &fakeScorer{fn: func(items []ScoreItem) ([]ScoreResult, error) {
		return make([]ScoreResult, len(items)), nil
	}}
	if _, err := Plan(context.Background(), cands, st, WithLLMScorer(scorer)); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if scorer.calls != 1 {
		t.Fatalf("scorer called %d times, want 1 (batch evaluation, no N+1)", scorer.calls)
	}
	if len(scorer.batchSizes) != 1 || scorer.batchSizes[0] != 5 {
		t.Fatalf("batch sizes = %v, want one batch of 5", scorer.batchSizes)
	}
}

func TestPlanCapsLLMCallsPerRun(t *testing.T) {
	st := &fakeStore{}
	cands := make([]collect.Candidate, 10)
	for i := range cands {
		cands[i] = collect.Candidate{Title: string(rune('a' + i)), Body: "x"}
	}
	scorer := &fakeScorer{fn: func(items []ScoreItem) ([]ScoreResult, error) {
		return make([]ScoreResult, len(items)), nil
	}}
	_, err := Plan(context.Background(), cands, st,
		WithLLMScorer(scorer),
		WithLLMBatchSize(2),
		WithLLMMaxCalls(2),
	)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if scorer.calls != 2 {
		t.Fatalf("scorer called %d times, want 2 (1ループあたりの上限)", scorer.calls)
	}
	scored := 0
	for _, n := range scorer.batchSizes {
		scored += n
	}
	if scored != 4 {
		t.Fatalf("scored %d candidates, want 4 (2 calls x batch 2); rest must stub", scored)
	}
}

func TestPlanCacheSkipsReevaluation(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{{Source: "s", ExternalID: "1", Title: "t", Body: "x"}}
	scorer := &fakeScorer{fn: func(items []ScoreItem) ([]ScoreResult, error) {
		out := make([]ScoreResult, len(items))
		for i := range out {
			out[i] = ScoreResult{Score: 42}
		}
		return out, nil
	}}
	cache := NewMemoryScoreCache()
	run := func() ReadyPlan {
		plan, err := Plan(context.Background(), cands, st, WithLLMScorer(scorer), WithLLMCache(cache))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		return plan
	}
	first := run()
	second := run()
	if scorer.calls != 1 {
		t.Fatalf("scorer called %d times across two runs, want 1 (cache hit on second)", scorer.calls)
	}
	if first.Ready[0].Score != 42 || second.Ready[0].Score != 42 {
		t.Fatalf("cached score not reused: first=%v second=%v", first.Ready[0].Score, second.Ready[0].Score)
	}
}

func TestPlanCacheReevaluatesWhenContentChanges(t *testing.T) {
	st := &fakeStore{}
	scorer := &fakeScorer{fn: func(items []ScoreItem) ([]ScoreResult, error) {
		return make([]ScoreResult, len(items)), nil
	}}
	cache := NewMemoryScoreCache()
	base := collect.Candidate{Source: "s", ExternalID: "1", Title: "t", Body: "first"}
	if _, err := Plan(context.Background(), []collect.Candidate{base}, st, WithLLMScorer(scorer), WithLLMCache(cache)); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	edited := base
	edited.Body = "second"
	if _, err := Plan(context.Background(), []collect.Candidate{edited}, st, WithLLMScorer(scorer), WithLLMCache(cache)); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if scorer.calls != 2 {
		t.Fatalf("scorer called %d times, want 2 (edited body must re-score)", scorer.calls)
	}
}
