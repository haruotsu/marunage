// Package triage records the OODA Orient verdicts the embedded
// marunage-triage skill produces against discovered rows. The skill
// itself runs inside a Claude session (it lives in
// internal/skills/embedded/marunage-triage/SKILL.md and is delivered
// to ~/.claude/skills/ by `marunage setup --skills`); the Go side here
// is intentionally narrow:
//
//   - Decision is the small struct the skill's JSON-Lines output decodes
//     into.
//   - Apply is the persistence hook: a "skip" verdict flips the row to
//     skipped via store.MarkSkippedWithReason; a "task" verdict appends
//     the rationale to judgment_reason so the row stays pending for the
//     dispatcher and `marunage review` can still see which rule matched.
//
// Keeping the hook in its own package (rather than inside dispatch)
// makes the OODA Orient phase a discrete thing — the dispatch package
// stays focused on Act.
package triage

import (
	"context"
	"fmt"

	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/store"
)

// Verdict strings mirror the JSON values the embedded
// marunage-triage SKILL.md emits. Centralising them here keeps the
// skill output and the dispatcher hook from drifting.
const (
	DecisionTask = "task"
	DecisionSkip = "skip"
)

// ErrInvalidDecision signals that the triage skill emitted a value
// outside {task, skip}. Either the user edited SKILL.md and produced
// a typo, or the discovery plugin parsed the JSON wrong; either way
// failing loud beats silently choosing one branch.
var ErrInvalidDecision = fmt.Errorf("triage: decision must be %q or %q", DecisionTask, DecisionSkip)

// Decision is the per-row verdict the skill emits. The field shape
// mirrors the documented JSON-Lines schema in
// internal/skills/embedded/marunage-triage/SKILL.md so the discovery
// layer can json.Unmarshal directly into this struct.
type Decision struct {
	// ExternalID echoes the source-side identifier (Slack ts, GitHub
	// issue number, etc.). Apply does not consume it directly — the
	// row id is passed in separately — but the field is present so a
	// json.Unmarshal of one SKILL.md output line round-trips without
	// loss, leaving the discovery layer free to map ExternalID -> row
	// id at its own seam.
	ExternalID string `json:"external_id"`
	// Decision must be one of DecisionTask / DecisionSkip.
	Decision string `json:"decision"`
	// Reason is the 1-sentence rationale recorded into judgment_reason
	// for the audit trail in `marunage review`. Required.
	Reason string `json:"reason"`
	// Priority carries the skill's optional priority hint; only
	// meaningful for task verdicts. Phase 1 of marunage does not
	// surface priority into the dispatcher's queue ordering yet, so
	// Apply currently records the verdict without acting on this
	// field — keep it on the struct so the JSON contract round-trips
	// and a later PR can wire it into tasks.priority.
	Priority int `json:"priority,omitempty"`
}

// Store is the narrow write surface Apply needs against the tasks
// table. Keeping it as an interface (rather than the concrete
// *store.TaskRepo) is the test seam: production wires the real repo,
// tests can swap in a fake. Both methods are members of *store.TaskRepo
// so the concrete type satisfies it implicitly.
type Store interface {
	MarkSkippedWithReason(ctx context.Context, id int64, reason string) error
	AppendJudgmentReason(ctx context.Context, id int64, suffix string) error
}

// Apply persists the triage verdict for row id. A "skip" decision
// transitions the row to skipped; a "task" decision leaves the status
// alone but records the rationale into judgment_reason so the post-
// mortem in `marunage review` can still see which rule matched.
//
// Empty reason rejects with store.ErrReasonRequired so the audit
// invariant "judgment_reason carries an explanation whenever the
// triage hook touches a row" stays enforceable. An unknown decision
// string returns ErrInvalidDecision without touching the store.
//
// Reason is run through logging.Redact before either store call so a
// triage rule that quoted a message body containing a Bearer header /
// GitHub PAT / Slack token cannot pin the secret into the persistent
// judgment_reason column. This mirrors dispatch.markFailed's defence
// in depth — both sinks the operator can read post-mortem must scrub
// secrets at the boundary, never trust the upstream caller.
//
// IDEMPOTENCY: Apply is NOT idempotent. The skip branch overwrites
// judgment_reason (so re-applying the same skip verdict is benign);
// the task branch APPENDS via store.AppendJudgmentReason, so calling
// Apply twice with the same task verdict grows the column. The
// caller (Discovery) owns dedup — a freshly-discovered row should
// have its verdict applied exactly once per discovery run.
//
// AUDIT: Apply does not emit audit events of its own. judgment_reason
// is the operator-visible record; the append-only audit.log is
// reserved for events the dispatcher records (`dispatch.start`,
// `dispatch.fail`, `dispatch.escalate`). If a future Discovery
// caller needs to prove "verdict V was applied at time T", record
// the audit event at the call site BEFORE Apply returns so a later
// judgment_reason overwrite cannot erase the evidence.
func Apply(ctx context.Context, s Store, id int64, d Decision) error {
	if d.Reason == "" {
		return store.ErrReasonRequired
	}
	reason := logging.Redact(d.Reason)
	switch d.Decision {
	case DecisionSkip:
		if err := s.MarkSkippedWithReason(ctx, id, reason); err != nil {
			return fmt.Errorf("triage apply skip: %w", err)
		}
		return nil
	case DecisionTask:
		if err := s.AppendJudgmentReason(ctx, id, reason); err != nil {
			return fmt.Errorf("triage apply task: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("%w: got %q", ErrInvalidDecision, d.Decision)
	}
}
