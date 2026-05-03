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
//  23. AcquireLock on missing id -> ErrNotFound
//  24. AcquireLock when another *pending* row already holds the same
//      lock_key -> ErrLockHeld (catches the dispatch race a probe limited
//      to status='running' would miss)
//  25. AcquireLock(id, k) twice on the same row leaves lock_key=k
//      (idempotent self-acquire for crash-recovery flows)
//  26. SetWorkspace with empty string clears the ws column (reaper /
//      clean flow contract from the SetWorkspace doc comment)
//  27. List with a non-matching filter returns a zero-length result, not
//      garbage (PR-20 / PR-60 iterate against this)
//  28. List tie-breaks on id when priority and created_at match (the
//      dispatch query plan ends with `id` for this reason)
//  29. List rejects oversized Statuses / Sources filters as a DoS guard
//      against an unbounded IN (?, ?, ...) expansion

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

//  3. Get on a missing id returns the typed sentinel so callers can
//     pattern-match instead of inspecting strings.
func TestTaskRepoGetMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)

	_, err := f.repo.Get(f.ctx, 99999)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(missing): err = %v; want ErrNotFound", err)
	}
}

//  2. Every column must round-trip. The point is to catch a future column
//     addition that someone wires into Insert but forgets in scanTask (or
//     vice-versa) — that asymmetry would silently drop data on read.
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

//  4. Insert validates Source and Title at the repo boundary; the schema
//     itself enforces NOT NULL but a Go-side error gives PR-20's CLI a
//     clean message instead of a wrapped sqlite constraint string. The
//     typed sentinels let the CLI render a flag-name-aware diagnostic
//     without parsing the message.
func TestTaskRepoInsertValidatesRequiredFields(t *testing.T) {
	f := newRepoFixture(t)

	if _, err := f.repo.Insert(f.ctx, store.Task{Title: "no source"}); !errors.Is(err, store.ErrSourceRequired) {
		t.Errorf("Insert without Source: err = %v; want ErrSourceRequired", err)
	}
	if _, err := f.repo.Insert(f.ctx, store.Task{Source: "manual"}); !errors.Is(err, store.ErrTitleRequired) {
		t.Errorf("Insert without Title: err = %v; want ErrTitleRequired", err)
	}
}

//  6. Idempotency (invariant #4): a Discovery plugin re-fetching the same
//     upstream id must hit the unique index and surface the typed sentinel
//     so the caller can short-circuit cleanly rather than re-create the row.
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

//  7. Manually-added rows (no upstream id) must not be blocked by the
//     unique partial index. Tested at the schema level in store_test.go;
//     repeated here at the repo boundary so a future change that started
//     sending an empty string instead of NULL would be caught immediately.
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

//  5. Insert rejects an unknown Status before reaching SQLite so callers see
//     the typed sentinel rather than a generic CHECK violation.
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

// seedListFixture inserts a small dataset that exercises the dispatch
// ordering (priority DESC, created_at ASC), two sources, and several
// statuses. Returns the assigned IDs in seed order so the assertions can
// reference rows without depending on autoincrement details.
//
// Layout (clock advances by 1m between inserts):
//
//	index 0: gmail / pending / prio=5  (high priority, oldest)
//	index 1: gmail / pending / prio=1
//	index 2: slack / running / prio=5  (same prio as #0, but newer)
//	index 3: slack / done    / prio=0
//	index 4: gmail / pending / prio=5  (same prio as #0, newest)
func seedListFixture(t *testing.T, f repoFixture) []int64 {
	t.Helper()
	rows := []store.Task{
		{Source: "gmail", Title: "g0", Priority: 5},
		{Source: "gmail", Title: "g1", Priority: 1},
		{Source: "slack", Title: "s0", Priority: 5, Status: store.StatusRunning},
		{Source: "slack", Title: "s1", Priority: 0, Status: store.StatusDone},
		{Source: "gmail", Title: "g2", Priority: 5},
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		id, err := f.repo.Insert(f.ctx, row)
		if err != nil {
			t.Fatalf("seed Insert #%d: %v", i, err)
		}
		ids[i] = id
		*f.now = f.now.Add(time.Minute) // ensure created_at strictly increases
	}
	return ids
}

