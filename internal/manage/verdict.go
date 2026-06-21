// Package manage is marunage's management layer (redesign §3): the second
// gate that, for items already confirmed as ours by the collection layer,
// decides whether to dispatch now (ready), wait (hold), postpone (defer),
// escalate to a human (needs-human), or discard (drop).
//
// Plan is the entry point. It runs a deterministic rule engine (redesign
// §3.2) that cheaply cuts off hold / needs-human / drop before any LLM
// spend, then scores the survivors into an ordered ready列 (redesign §3.3).
// The LLM scoring pass is a deterministic stub in this skeleton (PR-R03);
// PR-R06 replaces it with the real Claude call. Plan therefore runs end to
// end with no LLM configured.
//
// manage holds no state: it reads the current task table through the narrow
// injected Store interface and returns a ReadyPlan, leaving persistence to
// the cmd wiring (PR-R05). The shared Candidate / Verdict vocabulary is
// defined upstream in internal/collect and imported here (redesign §3.4),
// never re-declared.
package manage

import (
	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/store"
)

// VerdictPolicy is the per-verdict behaviour the management layer applies
// (redesign §3.3 原則2). Status is the DB status the verdict lands on,
// Dispatchable gates whether the dispatcher may pick the row, and Notify
// flags whether a human should be pinged. It deliberately mirrors
// config.VerdictPolicy so the config-driven [manage.verdicts] table can
// populate a Registry verbatim (see VerdictPoliciesFromConfig), keeping the
// verdict→status mapping (原則1: verdict 語彙と status 語彙の分離) out of code.
type VerdictPolicy struct {
	// Status is the stable DB status (store.Status*) the verdict resolves
	// to. The verdict vocabulary is open and grows; the status enum does
	// not (原則1), so this mapping is the seam between the two.
	Status string
	// Dispatchable reports whether a row carrying this verdict is eligible
	// for immediate dispatch. Plan uses it — not a hardcoded `== ready` —
	// to decide which candidates enter the ready列, so a future
	// dispatchable verdict needs no change to Plan.
	Dispatchable bool
	// Notify reports whether a human should be alerted (e.g. needs-human).
	Notify bool
}

// Registry maps each Verdict to its behaviour policy and falls back safely
// on unknown labels (redesign §3.3 原則2). Holding the mapping in a map —
// rather than a switch — means an added verdict const cannot silently break
// a switch that has not learned about it: policyFor routes anything missing
// to the needs-human policy so an unrecognised label escalates to a human
// instead of, say, dispatching by accident.
type Registry map[collect.Verdict]VerdictPolicy

// DefaultRegistry returns the built-in verdict→policy mapping. The five
// entries match config's documented [manage.verdicts] defaults (redesign §6)
// so manage behaves identically whether or not the config table is wired in.
// Returned fresh each call so a caller mutating the result cannot corrupt the
// package default.
func DefaultRegistry() Registry {
	return Registry{
		collect.VerdictReady:      {Status: store.StatusPending, Dispatchable: true},
		collect.VerdictHold:       {Status: store.StatusPending, Dispatchable: false},
		collect.VerdictDefer:      {Status: store.StatusPending, Dispatchable: false},
		collect.VerdictNeedsHuman: {Status: store.StatusWaitingHuman, Dispatchable: false, Notify: true},
		collect.VerdictDrop:       {Status: store.StatusSkipped, Dispatchable: false},
	}
}

// policyFor returns the policy registered for v, falling back to the
// needs-human policy when v is unknown or undecided (the empty Verdict).
// Escalating an unrecognised verdict to a human — rather than dropping or
// dispatching it — is the safe default (原則2): a typo or a not-yet-taught
// label surfaces for review instead of taking an irreversible action.
//
// The fallback assumes the registry defines needs-human; DefaultRegistry
// and VerdictPoliciesFromConfig both guarantee it. A registry missing that
// entry returns the zero VerdictPolicy (Dispatchable=false), which is still
// safe — it just won't carry the notify flag.
func (r Registry) policyFor(v collect.Verdict) VerdictPolicy {
	if p, ok := r[v]; ok {
		return p
	}
	return r[collect.VerdictNeedsHuman]
}
