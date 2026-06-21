package manage

import (
	"context"
	"math"
	"strings"

	"github.com/haruotsu/marunage/internal/collect"
)

// ScoreItem is one ready candidate handed to the LLM scoring pass. Only the
// candidates the rule engine cleared as ready reach the scorer (redesign §3.2:
// the LLM decides いつやるか, never 捨ててよいか), so the deterministic cut-offs
// — and their cost savings — always run first.
type ScoreItem struct {
	Candidate collect.Candidate
}

// ScoreResult is the LLM's judgment for one ScoreItem.
type ScoreResult struct {
	// Score orders the ready列 (higher = sooner). Ignored when Defer is set.
	Score float64
	// Defer demotes the candidate out of ready to defer (redesign §3.1
	// "やるべきだが今じゃない"). The LLM may rule a ready candidate "not now"
	// without dropping it; the rules already proved it is worth doing.
	Defer bool
	// Reason is the LLM rationale, recorded for the `marunage review` audit
	// trail. It crosses a trust boundary (LLM output): the wiring that
	// persists it MUST redact secrets, exactly as collect.Apply does.
	Reason string
}

// LLMScorer scores the ready candidates of one Plan run. Plan calls it in
// batches (one call per chunk) rather than once per candidate, so a run with N
// candidates costs O(1) LLM calls, not O(N) (redesign §9.1 cost control). It
// returns one ScoreResult per item in input order; an error or a length
// mismatch makes Plan fall back to the deterministic stub scorer so a flaky or
// hostile LLM never blocks dispatch (LLM 出力は信頼境界 / No silent loss).
type LLMScorer interface {
	Score(ctx context.Context, items []ScoreItem) ([]ScoreResult, error)
}

// WithLLMScorer injects the LLM scoring pass. Absent, Plan uses the
// deterministic stub scorer and behaves exactly as the rule-only skeleton
// (PR-R03) — which is what the off switch (llm_scoring=false) relies on: the
// cmd wiring simply does not pass this option.
func WithLLMScorer(s LLMScorer) Option {
	return func(p *planner) {
		if s != nil {
			p.scorer = s
		}
	}
}

// scoreReady fills in the Score of every ready candidate, and may demote some
// to defer when the LLM rules them "not now". With no scorer injected it is
// the deterministic stub (PR-R03 behaviour, pinned by test); with a scorer it
// runs the LLM pass and falls back to the stub for any candidate the LLM did
// not (or could not) score.
func (p *planner) scoreReady(ctx context.Context, decisions []PlannedCandidate, readyIdx []int) {
	if p.scorer == nil {
		for _, i := range readyIdx {
			p.stubScore(decisions, i)
		}
		return
	}

	cands := make([]collect.Candidate, len(readyIdx))
	for j, i := range readyIdx {
		cands[j] = decisions[i].Candidate
	}
	results := p.llmScore(ctx, cands)

	for j, i := range readyIdx {
		r := results[j]
		if r == nil {
			p.stubScore(decisions, i)
			continue
		}
		if r.Defer {
			p.demoteToDefer(decisions, i, r.Reason)
			continue
		}
		decisions[i].Score = r.Score
		if reason := strings.TrimSpace(r.Reason); reason != "" {
			decisions[i].Reason = "llm scorer: " + reason
		}
	}
}

// stubScore assigns the deterministic priority+deadline score to one ready row.
func (p *planner) stubScore(decisions []PlannedCandidate, i int) {
	c := decisions[i].Candidate
	decisions[i].Score = p.score(c, parseNotes(c.Notes))
}

// demoteToDefer reclassifies a ready candidate the LLM judged "not now". Its
// verdict becomes defer, its status follows the registry (so an operator can
// re-map defer's落とし先 from config), and it leaves the ready列 — but it is not
// dropped, because the rules already proved it worth doing.
func (p *planner) demoteToDefer(decisions []PlannedCandidate, i int, reason string) {
	decisions[i].Verdict = collect.VerdictDefer
	decisions[i].Status = p.registry.policyFor(collect.VerdictDefer).Status
	decisions[i].Score = 0
	decisions[i].Serialized = false
	if r := strings.TrimSpace(reason); r != "" {
		decisions[i].Reason = "llm scorer: " + r
	} else {
		decisions[i].Reason = "llm scorer: deferred (not now)"
	}
}

// llmScore runs the scorer over cands and returns one entry per input, in
// order. A nil entry means "the LLM did not score this — fall back to the
// stub": either the call failed, the result count did not match, or the score
// was an anomaly (NaN/Inf). A total failure yields an all-nil slice so every
// candidate stubs safely.
func (p *planner) llmScore(ctx context.Context, cands []collect.Candidate) []*ScoreResult {
	out := make([]*ScoreResult, len(cands))
	if len(cands) == 0 {
		return out
	}
	items := make([]ScoreItem, len(cands))
	for i, c := range cands {
		items[i] = ScoreItem{Candidate: c}
	}
	results, err := p.scorer.Score(ctx, items)
	if err != nil || len(results) != len(items) {
		return out
	}
	for i := range results {
		if !validScore(results[i]) {
			continue
		}
		r := results[i]
		out[i] = &r
	}
	return out
}

// validScore guards the trust boundary: a non-defer result must carry a finite
// score, or it is rejected and the candidate falls back to the stub. A defer
// result ignores its score, so its finiteness does not matter.
func validScore(r ScoreResult) bool {
	if r.Defer {
		return true
	}
	return !math.IsNaN(r.Score) && !math.IsInf(r.Score, 0)
}