func titles(ts []store.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Title
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

//  8. List with no filter returns every row in dispatch order
//     (priority DESC, created_at ASC, id ASC). PR-42 and PR-60 both rely
//     on this ordering so `marunage list` shows the same row a dispatcher
//     would pick next. The seed fixture exercises three laws at once:
//     (a) priority DESC dominates so prio=5 rows come before prio=1/0;
//     (b) within the same priority, created_at ASC orders the row that
//     was inserted earlier first (g0 before s0 before g2);
//     (c) the id tie-break only fires when (priority, created_at) match,
//     which is covered separately by TestTaskRepoListTieBreaksById.
func TestTaskRepoListNoFilterUsesDispatchOrder(t *testing.T) {
	f := newRepoFixture(t)
	seedListFixture(t, f)

	got, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"g0", "s0", "g2", "g1", "s1"}
	if !equalStrings(titles(got), want) {
		t.Errorf("dispatch order:\n got  %v\n want %v", titles(got), want)
	}
}

//  9. List filters by status. Single and multi-status both go through
//     one IN (?) path so we exercise both shapes.
func TestTaskRepoListByStatus(t *testing.T) {
	f := newRepoFixture(t)
	seedListFixture(t, f)

	pending, err := f.repo.List(f.ctx, store.ListFilter{
		Statuses: []string{store.StatusPending},
	})
	if err != nil {
		t.Fatalf("List(pending): %v", err)
	}
	if got := titles(pending); !equalStrings(got, []string{"g0", "g2", "g1"}) {
		t.Errorf("pending only: got %v", got)
	}

	multi, err := f.repo.List(f.ctx, store.ListFilter{
		Statuses: []string{store.StatusRunning, store.StatusDone},
	})
	if err != nil {
		t.Fatalf("List(running+done): %v", err)
	}
	if got := titles(multi); !equalStrings(got, []string{"s0", "s1"}) {
		t.Errorf("running+done: got %v", got)
	}
}

//  10. List filters by source. Same shape as status; mostly here so a
//     future "filter by both source and status" addition does not regress
//     the AND wiring.
func TestTaskRepoListBySource(t *testing.T) {
	f := newRepoFixture(t)
	seedListFixture(t, f)

	gmail, err := f.repo.List(f.ctx, store.ListFilter{Sources: []string{"gmail"}})
	if err != nil {
		t.Fatalf("List(gmail): %v", err)
	}
	if got := titles(gmail); !equalStrings(got, []string{"g0", "g2", "g1"}) {
		t.Errorf("gmail only: got %v", got)
	}

	mixed, err := f.repo.List(f.ctx, store.ListFilter{
		Sources:  []string{"gmail", "slack"},
		Statuses: []string{store.StatusPending},
	})
	if err != nil {
		t.Fatalf("List(gmail+slack, pending): %v", err)
	}
	if got := titles(mixed); !equalStrings(got, []string{"g0", "g2", "g1"}) {
		t.Errorf("gmail+slack pending: got %v", got)
	}
}

//  11. List honours Limit. Combined with the dispatch order, this is what
//     PR-42 calls when picking the next N candidates.
func TestTaskRepoListHonoursLimit(t *testing.T) {
	f := newRepoFixture(t)
	seedListFixture(t, f)

	top2, err := f.repo.List(f.ctx, store.ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List(limit=2): %v", err)
	}
	if got := titles(top2); !equalStrings(got, []string{"g0", "s0"}) {
		t.Errorf("limit=2: got %v", got)
	}
}

//  12. UpdateStatus pending -> running succeeds and the AFTER UPDATE trigger
//     bumps updated_at. Pre-seeding an obviously-old timestamp removes any
//     dependence on the test wall-clock resolution: any "now" the trigger
//     stamps will be after 2020.
func TestTaskRepoUpdateStatusSucceeds(t *testing.T) {
	f := newRepoFixture(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source:    "manual",
		Title:     "transition",
		CreatedAt: old,
		UpdatedAt: old,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	after, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}
	if after.Status != store.StatusRunning {
		t.Errorf("status after UpdateStatus = %q; want %q", after.Status, store.StatusRunning)
	}
	if !after.UpdatedAt.After(old) {
		t.Errorf("updated_at did not advance past seeded old time: got %v", after.UpdatedAt)
	}
}

