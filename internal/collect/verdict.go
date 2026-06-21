package collect

// Verdict is the management classification a candidate receives. It is
// defined here, in the most upstream layer, because redesign §3.4 makes
// collect the source of the shared vocabulary: the collection layer's
// early triage and the downstream manage layer both speak the same
// Verdict type rather than each re-declaring its own (which would force a
// conversion seam every time a candidate crosses a package boundary).
//
// The five labels below are the documented set (redesign §3.1), but the
// type is a plain string so the vocabulary stays open: a future label
// (e.g. "delegate") is one const away and needs no enum migration. The
// manage layer pairs each Verdict with a behaviour policy via a registry
// that falls back safely on unknown labels (redesign §3.3 原則2), so an
// added const cannot crash a switch that has not learned about it yet.
type Verdict string

const (
	// VerdictReady marks a candidate executable right now; the manage
	// layer ranks ready candidates and hands them to dispatch.
	VerdictReady Verdict = "ready"
	// VerdictHold marks a candidate blocked on a dependency or
	// precondition; it auto-promotes to ready once the condition clears.
	VerdictHold Verdict = "hold"
	// VerdictDefer marks a candidate worth doing but not now (distant
	// deadline, congestion, low priority).
	VerdictDefer Verdict = "defer"
	// VerdictNeedsHuman marks a candidate that must not be handed to an
	// agent unattended (missing info, approval/contract/HR matter).
	VerdictNeedsHuman Verdict = "needs-human"
	// VerdictDrop marks a candidate that should not be done at all
	// (advertisement, notification, out of scope, duplicate). Early
	// triage in collect assigns this to obvious noise so the manage
	// layer never pays LLM cost on it.
	VerdictDrop Verdict = "drop"
)

// knownVerdicts is the closed set of labels collect itself defines.
// Membership is what Known reports; the empty Verdict (the "undecided"
// sentinel a freshly normalised Candidate carries) is deliberately
// absent so callers can distinguish "not yet judged" from a real label.
var knownVerdicts = map[Verdict]struct{}{
	VerdictReady:      {},
	VerdictHold:       {},
	VerdictDefer:      {},
	VerdictNeedsHuman: {},
	VerdictDrop:       {},
}

// Known reports whether v is one of the labels this package defines. The
// manage layer uses it (alongside its policy registry) to route unknown
// or undecided verdicts to a safe fallback rather than trusting an empty
// or typo'd value.
func (v Verdict) Known() bool {
	_, ok := knownVerdicts[v]
	return ok
}
