package triage_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/triage"
)

// fakeStore captures the calls Apply makes so the tests can assert
// the (status transition, judgment_reason write, title overwrite)
// triplet without spinning up SQLite.
type fakeStore struct {
	skipped  []recorded
	appended []recorded
	titles   []recorded
	skipErr  error
	appErr   error
	titleErr error
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

func (s *fakeStore) UpdateTitle(_ context.Context, id int64, title string) error {
	s.titles = append(s.titles, recorded{id, title})
	return s.titleErr
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

// PR-72 TR7 (review-fix-loop iter 1): the triage skill output may
// quote message bodies that carry tokens (Bearer headers, GitHub
// PATs, Slack tokens). Apply must run Reason through logging.Redact
// BEFORE handing it to the store, mirroring dispatch.markFailed so a
// leaked secret never lands in tasks.judgment_reason.
func TestApplyRedactsSecretsInReasonOnSkipPath(t *testing.T) {
	const leaked = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567"
	s := &fakeStore{}
	err := triage.Apply(context.Background(), s, 5, triage.Decision{
		Decision: triage.DecisionSkip,
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
	err := triage.Apply(context.Background(), s, 6, triage.Decision{
		Decision: triage.DecisionTask,
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

// PR-72 TR8 (review-fix-loop iter 1): Decision must carry ExternalID
// so the discovery layer can json.Unmarshal directly into this struct
// (matching SKILL.md's documented JSON-Lines schema).
func TestDecisionDecodesExternalIDFromSkillJSON(t *testing.T) {
	const payload = `{"external_id": "T123.456", "decision": "task", "reason": "rule 1", "priority": 1}`
	var d triage.Decision
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if d.ExternalID != "T123.456" {
		t.Errorf("ExternalID = %q; want %q", d.ExternalID, "T123.456")
	}
	if d.Decision != triage.DecisionTask {
		t.Errorf("Decision = %q; want %q", d.Decision, triage.DecisionTask)
	}
	if d.Priority != 1 {
		t.Errorf("Priority = %d; want 1", d.Priority)
	}
}

// PR-72 TR9 (review-fix-loop iter 1): ErrInvalidDecision message
// must enumerate the actual constant values rather than hardcoded
// literals — adding a future verdict (e.g. DecisionDefer) shouldn't
// silently leave the error wording stale.
func TestErrInvalidDecisionMentionsBothConstants(t *testing.T) {
	s := &fakeStore{}
	err := triage.Apply(context.Background(), s, 1, triage.Decision{
		Decision: "maybe", Reason: "x",
	})
	if !errors.Is(err, triage.ErrInvalidDecision) {
		t.Fatalf("err = %v; want ErrInvalidDecision", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, triage.DecisionTask) || !strings.Contains(msg, triage.DecisionSkip) {
		t.Errorf("error message = %q; want it to mention %q and %q",
			msg, triage.DecisionTask, triage.DecisionSkip)
	}
}

// PR-72 TR10 (review-fix-loop iter 2): a stale id (the row was
// deleted between discovery and apply) surfaces as store.ErrNotFound
// on both branches. Pinning this prevents a future refactor from
// swallowing the error and making `marunage review` lose the audit
// trail for verdicts that never landed.
func TestApplyPropagatesNotFoundOnSkipPath(t *testing.T) {
	s := &fakeStore{skipErr: store.ErrNotFound}
	err := triage.Apply(context.Background(), s, 7, triage.Decision{
		Decision: triage.DecisionSkip, Reason: "rule 4: fyi",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Apply(skip on missing id): err = %v; want errors.Is(err, store.ErrNotFound)", err)
	}
}

func TestApplyPropagatesNotFoundOnTaskPath(t *testing.T) {
	s := &fakeStore{appErr: store.ErrNotFound}
	err := triage.Apply(context.Background(), s, 8, triage.Decision{
		Decision: triage.DecisionTask, Reason: "rule 1: direct mention",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Apply(task on missing id): err = %v; want errors.Is(err, store.ErrNotFound)", err)
	}
}

// task 判定で Title が指定されていれば、source プラグインが入れた
// raw タイトルを「対象 + 動詞」形式の改善タイトルで上書きする。
// 一覧で「何にどう対応する計画か」が一目で分かるようにするための
// 主目的の挙動。
func TestApplyTaskDecisionWithTitleOverwritesTitle(t *testing.T) {
	s := &fakeStore{}
	const newTitle = "佐藤さんに来週MTG日程を返信"
	err := triage.Apply(context.Background(), s, 11, triage.Decision{
		Decision: triage.DecisionTask,
		Reason:   "rule 1: @me が直接メンションされている",
		Title:    newTitle,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(s.appended) != 1 {
		t.Fatalf("AppendJudgmentReason called %d times; want 1", len(s.appended))
	}
	if len(s.titles) != 1 {
		t.Fatalf("UpdateTitle called %d times; want 1", len(s.titles))
	}
	got := s.titles[0]
	if got.id != 11 {
		t.Errorf("UpdateTitle id = %d; want 11", got.id)
	}
	if got.reason != newTitle {
		t.Errorf("UpdateTitle title = %q; want %q", got.reason, newTitle)
	}
}

// Title が空の task 判定は UpdateTitle を呼ばない。後方互換: 旧 SKILL.md
// (title フィールド未対応) の Discovery 出力は Title="" でデコードされる
// ので、その場合は raw タイトルをそのまま残す。
func TestApplyTaskDecisionEmptyTitleSkipsUpdateTitle(t *testing.T) {
	s := &fakeStore{}
	err := triage.Apply(context.Background(), s, 12, triage.Decision{
		Decision: triage.DecisionTask,
		Reason:   "rule 1: direct mention",
		Title:    "",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(s.titles) != 0 {
		t.Errorf("UpdateTitle called %d times on empty title; want 0", len(s.titles))
	}
	if len(s.appended) != 1 {
		t.Errorf("AppendJudgmentReason called %d times; want 1", len(s.appended))
	}
}

// skip 判定では Title が指定されていても無視する。skip された行は
// 一覧の上位に出ない / 操作されないので、タイトル整形コストを払う
// 意味がない。
func TestApplySkipDecisionIgnoresTitle(t *testing.T) {
	s := &fakeStore{}
	err := triage.Apply(context.Background(), s, 13, triage.Decision{
		Decision: triage.DecisionSkip,
		Reason:   "rule 4: FYI broadcast",
		Title:    "ignored title",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(s.titles) != 0 {
		t.Errorf("UpdateTitle called %d times on skip; want 0", len(s.titles))
	}
	if len(s.skipped) != 1 {
		t.Errorf("MarkSkippedWithReason called %d times; want 1", len(s.skipped))
	}
}

// Reason と同じく Title も logging.Redact を通してから store に渡す。
// LLM が message body を quote した結果として Bearer / GitHub PAT /
// Slack token を埋めてしまっても tasks.title に永続化させない。
func TestApplyRedactsSecretsInTitle(t *testing.T) {
	const leaked = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567"
	s := &fakeStore{}
	err := triage.Apply(context.Background(), s, 14, triage.Decision{
		Decision: triage.DecisionTask,
		Reason:   "rule 1: direct mention",
		Title:    "API trace で " + leaked + " を確認",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(s.titles) != 1 {
		t.Fatalf("UpdateTitle called %d times; want 1", len(s.titles))
	}
	if strings.Contains(s.titles[0].reason, leaked) {
		t.Errorf("title persisted leaked secret %q in:\n%s", leaked, s.titles[0].reason)
	}
}

// UpdateTitle が store エラーを返したら Apply 全体を失敗させる。
// reason は記録できたがタイトルだけ更新失敗、という silent partial state を
// 残さないため。Discovery 側はリトライ / オペレータ通知を選択できる。
func TestApplyPropagatesUpdateTitleStoreError(t *testing.T) {
	wantErr := errors.New("disk full")
	s := &fakeStore{titleErr: wantErr}
	err := triage.Apply(context.Background(), s, 15, triage.Decision{
		Decision: triage.DecisionTask,
		Reason:   "rule 1: direct mention",
		Title:    "新タイトル",
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("Apply(task with title err): err = %v; want it to wrap %v", err, wantErr)
	}
}

// SKILL.md の JSON-Lines 出力に title フィールドが含まれていれば
// Decision に正しくデコードされること。ExternalID テストと同型。
func TestDecisionDecodesTitleFromSkillJSON(t *testing.T) {
	const payload = `{"external_id": "T123.456", "decision": "task", "reason": "rule 1", "title": "佐藤さんに返信", "priority": 1}`
	var d triage.Decision
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if d.Title != "佐藤さんに返信" {
		t.Errorf("Title = %q; want %q", d.Title, "佐藤さんに返信")
	}
}
