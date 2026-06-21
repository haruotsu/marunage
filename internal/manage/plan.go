package manage

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/store"
)

// Store is the narrow read surface Plan needs against the tasks table. Keeping
// it an interface (not the concrete *store.TaskRepo) is the test seam:
// production wires the real repo, tests inject a fake. Both methods belong to
// *store.TaskRepo so the concrete type satisfies it implicitly — the compile-
// time assertion below fails the build if a signature drifts.
type Store interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
	Get(ctx context.Context, id int64) (store.Task, error)
}

var _ Store = (*store.TaskRepo)(nil)

// PlannedCandidate is one candidate after management evaluation: the original
// candidate plus the verdict, rationale, and (for ready rows) the scoring
// output. It is what the cmd wiring (PR-R05) persists into the plan_* columns
// (redesign §5.2).
type PlannedCandidate struct {
	Candidate collect.Candidate
	Verdict   collect.Verdict
	// Reason is the rule- or stub-scorer rationale. For a candidate that
	// arrived already classified by early triage, the upstream reason is
	// preserved verbatim.
	Reason string
	// Score is the stub scorer's ordering value (higher = sooner). Zero for
	// non-ready candidates. PR-R06 replaces the stub with LLM scoring.
	Score float64
	// Rank is the 1-based execution order within the ready列; 0 when the
	// candidate is not ready.
	Rank int
	// Serialized marks a ready candidate held back by a lock conflict: its
	// verdict stays ready but it is ordered after the unconstrained rows
	// (redesign §3.2 "ready だが直列化制約（順序のみ後送り）").
	Serialized bool
}

// ReadyPlan is the output of Plan (redesign §3.3): the ordered ready列 plus a
// full record of every candidate's verdict so the caller can persist the
// dropped / held / escalated rows too (invariant #1 No silent loss).
type ReadyPlan struct {
	// Ready is the dispatch-ordered list of ready candidates (Rank 1 first).
	Ready []PlannedCandidate
	// Decisions records every input candidate, in input order, with its
	// verdict / reason / score. Ready candidates also carry their rank here.
	Decisions []PlannedCandidate
}

// Option configures Plan. The zero set yields production defaults: the real
// clock, DefaultRules, DefaultRegistry, no cwd allowlist (all paths allowed),
// and no lock-key resolution.
type Option func(*planner)

// WithRules overrides the rule-engine toggles. Defaults to DefaultRules.
func WithRules(r Rules) Option { return func(p *planner) { p.rules = r } }

// WithRegistry overrides the verdict→policy registry. Defaults to
// DefaultRegistry; PR-R05 passes a config-driven one (VerdictPoliciesFromConfig).
func WithRegistry(r Registry) Option {
	return func(p *planner) {
		if r != nil {
			p.registry = r
		}
	}
}

// WithAllowedCwdPrefixes installs the cwd allowlist the cwd-violation rule
// enforces. Empty/nil means "all paths allowed" (policy.CwdAllowed semantics).
func WithAllowedCwdPrefixes(prefixes []string) Option {
	return func(p *planner) { p.cwdPrefixes = prefixes }
}

// WithDefaultCwd sets the fallback cwd used when a candidate's notes carry no
// cwd, mirroring the dispatcher's effective-cwd resolution.
func WithDefaultCwd(cwd string) Option { return func(p *planner) { p.defaultCwd = cwd } }

// WithLockKeys installs the [execution.lock_keys] regex map used to resolve a
// candidate's notes.lock_hint for the lock-conflict rule. Absent means no
// candidate ever resolves a lock key and the rule never fires.
func WithLockKeys(m map[string]string) Option { return func(p *planner) { p.lockKeys = m } }

// WithClock injects a deterministic clock for the due-boost window. Defaults
// to time.Now.
func WithClock(now func() time.Time) Option {
	return func(p *planner) {
		if now != nil {
			p.now = now
		}
	}
}

type planner struct {
	store       Store
	rules       Rules
	registry    Registry
	cwdPrefixes []string
	defaultCwd  string
	lockKeys    map[string]string
	now         func() time.Time
}

// dueBoost is the score added to a ready candidate whose deadline falls inside
// the configured window. It is large enough to float a due-soon row above its
// equal-priority peers (the documented "priority boost"); the magnitude is a
// stub placeholder until PR-R06's LLM scoring assigns real values.
const dueBoost = 1000.0

