package manage

import (
	"context"
	"hash/fnv"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"

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

// WithLLMBatchSize sets how many ready candidates go in one scorer call
// (redesign §9.1 batch evaluation: avoid N+1). 0 (the default) sends every
// uncached candidate in a single call. A positive n chunks them into calls of
// at most n, bounded by WithLLMMaxCalls.
func WithLLMBatchSize(n int) Option { return func(p *planner) { p.batchSize = n } }

// WithLLMMaxCalls caps the number of scorer invocations per Plan run (redesign
// §9.1: 1ループあたりの LLM 呼び出し上限 — runaway protection). Candidates beyond
// the cap fall back to the deterministic stub. Defaults to 1, which — paired
// with the default batch size — means exactly one LLM call per loop tick.
func WithLLMMaxCalls(n int) Option { return func(p *planner) { p.maxCalls = n } }

// WithLLMCache memoises LLM judgments across Plan runs so a candidate that
// reappears on the next loop tick is not re-evaluated (redesign §9.1, minimal
// cache). Absent, every run re-scores. NewMemoryScoreCache is the built-in.
func WithLLMCache(c ScoreCache) Option { return func(p *planner) { p.cache = c } }

// ScoreCache memoises LLM judgments keyed by candidate content. The key folds
// in the candidate's body/notes/etc., so an edited candidate re-scores rather
// than serving a stale judgment.
type ScoreCache interface {
	Get(key string) (ScoreResult, bool)
	Put(key string, r ScoreResult)
}

// MemoryScoreCache is the built-in in-process ScoreCache. It is guarded by a
// mutex because the cache outlives a single Plan call and is shared across
// loop ticks; the lock keeps `go test -race` clean even though ticks run
// sequentially today.
type MemoryScoreCache struct {
	mu sync.Mutex
	m  map[string]ScoreResult
}

// NewMemoryScoreCache returns an empty in-process score cache.
func NewMemoryScoreCache() *MemoryScoreCache {
	return &MemoryScoreCache{m: make(map[string]ScoreResult)}
}

func (c *MemoryScoreCache) Get(key string) (ScoreResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[key]
	return r, ok
}

func (c *MemoryScoreCache) Put(key string, r ScoreResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = r
}

// scoreCacheKey folds the candidate's identity and content into a stable key.
// Including the mutable fields (title/body/notes/priority) — not just
// (source, external_id) — means a candidate whose content changed re-scores
// instead of serving a stale judgment.
func scoreCacheKey(c collect.Candidate) string {
	h := fnv.New64a()
	for _, field := range []string{c.Source, c.ExternalID, c.Title, c.Body, c.Notes, c.Priority} {
		_, _ = io.WriteString(h, field)
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 16)
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

	// Resolve cache hits first; only the misses cost an LLM call.
	pending := make([]int, 0, len(cands))
	for i, c := range cands {
		if p.cache != nil {
			if r, ok := p.cache.Get(scoreCacheKey(c)); ok {
				rr := r
				out[i] = &rr
				continue
			}
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return out
	}

	batch := p.batchSize
	if batch <= 0 {
		batch = len(pending) // 0 means "everything in one call"
	}
	maxCalls := p.maxCalls
	if maxCalls <= 0 {
		maxCalls = 1
	}

	calls := 0
	for start := 0; start < len(pending); start += batch {
		if calls >= maxCalls {
			break // cost guard: the rest fall back to the stub
		}
		end := start + batch
		if end > len(pending) {
			end = len(pending)
		}
		idxs := pending[start:end]
		items := make([]ScoreItem, len(idxs))
		for j, idx := range idxs {
			items[j] = ScoreItem{Candidate: cands[idx]}
		}
		calls++
		results, err := p.scorer.Score(ctx, items)
		if err != nil || len(results) != len(items) {
			continue // these stay nil → stub
		}
		for j, idx := range idxs {
			if !validScore(results[j]) {
				continue
			}
			r := results[j]
			out[idx] = &r
			if p.cache != nil {
				p.cache.Put(scoreCacheKey(cands[idx]), r)
			}
		}
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