//  13. UpdateStatus rejects unknown values before talking to SQLite so the
//     row is not even attempted, and the error is the typed sentinel.
func TestTaskRepoUpdateStatusInvalidValueReturnsErr(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "bad"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := f.repo.UpdateStatus(f.ctx, id, "complete"); !errors.Is(err, store.ErrInvalidStatus) {
		t.Fatalf("UpdateStatus(invalid): err = %v; want ErrInvalidStatus", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusPending {
		t.Errorf("status after rejected UpdateStatus = %q; want unchanged %q",
			got.Status, store.StatusPending)
	}
}

//  14. UpdateStatus on a missing id returns ErrNotFound rather than silently
//     succeeding (which the bare UPDATE would do — RowsAffected=0 with no
//     error).
func TestTaskRepoUpdateStatusMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.UpdateStatus(f.ctx, 99999, store.StatusDone); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateStatus(missing): err = %v; want ErrNotFound", err)
	}
}

//  15. SetWorkspace stores the ws reference. PR-42 calls this immediately
//     after `cmux new-workspace` returns so the row is "claimed" before
//     the next dispatch loop iteration runs (the soft de-dup the spec
//     calls "ws参照を即座に DB に書き戻す").
func TestTaskRepoSetWorkspaceStoresReference(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "claim me"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := f.repo.SetWorkspace(f.ctx, id, "workspace:42"); err != nil {
		t.Fatalf("SetWorkspace: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WS != "workspace:42" {
		t.Errorf("ws = %q; want %q", got.WS, "workspace:42")
	}
}

//  16. SetWorkspace on a missing id returns ErrNotFound. Same reasoning as
//     UpdateStatus: silent no-op would mask a stale id in the dispatcher.
func TestTaskRepoSetWorkspaceMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.SetWorkspace(f.ctx, 99999, "workspace:1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetWorkspace(missing): err = %v; want ErrNotFound", err)
	}
}

//  17. AcquireLock on a free key claims it: the column is persisted and a
//     subsequent Get sees it. The Phase-1 dispatcher (PR-42) does this
//     immediately after picking a candidate so a concurrent loop iteration
//     cannot pick a colliding row.
func TestTaskRepoAcquireLockClaimsFreeKey(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "first"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := f.repo.AcquireLock(f.ctx, id, "git-repo:web"); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LockKey != "git-repo:web" {
		t.Errorf("lock_key = %q; want %q", got.LockKey, "git-repo:web")
	}
}

//  18. AcquireLock blocks when another *running* task already holds the
//     same key. The blocked attempt must NOT mutate lock_key, otherwise a
//     retry would silently overwrite the holder's claim.
func TestTaskRepoAcquireLockBlockedByRunningHolder(t *testing.T) {
	f := newRepoFixture(t)

	holderID, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "holder"})
	if err != nil {
		t.Fatalf("Insert holder: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, holderID, "k1"); err != nil {
		t.Fatalf("holder AcquireLock: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, holderID, store.StatusRunning); err != nil {
		t.Fatalf("holder UpdateStatus(running): %v", err)
	}

	laterID, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "later"})
	if err != nil {
		t.Fatalf("Insert later: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, laterID, "k1"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("later AcquireLock: err = %v; want ErrLockHeld", err)
	}
	got, err := f.repo.Get(f.ctx, laterID)
	if err != nil {
		t.Fatalf("Get later: %v", err)
	}
	if got.LockKey != "" {
		t.Errorf("blocked AcquireLock left lock_key = %q; want empty", got.LockKey)
	}
}