// Plan classifies every candidate and returns an ordered ready列 (redesign
// §3). It runs the deterministic rule engine first (cheap cut-offs to hold /
// needs-human / drop), then a stub scorer that orders the survivors by
// priority — so Plan runs end to end with no LLM configured (PR-R03). A
// candidate that already carries a verdict from collect's early triage is
// respected verbatim and not re-judged.
//
// A non-nil error is reserved for catastrophic store failures (the world
// snapshot or a dependency lookup blew up); per-candidate classification never
// errors.
func Plan(ctx context.Context, candidates []collect.Candidate, st Store, opts ...Option) (ReadyPlan, error) {
	p := &planner{
		store:    st,
		rules:    DefaultRules(),
		registry: DefaultRegistry(),
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}

	w, err := snapshotWorld(ctx, st)
	if err != nil {
		return ReadyPlan{}, err
	}

	decisions := make([]PlannedCandidate, 0, len(candidates))
	readyIdx := make([]int, 0, len(candidates))
	for _, c := range candidates {
		n := parseNotes(c.Notes)

		verdict := c.Verdict
		reason := c.Reason
		if verdict == "" {
			v, r, err := p.cutoff(ctx, c, n, w)
			if err != nil {
				return ReadyPlan{}, err
			}
			if v != "" {
				verdict, reason = v, r
			} else {
				verdict, reason = collect.VerdictReady, "rule engine: cleared for scoring"
			}
		}

		pc := PlannedCandidate{Candidate: c, Verdict: verdict, Reason: reason}
		if p.registry.policyFor(verdict).Dispatchable {
			pc.Score = p.score(c, n)
			pc.Serialized = p.lockConflict(c, w)
			readyIdx = append(readyIdx, len(decisions))
		}
		decisions = append(decisions, pc)
	}

	rankReady(decisions, readyIdx)

	ready := make([]PlannedCandidate, 0, len(readyIdx))
	for _, i := range readyIdx {
		ready = append(ready, decisions[i])
	}
	return ReadyPlan{Ready: ready, Decisions: decisions}, nil
}

// score is the stub LLM scorer: a deterministic value from the candidate's
// priority hint plus a deadline boost. PR-R06 swaps this for the real Claude
// scoring pass; until then ordering is reproducible and LLM-free.
func (p *planner) score(c collect.Candidate, n candidateNotes) float64 {
	s := parsePriority(c.Priority)
	if p.dueWithinThreshold(n) {
		s += dueBoost
	}
	return s
}

// parsePriority reads the candidate's free-form priority hint as a number,
// defaulting to 0 when empty or non-numeric. The free-form string is the
// collection layer's contract (source.Task.Priority); the stub scorer only
// needs a deterministic ordering key.
func parsePriority(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return 0
}

// lockConflict reports whether the candidate's resolved lock key is already
// held by a running task. Resolution reuses dispatch.ResolveLockKey so the
// regex semantics stay identical to the dispatcher's (redesign §7 folds this
// concern into manage; until that lands, sharing the resolver avoids
// divergence). A resolve error is treated as "no lock" — best-effort, since a
// lock conflict only reorders a ready row, never changes its verdict.
func (p *planner) lockConflict(c collect.Candidate, w world) bool {
	if len(p.lockKeys) == 0 || len(w.runningLocks) == 0 {
		return false
	}
	key, err := dispatch.ResolveLockKey(p.lockKeys, c.Notes)
	if err != nil || key == "" {
		return false
	}
	_, held := w.runningLocks[key]
	return held
}

// rankReady orders the ready candidates and stamps their 1-based Rank in
// place. Non-serialized rows come first, then serialized (lock-contended)
// rows, each group ordered by descending score with a stable tiebreak on the
// original input order — so the result is fully deterministic.
func rankReady(decisions []PlannedCandidate, readyIdx []int) {
	sort.SliceStable(readyIdx, func(a, b int) bool {
		da, db := decisions[readyIdx[a]], decisions[readyIdx[b]]
		if da.Serialized != db.Serialized {
			return !da.Serialized // non-serialized first
		}
		return da.Score > db.Score
	})
	for rank, i := range readyIdx {
		decisions[i].Rank = rank + 1
	}
}
