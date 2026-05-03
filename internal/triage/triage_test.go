package triage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/triage"
)

// fakeStore captures the calls Apply makes so the tests can assert
// the (status transition, judgment_reason write) pair without
// spinning up SQLite.
type fakeStore struct {
	skipped  []recorded
	appended []recorded
	skipErr  error
	appErr   error
}

type recorded struct {
	id     int64
	reason string
}

func (s *fakeStore) MarkSkippedWithReason(_ context.Context, id int64, reason string) error {
	s.skipped = append(s.skipped, recorded{id, reason})
	return s.skipErr
}

func (s *fakeStore) AppendJudgmentReason(_ context.Context, id int64, suffix string) error {
	s.appended = append(s.appended, recorded{id, suffix})
	return s.appErr
}

// PR-72 TR1: a "task" decision records the triage reason on the row
// (so `marunage review` can quote which rule matched) without changing
// the status — the dispatcher will continue with the row in pending.
func TestApplyTaskDecisionAppendsReasonAndKeepsStatus(t *testing.T) {
	s := &fakeStore{}
	const reason = "rule 1: @me directly mentioned"
	err := triage.Apply(context.Background(), s, 7, triage.Decision{
		Decision: triage.DecisionTask,
		Reason:   reason,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(s.skipped) != 0 {
		t.Errorf("MarkSkippedWithReason called %d times; want 0", len(s.skipped))
	}
	if len(s.appended) != 1 {
		t.Fatalf("AppendJudgmentReason called %d times; want 1", len(s.appended))
	}
	got := s.appended[0]
	if got.id != 7 {
		t.Errorf("appended id = %d; want 7", got.id)
	}
	if !strings.Contains(got.reason, reason) {
		t.Errorf("appended reason = %q; want it to contain %q", got.reason, reason)
	}
}

// PR-72 TR2: a "skip" decision flips the row to skipped via
// MarkSkippedWithReason, carrying the triage rationale.
func TestApplySkipDecisionMarksRowSkipped(t *testing.T) {
	s := &fakeStore{}
	const reason = "rule 4: FYI broadcast, not actionable"
	err := triage.Apply(context.Background(), s, 9, triage.Decision{
		Decision: triage.DecisionSkip,
		Reason:   reason,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(s.appended) != 0 {
		t.Errorf("AppendJudgmentReason called %d times; want 0", len(s.appended))
	}
	if len(s.skipped) != 1 {
		t.Fatalf("MarkSkippedWithReason called %d times; want 1", len(s.skipped))
	}
	got := s.skipped[0]
	if got.id != 9 {
		t.Errorf("skipped id = %d; want 9", got.id)
	}
	if !strings.Contains(got.reason, reason) {
		t.Errorf("skipped reason = %q; want it to contain %q", got.reason, reason)
	}
}

// PR-72 TR3: an unknown decision string is a Discovery / triage skill
// bug — Apply must return ErrInvalidDecision rather than silently
// defaulting one way or the other.
func TestApplyUnknownDecisionReturnsError(t *testing.T) {
	s := &fakeStore{}
	err := triage.Apply(context.Background(), s, 1, triage.Decision{
		Decision: "maybe",
		Reason:   "garbled",
	})
	if !errors.Is(err, triage.ErrInvalidDecision) {
		t.Fatalf("Apply(unknown): err = %v; want ErrInvalidDecision", err)
	}
	if len(s.appended)+len(s.skipped) != 0 {
		t.Errorf("store touched on invalid decision; appended=%d skipped=%d",
			len(s.appended), len(s.skipped))
	}
}

// PR-72 TR4: empty reason fails loud — judgment_reason must always
// carry an audit string per docs/requirement.md "No silent execution".
func TestApplyRejectsEmptyReason(t *testing.T) {
	s := &fakeStore{}
	err := triage.Apply(context.Background(), s, 1, triage.Decision{
		Decision: triage.DecisionTask,
		Reason:   "",
	})
	if !errors.Is(err, store.ErrReasonRequired) {
		t.Fatalf("Apply(empty reason): err = %v; want ErrReasonRequired", err)
	}
	if len(s.appended)+len(s.skipped) != 0 {
		t.Errorf("store touched on empty reason; appended=%d skipped=%d",
			len(s.appended), len(s.skipped))
	}
}

// PR-72 TR5: a store-level failure on the skip path bubbles up so the
// caller (Discovery / dispatch) can decide whether to retry or surface
// the error to the operator.
func TestApplyPropagatesSkipStoreError(t *testing.T) {
	wantErr := errors.New("disk full")
	s := &fakeStore{skipErr: wantErr}
	err := triage.Apply(context.Background(), s, 3, triage.Decision{
		Decision: triage.DecisionSkip,
		Reason:   "rule 5: already replied",
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("Apply(skip with store err): err = %v; want it to wrap %v", err, wantErr)
	}
}

// PR-72 TR6: same propagation contract on the task path.
func TestApplyPropagatesAppendStoreError(t *testing.T) {
	wantErr := errors.New("locked")
	s := &fakeStore{appErr: wantErr}
	err := triage.Apply(context.Background(), s, 4, triage.Decision{
		Decision: triage.DecisionTask,
		Reason:   "rule 1: direct mention",
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("Apply(task with store err): err = %v; want it to wrap %v", err, wantErr)
	}
}
