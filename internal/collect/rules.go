package collect

import "strings"

// Rule is one deterministic early-triage check. Early triage is
// intentionally rule-only (no LLM) so the collection layer stays cheap
// (redesign Open Question 5): it exists to discard the obvious noise —
// advertisements, notification chatter — before the manage layer spends
// an LLM call deciding "when to do this".
//
// A Rule whose Match returns true stamps its Verdict and Reason onto the
// candidate. Rules are tried in order and the first match wins, so the
// default set is ordered most-specific-first.
type Rule struct {
	// Name is a stable identifier for the rule (audit / config). It is
	// woven into the recorded Reason so `marunage review` shows which
	// rule fired.
	Name string
	// Match reports whether the rule applies to c.
	Match func(c Candidate) bool
	// Verdict is stamped onto a matched candidate. For early triage this
	// is VerdictDrop, but the type is open so a future rule could route a
	// match to needs-human, etc.
	Verdict Verdict
	// Reason is the rationale recorded alongside the Verdict.
	Reason string
}

// DefaultRules returns the built-in early-triage rule set. These pin the
// "明らかなノイズ（広告・GitHub通知等）" examples from redesign §2: Gmail's
// Promotions category (advertisements) and GitHub notification emails
// (the real issues/PRs already arrive through the github source, so the
// email copies are pure noise).
//
// Returned fresh each call so a caller passing it to WithRules and then
// appending its own rules cannot mutate the package-level default.
func DefaultRules() []Rule {
	return []Rule{
		{
			Name:    "gmail-promotions",
			Verdict: VerdictDrop,
			Reason:  "early triage (gmail-promotions): message is in the Gmail Promotions category (advertisement)",
			Match: func(c Candidate) bool {
				return c.Source == "gmail" && hasLabel(c, "CATEGORY_PROMOTIONS")
			},
		},
		{
			Name:    "github-notification-email",
			Verdict: VerdictDrop,
			Reason:  "early triage (github-notification-email): GitHub notification email; the underlying issue/PR is tracked via the github source",
			Match: func(c Candidate) bool {
				return c.Source == "gmail" && senderContains(c, "notifications@github.com")
			},
		},
	}
}

// classify applies the first matching rule to c and returns the verdict
// and reason. No match yields the zero Verdict ("undecided"), leaving the
// candidate for the manage layer to judge.
func classify(c Candidate, rules []Rule) (Verdict, string) {
	for _, r := range rules {
		if r.Match != nil && r.Match(c) {
			return r.Verdict, r.Reason
		}
	}
	return "", ""
}

// hasLabel reports whether the candidate's RawMetadata["labels"] contains
// label. The gmail adapter stores labels as []string, but a value that
// has round-tripped through JSON (map[string]any) arrives as []any, so
// both shapes are handled.
func hasLabel(c Candidate, label string) bool {
	raw, ok := c.RawMetadata["labels"]
	if !ok {
		return false
	}
	switch labels := raw.(type) {
	case []string:
		for _, l := range labels {
			if l == label {
				return true
			}
		}
	case []any:
		for _, l := range labels {
			if s, ok := l.(string); ok && s == label {
				return true
			}
		}
	}
	return false
}

// senderContains reports whether the candidate's RawMetadata["from"]
// sender address contains needle (case-insensitive).
func senderContains(c Candidate, needle string) bool {
	from, ok := c.RawMetadata["from"].(string)
	if !ok {
		return false
	}
	return strings.Contains(strings.ToLower(from), strings.ToLower(needle))
}