//  19. AcquireLock succeeds again once the previous holder transitions out
//     of running. Status-based release is the whole point of "soft" lock:
//     no explicit ReleaseLock call is required for the next claim to go
//     through.
func TestTaskRepoAcquireLockSucceedsAfterHolderDone(t *testing.T) {
	f := newRepoFixture(t)

	holderID, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "holder"})
	if err != nil {
		t.Fatalf("Insert holder: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, holderID, "k1"); err != nil {
		t.Fatalf("holder AcquireLock: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, holderID, store.StatusRunning); err != nil {
		t.Fatalf("holder running: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, holderID, store.StatusDone); err != nil {
		t.Fatalf("holder done: %v", err)
	}

	laterID, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "later"})
	if err != nil {
		t.Fatalf("Insert later: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, laterID, "k1"); err != nil {
		t.Fatalf("later AcquireLock after holder done: %v", err)
	}
}

//  20. ReleaseLock clears lock_key. Used by reaper / clean flows when a
//     task aborted without going through done/failed (e.g. crash, manual
//     intervention).
func TestTaskRepoReleaseLockClearsLockKey(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "t"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, id, "k1"); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := f.repo.ReleaseLock(f.ctx, id); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LockKey != "" {
		t.Errorf("lock_key after ReleaseLock = %q; want empty", got.LockKey)
	}
}

//  21. ReleaseLock on a missing id returns ErrNotFound for the same reason
//     UpdateStatus / SetWorkspace do.
func TestTaskRepoReleaseLockMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.ReleaseLock(f.ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ReleaseLock(missing): err = %v; want ErrNotFound", err)
	}
}

//  22. AcquireLock with an empty key is a programmer error — the schema
//     would gladly store NULL, defeating every subsequent probe — so the
//     repo rejects it loudly at the boundary with a typed sentinel.
func TestTaskRepoAcquireLockEmptyKeyValidates(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "t"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, id, ""); !errors.Is(err, store.ErrLockKeyRequired) {
		t.Fatalf("AcquireLock(empty key): err = %v; want ErrLockKeyRequired", err)
	}
}

//  23. AcquireLock on a missing id returns ErrNotFound (probe-then-update
//     would silently no-op without this check).
func TestTaskRepoAcquireLockMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.AcquireLock(f.ctx, 99999, "k1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AcquireLock(missing): err = %v; want ErrNotFound", err)
	}
}

//  24. AcquireLock blocks when another *pending* row already holds the same
//     lock_key, not just when there is a running holder. Without this, a
//     dispatcher pattern of "AcquireLock; UpdateStatus(running)" lets two
//     callers both observe "no running holder", both succeed, and the
//     second silently overwrites the first claim — exactly the
//     "two callers think they hold the same lock_key" race the
//     security review flagged.
func TestTaskRepoAcquireLockBlockedByPendingHolder(t *testing.T) {
	f := newRepoFixture(t)

	holderID, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "holder"})
	if err != nil {
		t.Fatalf("Insert holder: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, holderID, "k1"); err != nil {
		t.Fatalf("holder AcquireLock: %v", err)
	}
	// holder is still pending here — never transitioned to running.

	laterID, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "later"})
	if err != nil {
		t.Fatalf("Insert later: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, laterID, "k1"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("later AcquireLock(pending holder): err = %v; want ErrLockHeld", err)
	}
}

//  25. AcquireLock is idempotent for the same (id, lockKey): a retry after
//     a transient error must succeed without complaint and leave lock_key
//     untouched. The dispatcher relies on this for crash-recovery /
//     re-claim flows.
func TestTaskRepoAcquireLockIdempotentSelfAcquire(t *testing.T) {
	f := newRepoFixture(t)

	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "retry"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, id, "k1"); err != nil {
		t.Fatalf("AcquireLock #1: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, id, "k1"); err != nil {
		t.Fatalf("AcquireLock #2 (idempotent retry): %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LockKey != "k1" {
		t.Errorf("lock_key after self re-acquire = %q; want %q", got.LockKey, "k1")
	}
}

//  26. SetWorkspace with empty string clears the column. The doc comment
//     on SetWorkspace promises this for reaper / clean flows; without a
//     test the contract would silently regress.
func TestTaskRepoSetWorkspaceEmptyClearsColumn(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "ws"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.SetWorkspace(f.ctx, id, "workspace:1"); err != nil {
		t.Fatalf("SetWorkspace set: %v", err)
	}
	if err := f.repo.SetWorkspace(f.ctx, id, ""); err != nil {
		t.Fatalf("SetWorkspace clear: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WS != "" {
		t.Errorf("WS after empty SetWorkspace = %q; want empty", got.WS)
	}
}

//  27. List with a non-matching filter returns a zero-length slice. PR-20
//     `marunage list` iterates over this with `for _, t := range list`
//     so "no matches" must be observably empty, not garbage.
func TestTaskRepoListReturnsZeroLengthOnNoMatch(t *testing.T) {
	f := newRepoFixture(t)
	seedListFixture(t, f)
	got, err := f.repo.List(f.ctx, store.ListFilter{
		Statuses: []string{store.StatusFailed},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List(failed) length = %d; want 0", len(got))
	}
}

//  28. List tie-breaks on id when priority and created_at are identical.
//     Without the trailing `id ASC` in the ORDER BY (and the matching
//     trailing column in idx_tasks_dispatch), two rows inserted at the
//     same instant could swap order between calls, breaking dispatch
//     determinism. The clock is intentionally NOT advanced here so both
//     created_at values are identical.
func TestTaskRepoListTieBreaksById(t *testing.T) {
	f := newRepoFixture(t)
	a, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "a", Priority: 5})
	if err != nil {
		t.Fatalf("Insert a: %v", err)
	}
	b, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "b", Priority: 5})
	if err != nil {
		t.Fatalf("Insert b: %v", err)
	}
	got, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d rows; want 2", len(got))
	}
	if got[0].ID != a || got[1].ID != b {
		t.Errorf("tie-break by id: got [%d, %d]; want [%d, %d]",
			got[0].ID, got[1].ID, a, b)
	}
}

