package manage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/policy"
	"github.com/haruotsu/marunage/internal/store"
)

// Rules toggles the deterministic rule-engine cut-offs (redesign §3.2). It
// mirrors config.ManageRulesConfig but with the due window already parsed to
// a time.Duration, so Plan never re-parses a string per candidate. Each rule
// is independently switchable, so the rule set can be tuned from config
// without code changes (RulesFromConfig bridges the two).
type Rules struct {
	// BlockIfDepsIncomplete holds a candidate whose notes.depends_on lists a
	// task that is not yet done.
	BlockIfDepsIncomplete bool
	// EscalateIfBodyEmpty routes a candidate with no body (or no title) to
	// needs-human — there is not enough to act on unattended.
	EscalateIfBodyEmpty bool
	// DropIfCwdViolation drops a candidate whose resolved cwd falls outside
	// the allowed prefixes (absorbs policy.CwdAllowed, redesign §3.2 / §7).
	DropIfCwdViolation bool
	// BoostIfDueWithin raises a ready candidate's score when notes.due falls
	// within this window of now. Zero disables the boost.
	BoostIfDueWithin time.Duration
}

// DefaultRules returns the built-in rule toggles, matching config's documented
// [manage.rules] defaults (redesign §6): every cut-off on and a 24h due-boost
// window. Returned by value so a caller tweaking one field cannot mutate a
// shared default.
func DefaultRules() Rules {
	return Rules{
		BlockIfDepsIncomplete: true,
		EscalateIfBodyEmpty:   true,
		DropIfCwdViolation:    true,
		BoostIfDueWithin:      24 * time.Hour,
	}
}

// candidateNotes is the JSON shape the management layer reads out of a
// candidate's notes blob (redesign §5.3: dependencies and metadata live in
// notes JSON for the MVP). Parsing is best-effort — malformed notes yield the
// zero value rather than an error — so one badly-formatted row cannot abort a
// whole Plan; the missing fields simply leave their rules inert.
type candidateNotes struct {
	DependsOn []int64 `json:"depends_on"`
	Due       string  `json:"due"`
	Cwd       string  `json:"cwd"`
	LockHint  string  `json:"lock_hint"`
}

func parseNotes(notes string) candidateNotes {
	var n candidateNotes
	if strings.TrimSpace(notes) == "" {
		return n
	}
	_ = json.Unmarshal([]byte(notes), &n)
	return n
}

// world is the read-only snapshot of the current task table the cut-off rules
// consult. Building it once per Plan (rather than per candidate) keeps the
// store reads bounded and the evaluation deterministic.
type world struct {
	// doneSkipped holds the (source, external_id) keys of every finished or
	// discarded row, for the duplicate rule.
	doneSkipped map[string]struct{}
	// runningLocks holds the lock_key of every currently running row, for the
	// lock-conflict rule.
	runningLocks map[string]struct{}
}

func dupKey(source, externalID string) string {
	return source + "\x00" + externalID
}

// snapshotWorld reads the done/skipped and running rows once so the per-
// candidate rules can decide duplicate / lock-conflict without re-querying.
// A store failure here is catastrophic (we cannot judge correctly), so it is
// returned to the caller rather than silently producing wrong verdicts.
func snapshotWorld(ctx context.Context, st Store) (world, error) {
	w := world{
		doneSkipped:  map[string]struct{}{},
		runningLocks: map[string]struct{}{},
	}
	finished, err := st.List(ctx, store.ListFilter{
		Statuses: []string{store.StatusDone, store.StatusSkipped},
	})
	if err != nil {
		return world{}, err
	}
	for _, t := range finished {
		if t.ExternalID != "" {
			w.doneSkipped[dupKey(t.Source, t.ExternalID)] = struct{}{}
		}
	}
	running, err := st.List(ctx, store.ListFilter{
		Statuses: []string{store.StatusRunning},
	})
	if err != nil {
		return world{}, err
	}
	for _, t := range running {
		if t.LockKey != "" {
			w.runningLocks[t.LockKey] = struct{}{}
		}
	}
	return w, nil
}

// cutoff applies the deterministic cut-off rules to one candidate and returns
// the first decisive verdict with its reason, or the empty Verdict when no
// rule fires (the candidate proceeds to scoring as ready).
//
// Precedence is decisive-first, most-recoverable-last: drop (cwd violation,
// duplicate) → needs-human (missing info) → hold (incomplete deps). A
// candidate that is out of scope or already handled is discarded before we
// bother holding or escalating it.
//
// The deps rule reads each dependency's status through the store; a missing
// dependency counts as not-done (hold), and any non-NotFound store error is
// returned to the caller.
func (p *planner) cutoff(ctx context.Context, c collect.Candidate, n candidateNotes, w world) (collect.Verdict, string, error) {
	if p.rules.DropIfCwdViolation {
		cwd := n.Cwd
		if cwd == "" {
			cwd = p.defaultCwd
		}
		if !policy.CwdAllowed(cwd, p.cwdPrefixes) {
			return collect.VerdictDrop, "rule (cwd-violation): cwd is outside allowed_cwd_prefixes", nil
		}
	}

	if c.ExternalID != "" {
		if _, dup := w.doneSkipped[dupKey(c.Source, c.ExternalID)]; dup {
			return collect.VerdictDrop, "rule (duplicate): (source, external_id) already done or skipped", nil
		}
	}

	if p.rules.EscalateIfBodyEmpty {
		if strings.TrimSpace(c.Body) == "" || strings.TrimSpace(c.Title) == "" {
			return collect.VerdictNeedsHuman, "rule (info-insufficient): empty title or body", nil
		}
	}

	if p.rules.BlockIfDepsIncomplete && len(n.DependsOn) > 0 {
		incomplete, err := p.depsIncomplete(ctx, n.DependsOn)
		if err != nil {
			return "", "", err
		}
		if incomplete {
			return collect.VerdictHold, "rule (deps-incomplete): a notes.depends_on task is not done", nil
		}
	}

	return "", "", nil
}

// depsIncomplete reports whether any listed dependency is not yet done. A
// dependency the store cannot find is treated as incomplete: it has certainly
// not finished, so the dependent stays held until it appears and completes.
func (p *planner) depsIncomplete(ctx context.Context, deps []int64) (bool, error) {
	for _, id := range deps {
		t, err := p.store.Get(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return true, nil
			}
			return false, err
		}
		if t.Status != store.StatusDone {
			return true, nil
		}
	}
	return false, nil
}

// dueWithinThreshold reports whether the candidate's notes.due is at or before
// now+window. An unparseable or absent due never boosts. Overdue items (due in
// the past) count as within the window — they are the most urgent.
func (p *planner) dueWithinThreshold(n candidateNotes) bool {
	if p.rules.BoostIfDueWithin <= 0 || n.Due == "" {
		return false
	}
	due, ok := parseDue(n.Due)
	if !ok {
		return false
	}
	return !due.After(p.now().Add(p.rules.BoostIfDueWithin))
}

// parseDue accepts the timestamp formats notes.due may carry. RFC3339 is the
// documented form; the store's millisecond layout is also accepted so a value
// copied from a task timestamp round-trips.
func parseDue(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
