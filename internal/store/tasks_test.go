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

// seedListFixture inserts a small dataset that exercises the dispatch
// ordering (priority DESC, created_at ASC), two sources, and several
// statuses. Returns the assigned IDs in seed order so the assertions can
// reference rows without depending on autoincrement details.
//
// Layout (clock advances by 1m between inserts):
//   index 0: gmail / pending / prio=5  (high priority, oldest)
//   index 1: gmail / pending / prio=1
//   index 2: slack / running / prio=5  (same prio as #0, but newer)
//   index 3: slack / done    / prio=0
//   index 4: gmail / pending / prio=5  (same prio as #0, newest)
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

// 8. List with no filter returns every row in dispatch order
//    (priority DESC, created_at ASC). PR-42 and PR-60 both rely on this
//    ordering so a `marunage list` call shows the same row a dispatcher
//    would pick next.
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

// 9. List filters by status. Single and multi-status both go through
//    one IN (?) path so we exercise both shapes.
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

// 10. List filters by source. Same shape as status; mostly here so a
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

// 11. List honours Limit. Combined with the dispatch order, this is what
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

// 12. UpdateStatus pending -> running succeeds and the AFTER UPDATE trigger
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

// 13. UpdateStatus rejects unknown values before talking to SQLite so the
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

// 14. UpdateStatus on a missing id returns ErrNotFound rather than silently
//     succeeding (which the bare UPDATE would do — RowsAffected=0 with no
//     error).
func TestTaskRepoUpdateStatusMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.UpdateStatus(f.ctx, 99999, store.StatusDone); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateStatus(missing): err = %v; want ErrNotFound", err)
	}
}

// 15. SetWorkspace stores the ws reference. PR-42 calls this immediately
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

// 16. SetWorkspace on a missing id returns ErrNotFound. Same reasoning as
//     UpdateStatus: silent no-op would mask a stale id in the dispatcher.
func TestTaskRepoSetWorkspaceMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.SetWorkspace(f.ctx, 99999, "workspace:1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetWorkspace(missing): err = %v; want ErrNotFound", err)
	}
}

// 17. AcquireLock on a free key claims it: the column is persisted and a
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

// 18. AcquireLock blocks when another *running* task already holds the
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

// 19. AcquireLock succeeds again once the previous holder transitions out
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

// 20. ReleaseLock clears lock_key. Used by reaper / clean flows when a
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

// 21. ReleaseLock on a missing id returns ErrNotFound for the same reason
//     UpdateStatus / SetWorkspace do.
func TestTaskRepoReleaseLockMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.ReleaseLock(f.ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ReleaseLock(missing): err = %v; want ErrNotFound", err)
	}
}

// 22. AcquireLock with an empty key is a programmer error — the schema
//     would gladly store NULL, defeating every subsequent probe — so the
//     repo rejects it loudly at the boundary.
func TestTaskRepoAcquireLockEmptyKeyValidates(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "t"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.AcquireLock(f.ctx, id, ""); err == nil {
		t.Fatalf("AcquireLock with empty lockKey must fail")
	}
}

// 23. AcquireLock on a missing id returns ErrNotFound (probe-then-update
//     would silently no-op without this check).
func TestTaskRepoAcquireLockMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.AcquireLock(f.ctx, 99999, "k1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AcquireLock(missing): err = %v; want ErrNotFound", err)
	}
}

// 24. AcquireLock blocks when another *pending* row already holds the same
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

// 25. AcquireLock is idempotent for the same (id, lockKey): a retry after
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