//  29. List rejects oversized Statuses / Sources filters. Without an
//     upper bound, an unbounded IN (?, ?, ...) expansion grows linearly
//     with the caller-controlled slice length and could exceed
//     SQLITE_MAX_VARIABLE_NUMBER (32766 by default) or simply waste
//     memory. CLI flags accepting `--status` repeatedly could trigger
//     either failure mode if not capped here.
func TestTaskRepoListRejectsOversizedFilter(t *testing.T) {
	f := newRepoFixture(t)
	huge := make([]string, 100)
	for i := range huge {
		huge[i] = "x"
	}
	if _, err := f.repo.List(f.ctx, store.ListFilter{Statuses: huge}); err == nil {
		t.Errorf("oversized Statuses filter must error")
	}
	if _, err := f.repo.List(f.ctx, store.ListFilter{Sources: huge}); err == nil {
		t.Errorf("oversized Sources filter must error")
	}
}

// Test list for PR-41 (権限モード) — store-side helpers consumed by PR-42:
//
//  30. EscalateToHuman: running -> waiting_human stamps the new status and
//      overwrites judgment_reason; updated_at advances.
//  31. EscalateToHuman: waiting_human -> waiting_human is idempotent (the
//      same prompt re-firing must not be an error) and refreshes
//      judgment_reason so the latest reason wins.
//  32. EscalateToHuman: pending / done / failed / skipped reject with
//      ErrInvalidTransition. The escalation path is reserved for cmux
//      sessions that are actually mid-flight (running) or already paused
//      for a human (waiting_human); end-state rows must not be reanimated
//      by a stale dispatcher.
//  33. EscalateToHuman: empty reason -> ErrReasonRequired. Escalation is
//      meaningless without a reason for the human to read in the Web UI /
//      Slack DM, so silently accepting "" would make audit logs useless.
//  34. EscalateToHuman: missing id -> ErrNotFound (atomic guard + probe
//      pattern, same as AcquireLock — distinguishes "row absent" from
//      "transition forbidden").

