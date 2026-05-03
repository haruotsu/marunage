package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// Test list for PR-11 (tasks repository layer).
//
//   1. Insert + Get minimal (source, title) round-trips with defaults
//      (Status=pending, CreatedAt populated, UpdatedAt==CreatedAt, Priority=0)
//   2. Insert + Get full round-trip across every column
//   3. Get missing id -> ErrNotFound
//   4. Insert without Source / Title -> validation error
//   5. Insert with invalid Status value -> ErrInvalidStatus
//   6. (source, external_id) duplicate Insert -> ErrDuplicateExternalID
//   7. (source, NULL external_id) duplicates remain insertable (manual add)
//   8. List() with no filter returns all rows in dispatch order
//      (priority DESC, created_at ASC)
//   9. List() filters by status (single + multiple)
//  10. List() filters by source (single + multiple)
//  11. List() honours Limit
//  12. UpdateStatus pending -> running succeeds
//  13. UpdateStatus to invalid status -> ErrInvalidStatus, row unchanged
//  14. UpdateStatus on missing id -> ErrNotFound
//  15. SetWorkspace stores ws reference and bumps updated_at
//  16. SetWorkspace on missing id -> ErrNotFound
//  17. AcquireLock on a free key claims it and persists lock_key
//  18. AcquireLock when another running task holds the same lock_key
//      -> ErrLockHeld, row unchanged
//  19. AcquireLock succeeds again after the previous holder transitions
//      to done/failed/skipped (the lock is implicitly released by status
//      change because the conflict probe is "running with same lock_key")
//  20. ReleaseLock clears lock_key
//  21. ReleaseLock on missing id -> ErrNotFound
//  22. AcquireLock with empty lockKey -> validation error

// repoFixture wires a TaskRepo to a fresh on-disk SQLite plus a deterministic
// clock so every test below can pin timestamps without sleeping.
type repoFixture struct {
	repo *store.TaskRepo
	now  *time.Time
	ctx  context.Context
}

func newRepoFixture(t *testing.T) repoFixture {
	t.Helper()
	db := openTempDB(t)
	clock := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return clock }))
	return repoFixture{repo: repo, now: &clock, ctx: context.Background()}
}

// 1. Insert + Get minimal round-trip with defaults.
func TestTaskRepoInsertAndGetMinimal(t *testing.T) {
	f := newRepoFixture(t)

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual",
		Title:  "Buy milk",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("Insert returned id = %d; want > 0", id)
	}

	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != id || got.Source != "manual" || got.Title != "Buy milk" {
		t.Errorf("round trip mismatch: id=%d source=%q title=%q",
			got.ID, got.Source, got.Title)
	}
	if got.Status != store.StatusPending {
		t.Errorf("default Status = %q; want %q", got.Status, store.StatusPending)
	}
	if got.Priority != 0 {
		t.Errorf("default Priority = %d; want 0", got.Priority)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero; Insert must populate it from the clock")
	}
	if !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("UpdatedAt %v != CreatedAt %v on fresh insert",
			got.UpdatedAt, got.CreatedAt)
	}
	// Optional fields default to the empty / zero value.
	if got.ExternalID != "" || got.LockKey != "" || got.WS != "" {
		t.Errorf("optional fields not zeroed: %+v", got)
	}
	if !got.StartedAt.IsZero() || !got.CompletedAt.IsZero() {
		t.Errorf("StartedAt/CompletedAt should remain zero before dispatch")
	}
}

// 3. Get on a missing id returns the typed sentinel so callers can
//    pattern-match instead of inspecting strings.
func TestTaskRepoGetMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)

	_, err := f.repo.Get(f.ctx, 99999)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(missing): err = %v; want ErrNotFound", err)
	}
}

