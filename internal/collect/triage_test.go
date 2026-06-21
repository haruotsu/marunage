package collect_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/store"
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

func TestApplyTaskDecisionAppendsReasonAndKeepsStatus(t *testing.T) {
	s := &fakeStore{}
	const reason = "rule 1: @me directly mentioned"
	err := collect.Apply(context.Background(), s, 7, collect.Decision{
		Decision: collect.DecisionTask,
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

func TestApplySkipDecisionMarksRowSkipped(t *testing.T) {
	s := &fakeStore{}
	const reason = "rule 4: FYI broadcast, not actionable"
	err := collect.Apply(context.Background(), s, 9, collect.Decision{
		Decision: collect.DecisionSkip,
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

func TestApplyUnknownDecisionReturnsError(t *testing.T) {
	s := &fakeStore{}
	err := collect.Apply(context.Background(), s, 1, collect.Decision{
		Decision: "maybe",
		Reason:   "garbled",
	})
	if !errors.Is(err, collect.ErrInvalidDecision) {
		t.Fatalf("Apply(unknown): err = %v; want ErrInvalidDecision", err)
	}
	if len(s.appended)+len(s.skipped) != 0 {
		t.Errorf("store touched on invalid decision; appended=%d skipped=%d",
			len(s.appended), len(s.skipped))
	}
}

func TestApplyRejectsEmptyReason(t *testing.T) {
	s := &fakeStore{}
	err := collect.Apply(context.Background(), s, 1, collect.Decision{
		Decision: collect.DecisionTask,
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

func TestApplyPropagatesSkipStoreError(t *testing.T) {
	wantErr := errors.New("disk full")
	s := &fakeStore{skipErr: wantErr}
	err := collect.Apply(context.Background(), s, 3, collect.Decision{
		Decision: collect.DecisionSkip,
		Reason:   "rule 5: already replied",
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("Apply(skip with store err): err = %v; want it to wrap %v", err, wantErr)
	}
}

func TestApplyPropagatesAppendStoreError(t *testing.T) {
	wantErr := errors.New("locked")
	s := &fakeStore{appErr: wantErr}
	err := collect.Apply(context.Background(), s, 4, collect.Decision{
		Decision: collect.DecisionTask,
		Reason:   "rule 1: direct mention",
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("Apply(task with store err): err = %v; want it to wrap %v", err, wantErr)
	}
}

func TestApplyRedactsSecretsInReasonOnSkipPath(t *testing.T) {
	const leaked = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567"
	s := &fakeStore{}
	err := collect.Apply(context.Background(), s, 5, collect.Decision{
		Decision: collect.DecisionSkip,
		Reason:   "rule 5: already replied; quoted body header: Authorization: Bearer " + leaked,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(s.skipped) != 1 {
		t.Fatalf("MarkSkippedWithReason called %d times; want 1", len(s.skipped))
	}
	if strings.Contains(s.skipped[0].reason, leaked) {
		t.Errorf("skip reason persisted leaked secret %q in:\n%s", leaked, s.skipped[0].reason)
	}
}

func TestApplyRedactsSecretsInReasonOnTaskPath(t *testing.T) {
	const leaked = "xoxb-1234567890-1234567890-AbCdEfGhIjKlMnOpQrStUvWx"
	s := &fakeStore{}
	err := collect.Apply(context.Background(), s, 6, collect.Decision{
		Decision: collect.DecisionTask,
		Reason:   "rule 1: direct mention; body contained slack token " + leaked,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(s.appended) != 1 {
		t.Fatalf("AppendJudgmentReason called %d times; want 1", len(s.appended))
	}
	if strings.Contains(s.appended[0].reason, leaked) {
		t.Errorf("task reason persisted leaked secret %q in:\n%s", leaked, s.appended[0].reason)
	}
}

func TestDecisionDecodesExternalIDFromSkillJSON(t *testing.T) {
	const payload = `{"external_id": "T123.456", "decision": "task", "reason": "rule 1", "priority": 1}`
	var d collect.Decision
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if d.ExternalID != "T123.456" {
		t.Errorf("ExternalID = %q; want %q", d.ExternalID, "T123.456")
	}
	if d.Decision != collect.DecisionTask {
		t.Errorf("Decision = %q; want %q", d.Decision, collect.DecisionTask)
	}
	if d.Priority != 1 {
		t.Errorf("Priority = %d; want 1", d.Priority)
	}
}

func TestErrInvalidDecisionMentionsBothConstants(t *testing.T) {
	s := &fakeStore{}
	err := collect.Apply(context.Background(), s, 1, collect.Decision{
		Decision: "maybe", Reason: "x",
	})
	if !errors.Is(err, collect.ErrInvalidDecision) {
		t.Fatalf("err = %v; want ErrInvalidDecision", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, collect.DecisionTask) || !strings.Contains(msg, collect.DecisionSkip) {
		t.Errorf("error message = %q; want it to mention %q and %q",
			msg, collect.DecisionTask, collect.DecisionSkip)
	}
}

func TestApplyPropagatesNotFoundOnSkipPath(t *testing.T) {
	s := &fakeStore{skipErr: store.ErrNotFound}
	err := collect.Apply(context.Background(), s, 7, collect.Decision{
		Decision: collect.DecisionSkip, Reason: "rule 4: fyi",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Apply(skip on missing id): err = %v; want errors.Is(err, store.ErrNotFound)", err)
	}
}

func TestApplyPropagatesNotFoundOnTaskPath(t *testing.T) {
	s := &fakeStore{appErr: store.ErrNotFound}
	err := collect.Apply(context.Background(), s, 8, collect.Decision{
		Decision: collect.DecisionTask, Reason: "rule 1: direct mention",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Apply(task on missing id): err = %v; want errors.Is(err, store.ErrNotFound)", err)
	}
}