//  30. EscalateToHuman from running: status flips, judgment_reason is
//     overwritten with the supplied reason, and the AFTER UPDATE trigger
//     bumps updated_at past the seeded old timestamp.
func TestTaskRepoEscalateToHumanFromRunning(t *testing.T) {
	f := newRepoFixture(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source:         "manual",
		Title:          "needs human",
		Status:         store.StatusRunning,
		JudgmentReason: "phase1: markdown source bypass",
		CreatedAt:      old,
		UpdatedAt:      old,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	const reason = "auto-accept did not match: Bash(rm -rf /tmp/x)"
	if err := f.repo.EscalateToHuman(f.ctx, id, reason); err != nil {
		t.Fatalf("EscalateToHuman: %v", err)
	}

	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusWaitingHuman {
		t.Errorf("status = %q; want %q", got.Status, store.StatusWaitingHuman)
	}
	if got.JudgmentReason != reason {
		t.Errorf("judgment_reason = %q; want %q (overwrite)", got.JudgmentReason, reason)
	}
	if !got.UpdatedAt.After(old) {
		t.Errorf("updated_at did not advance past seeded old time: got %v", got.UpdatedAt)
	}
}

//  31. EscalateToHuman is idempotent on waiting_human and refreshes the
//     reason. Same prompt firing twice must not error.
func TestTaskRepoEscalateToHumanIdempotentOnWaitingHuman(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source:         "manual",
		Title:          "already paused",
		Status:         store.StatusWaitingHuman,
		JudgmentReason: "first reason",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := f.repo.EscalateToHuman(f.ctx, id, "second reason"); err != nil {
		t.Fatalf("EscalateToHuman idempotent re-call: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.JudgmentReason != "second reason" {
		t.Errorf("judgment_reason = %q; want refreshed %q", got.JudgmentReason, "second reason")
	}
	if got.Status != store.StatusWaitingHuman {
		t.Errorf("status = %q; want stay %q", got.Status, store.StatusWaitingHuman)
	}
}

//  32. EscalateToHuman rejects every status outside {running, waiting_human}
//     with ErrInvalidTransition, leaving the row untouched.
func TestTaskRepoEscalateToHumanRejectsInvalidSources(t *testing.T) {
	cases := []struct {
		name string
		from string
	}{
		{"pending", store.StatusPending},
		{"done", store.StatusDone},
		{"failed", store.StatusFailed},
		{"skipped", store.StatusSkipped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRepoFixture(t)
			id, err := f.repo.Insert(f.ctx, store.Task{
				Source:         "manual",
				Title:          "no escalation from " + tc.from,
				Status:         tc.from,
				JudgmentReason: "untouched",
			})
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}

			err = f.repo.EscalateToHuman(f.ctx, id, "should be rejected")
			if !errors.Is(err, store.ErrInvalidTransition) {
				t.Fatalf("EscalateToHuman from %q: err = %v; want ErrInvalidTransition", tc.from, err)
			}

			got, err := f.repo.Get(f.ctx, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != tc.from {
				t.Errorf("status changed despite rejected escalation: got %q; want %q", got.Status, tc.from)
			}
			if got.JudgmentReason != "untouched" {
				t.Errorf("judgment_reason changed despite rejected escalation: got %q", got.JudgmentReason)
			}
		})
	}
}

//  33. EscalateToHuman rejects an empty reason. The Web UI / Slack DM has
//     nothing to show otherwise.
func TestTaskRepoEscalateToHumanRejectsEmptyReason(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual",
		Title:  "needs reason",
		Status: store.StatusRunning,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := f.repo.EscalateToHuman(f.ctx, id, ""); !errors.Is(err, store.ErrReasonRequired) {
		t.Fatalf("EscalateToHuman empty reason: err = %v; want ErrReasonRequired", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusRunning {
		t.Errorf("status changed despite rejected escalate: got %q; want %q", got.Status, store.StatusRunning)
	}
}

//  34. EscalateToHuman on missing id returns ErrNotFound, distinct from
//     ErrInvalidTransition. Same atomic-update + probe pattern as
//     AcquireLock.
func TestTaskRepoEscalateToHumanMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	err := f.repo.EscalateToHuman(f.ctx, 99999, "phantom")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("EscalateToHuman(missing): err = %v; want ErrNotFound", err)
	}
}

// Test list for PR-41 ④ ExpireWaitingHuman:
//
//  35. Rows in waiting_human whose updated_at is strictly before deadline
//      flip to failed; returns the number of rows actually transitioned.
//  36. Rows in waiting_human whose updated_at >= deadline are left
//      untouched (boundary is exclusive — "older than" deadline only).
//  37. Rows in any other status are never touched, even if their
//      updated_at is older than deadline (the reaper has its own paths
//      for running / pending; this helper is the human-wait path only).
//  38. Empty result returns (0, nil) — not an error, so the loop / daemon
//      can call ExpireWaitingHuman every tick without log spam.
//  39. Zero deadline returns ErrDeadlineRequired without touching any row
//      — a missing or zero-value deadline would otherwise expire nothing
//      (epoch is the smallest representable time) AND silently mask a
//      caller bug. Fail loudly instead.
//  40. judgment_reason is preserved on expiry. The reason that caused
//      escalation must remain visible after the human window elapses, so
//      the post-mortem in `marunage review` can see why this row landed
//      on the human queue in the first place.

// 35-37. ExpireWaitingHuman flips only the right rows.
func TestTaskRepoExpireWaitingHumanFlipsOnlyExpired(t *testing.T) {
	f := newRepoFixture(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// expired waiting_human
	expired, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "expired", Status: store.StatusWaitingHuman,
		JudgmentReason: "auto-accept failed: Bash(rm -rf /)",
		CreatedAt:      old, UpdatedAt: old,
	})
	if err != nil {
		t.Fatalf("Insert expired: %v", err)
	}
	// fresh waiting_human (deadline boundary is exclusive)
	freshID, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "fresh", Status: store.StatusWaitingHuman,
		CreatedAt: fresh, UpdatedAt: fresh,
	})
	if err != nil {
		t.Fatalf("Insert fresh: %v", err)
	}
	// running (must stay; reaper handles running paths separately)
	runningID, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "running", Status: store.StatusRunning,
		CreatedAt: old, UpdatedAt: old,
	})
	if err != nil {
		t.Fatalf("Insert running: %v", err)
	}
	// pending (must stay)
	pendingID, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "pending", Status: store.StatusPending,
		CreatedAt: old, UpdatedAt: old,
	})
	if err != nil {
		t.Fatalf("Insert pending: %v", err)
	}

	n, err := f.repo.ExpireWaitingHuman(f.ctx, deadline)
	if err != nil {
		t.Fatalf("ExpireWaitingHuman: %v", err)
	}
	if n != 1 {
		t.Errorf("affected = %d; want 1", n)
	}

	got, err := f.repo.Get(f.ctx, expired)
	if err != nil {
		t.Fatalf("Get expired: %v", err)
	}
	if got.Status != store.StatusFailed {
		t.Errorf("expired status = %q; want %q", got.Status, store.StatusFailed)
	}
	// 40. judgment_reason preserved.
	if got.JudgmentReason != "auto-accept failed: Bash(rm -rf /)" {
		t.Errorf("judgment_reason after expiry = %q; want preserved", got.JudgmentReason)
	}

	for _, c := range []struct {
		id   int64
		want string
		name string
	}{
		{freshID, store.StatusWaitingHuman, "fresh waiting_human"},
		{runningID, store.StatusRunning, "running"},
		{pendingID, store.StatusPending, "pending"},
	} {
		got, err := f.repo.Get(f.ctx, c.id)
		if err != nil {
			t.Fatalf("Get %s: %v", c.name, err)
		}
		if got.Status != c.want {
			t.Errorf("%s status = %q; want %q (untouched by ExpireWaitingHuman)", c.name, got.Status, c.want)
		}
	}
}