// 2. Every column must round-trip. The point is to catch a future column
//    addition that someone wires into Insert but forgets in scanTask (or
//    vice-versa) — that asymmetry would silently drop data on read.
func TestTaskRepoInsertAndGetAllFields(t *testing.T) {
	f := newRepoFixture(t)

	want := store.Task{
		Source:         "gmail",
		ExternalID:     "thread-42",
		ExternalURL:    "https://mail.example/t/42",
		Title:          "Re: contract review",
		Body:           "please review the attached PDF by Friday.",
		Notes:          `{"channel":"#legal","sender":"alice@example"}`,
		Status:         store.StatusRunning,
		JudgmentReason: "directly addressed to me",
		Priority:       7,
		LockKey:        "repo:acme/web",
		CWD:            "/Users/me/works/acme",
		WS:             "workspace:101",
		ResultSummary:  "drafted reply, awaiting send",
		Reflection:     "next time, ask sender for the PDF inline",
		// Times must be UTC + millisecond precision to round-trip exactly
		// through the stored TEXT format.
		CreatedAt:   time.Date(2026, 5, 3, 10, 0, 0, 123_000_000, time.UTC),
		UpdatedAt:   time.Date(2026, 5, 3, 10, 5, 0, 456_000_000, time.UTC),
		StartedAt:   time.Date(2026, 5, 3, 10, 1, 0, 0, time.UTC),
		CompletedAt: time.Date(2026, 5, 3, 10, 4, 0, 0, time.UTC),
	}

	id, err := f.repo.Insert(f.ctx, want)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	want.ID = id

	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) ||
		!got.UpdatedAt.Equal(want.UpdatedAt) ||
		!got.StartedAt.Equal(want.StartedAt) ||
		!got.CompletedAt.Equal(want.CompletedAt) {
		t.Errorf("time fields drifted:\n got  CreatedAt=%v UpdatedAt=%v StartedAt=%v CompletedAt=%v\n want CreatedAt=%v UpdatedAt=%v StartedAt=%v CompletedAt=%v",
			got.CreatedAt, got.UpdatedAt, got.StartedAt, got.CompletedAt,
			want.CreatedAt, want.UpdatedAt, want.StartedAt, want.CompletedAt)
	}
	// Compare the rest by zeroing time so we can use a single equality check.
	gotCmp, wantCmp := got, want
	gotCmp.CreatedAt, gotCmp.UpdatedAt = time.Time{}, time.Time{}
	gotCmp.StartedAt, gotCmp.CompletedAt = time.Time{}, time.Time{}
	wantCmp.CreatedAt, wantCmp.UpdatedAt = time.Time{}, time.Time{}
	wantCmp.StartedAt, wantCmp.CompletedAt = time.Time{}, time.Time{}
	if gotCmp != wantCmp {
		t.Errorf("scalar fields drifted:\n got  %+v\n want %+v", gotCmp, wantCmp)
	}
}

// 4. Insert validates Source and Title at the repo boundary; the schema
//    itself enforces NOT NULL but a Go-side error gives PR-20's CLI a
//    clean message instead of a wrapped sqlite constraint string.
func TestTaskRepoInsertValidatesRequiredFields(t *testing.T) {
	f := newRepoFixture(t)

	if _, err := f.repo.Insert(f.ctx, store.Task{Title: "no source"}); err == nil {
		t.Errorf("Insert without Source must fail")
	}
	if _, err := f.repo.Insert(f.ctx, store.Task{Source: "manual"}); err == nil {
		t.Errorf("Insert without Title must fail")
	}
}

// 6. Idempotency (invariant #4): a Discovery plugin re-fetching the same
//    upstream id must hit the unique index and surface the typed sentinel
//    so the caller can short-circuit cleanly rather than re-create the row.
func TestTaskRepoInsertDuplicateExternalIDReturnsErr(t *testing.T) {
	f := newRepoFixture(t)

	if _, err := f.repo.Insert(f.ctx, store.Task{
		Source:     "gmail",
		ExternalID: "thread-1",
		Title:      "first",
	}); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	_, err := f.repo.Insert(f.ctx, store.Task{
		Source:     "gmail",
		ExternalID: "thread-1",
		Title:      "duplicate",
	})
	if !errors.Is(err, store.ErrDuplicateExternalID) {
		t.Fatalf("duplicate Insert: err = %v; want ErrDuplicateExternalID", err)
	}
}

// 7. Manually-added rows (no upstream id) must not be blocked by the
//    unique partial index. Tested at the schema level in store_test.go;
//    repeated here at the repo boundary so a future change that started
//    sending an empty string instead of NULL would be caught immediately.
func TestTaskRepoInsertAllowsRepeatedNullExternalID(t *testing.T) {
	f := newRepoFixture(t)

	for i := 0; i < 3; i++ {
		if _, err := f.repo.Insert(f.ctx, store.Task{
			Source: "manual",
			Title:  "manual add",
		}); err != nil {
			t.Fatalf("manual Insert #%d: %v", i, err)
		}
	}
}

// 5. Insert rejects an unknown Status before reaching SQLite so callers see
//    the typed sentinel rather than a generic CHECK violation.
func TestTaskRepoInsertRejectsInvalidStatus(t *testing.T) {
	f := newRepoFixture(t)

	_, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual",
		Title:  "bad status",
		Status: "completed", // typo for "done"
	})
	if !errors.Is(err, store.ErrInvalidStatus) {
		t.Fatalf("Insert(invalid status): err = %v; want ErrInvalidStatus", err)
	}
}
