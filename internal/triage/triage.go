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
	"errors"
	"fmt"

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
var ErrInvalidDecision = errors.New("triage: decision must be \"task\" or \"skip\"")

// Decision is the per-row verdict the skill emits. The field shape
// mirrors the documented JSON-Lines schema in
// internal/skills/embedded/marunage-triage/SKILL.md so the discovery
// layer can json.Unmarshal directly into this struct.
type Decision struct {
	// Decision must be one of DecisionTask / DecisionSkip.
	Decision string `json:"decision"`
	// Reason is the 1-sentence rationale recorded into judgment_reason
	// for the audit trail in `marunage review`. Required.
	Reason string `json:"reason"`
	// Priority carries the skill's optional priority hint; only
	// meaningful for task verdicts and ignored on skip.
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
func Apply(ctx context.Context, s Store, id int64, d Decision) error {
	if d.Reason == "" {
		return store.ErrReasonRequired
	}
	switch d.Decision {
	case DecisionSkip:
		if err := s.MarkSkippedWithReason(ctx, id, d.Reason); err != nil {
			return fmt.Errorf("triage apply skip: %w", err)
		}
		return nil
	case DecisionTask:
		if err := s.AppendJudgmentReason(ctx, id, d.Reason); err != nil {
			return fmt.Errorf("triage apply task: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("%w: got %q", ErrInvalidDecision, d.Decision)
	}
}