// 38. ExpireWaitingHuman returns (0, nil) when nothing matches.
func TestTaskRepoExpireWaitingHumanNoMatchesIsNotAnError(t *testing.T) {
	f := newRepoFixture(t)
	deadline := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	n, err := f.repo.ExpireWaitingHuman(f.ctx, deadline)
	if err != nil {
		t.Fatalf("ExpireWaitingHuman empty: %v", err)
	}
	if n != 0 {
		t.Errorf("affected on empty table = %d; want 0", n)
	}
}

//  39. Zero deadline rejects with ErrDeadlineRequired. Without this guard
//     a caller that forgets to compute (now - human_wait_timeout) would
//     pass time.Time{} (epoch), which would silently expire nothing and
//     mask the bug forever.
func TestTaskRepoExpireWaitingHumanZeroDeadlineRejected(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual", Title: "must survive", Status: store.StatusWaitingHuman,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	n, err := f.repo.ExpireWaitingHuman(f.ctx, time.Time{})
	if !errors.Is(err, store.ErrDeadlineRequired) {
		t.Fatalf("ExpireWaitingHuman zero deadline: err = %v; want ErrDeadlineRequired", err)
	}
	if n != 0 {
		t.Errorf("affected on rejected call = %d; want 0", n)
	}

	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusWaitingHuman {
		t.Errorf("row touched despite rejected call: status = %q", got.Status)
	}
}
