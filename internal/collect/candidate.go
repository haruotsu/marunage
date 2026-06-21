package collect

import (
	"strings"

	"github.com/haruotsu/marunage/internal/source"
)

// Candidate is the normalised, collection-layer view of one discovered
// item. It is what flows out of collect.Gather and into the downstream
// manage layer (redesign §2). Defining it here — not in source or manage
// — keeps the most upstream package the single source of the shared type
// (redesign §3.4), so a candidate crosses the collect→manage boundary
// without a conversion seam.
//
// The field set is a near-superset of source.Task (the raw plugin view)
// plus the early-triage outcome: Verdict and Reason. A normalised
// Candidate starts with Verdict == "" ("undecided"); early triage may
// stamp VerdictDrop on obvious noise so the manage layer can skip it
// without paying LLM cost.
type Candidate struct {
	// Source is the plugin name that produced this candidate. Gather
	// forces it to the plugin's own Name() so a misbehaving plugin
	// cannot smuggle items under another source's identity.
	Source string

	// ExternalID is the upstream-stable identifier; combined with Source
	// it is the (source, external_id) uniqueness key the queue enforces.
	ExternalID string

	// Title is the one-line summary; whitespace-trimmed during normalise.
	Title string

	// Body is the richer multi-line detail. May be empty.
	Body string

	// Notes carries plugin-side annotations (and, downstream, the manage
	// layer's depends_on / due metadata in JSON).
	Notes string

	// Priority is the plugin's free-form priority hint, if any.
	Priority string

	// SourcePath is the file/URL/channel the item came from.
	SourcePath string

	// Done flags upstream completion at observation time.
	Done bool

	// RawMetadata is the source-specific extras bag (gmail labels, slack
	// thread_ts, ...). Early-triage rules read it to spot noise.
	RawMetadata map[string]any

	// Verdict is the early-triage outcome. The empty Verdict means
	// "undecided" — the candidate passed early triage untouched and the
	// manage layer still owns its classification. VerdictDrop means early
	// triage rejected it as obvious noise.
	Verdict Verdict

	// Reason is the human-readable rationale for a non-empty Verdict,
	// recorded for the `marunage review` audit trail. Empty when Verdict
	// is undecided. Early triage only ever sets it from static Rule.Reason
	// strings, so it carries no message content today; any wiring that
	// persists this Reason to a sink (the cmd path in PR-R05) MUST route
	// it through logging.Redact first, exactly as Apply does — never trust
	// it to be secret-free, since a custom rule could embed message text.
	Reason string
}

// normalise lifts a source.Task into a Candidate, forcing Source to the
// owning plugin's name and trimming the title. The verdict is left empty
// here; classify (early triage) assigns it afterwards.
func normalise(t source.Task, sourceName string) Candidate {
	return Candidate{
		Source:      sourceName,
		ExternalID:  t.ExternalID,
		Title:       strings.TrimSpace(t.Title),
		Body:        t.Body,
		Notes:       t.Notes,
		Priority:    t.Priority,
		SourcePath:  t.SourcePath,
		Done:        t.Done,
		RawMetadata: t.RawMetadata,
	}
}
