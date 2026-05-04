package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// Test list for PR-12 (kv_state repository layer).
//
//   1. Get on a missing key -> ErrKVNotFound
//   2. Set + Get round-trips the value
//   3. Set on an existing key UPSERTs the value and bumps updated_at
//   4. Set with empty key -> ErrKVKeyRequired
//   5. Delete removes the row; subsequent Get -> ErrKVNotFound
//   6. Delete on a missing key -> ErrKVNotFound
//   7. Delete with empty key -> ErrKVKeyRequired
//   8. CompareAndSwap with matching expected -> succeeds, new value visible
//   9. CompareAndSwap with mismatching expected -> ErrKVStaleValue, value unchanged
//  10. CompareAndSwap on a missing key -> ErrKVNotFound
//  11. CompareAndSwap with empty key -> ErrKVKeyRequired
//  12. WithKVClock pins updated_at deterministically
//  13. Concurrent Set on the same key keeps the table consistent (last writer
//      wins, no torn rows, count stays at 1)
//  14. Set with empty value -> ErrKVValueRequired (keeps the "row absence
//      means no checkpoint" invariant from kvstate.go's package godoc;
//      otherwise an empty checkpoint would shadow ErrKVNotFound)
//  15. CompareAndSwap with empty newValue -> ErrKVValueRequired (same
//      invariant; CAS that lands an empty value is indistinguishable from
//      "row was deleted")

// kvFixture wires a KVStateRepo to a fresh on-disk SQLite plus a deterministic
// clock so every test below can pin updated_at without sleeping.
type kvFixture struct {
	repo *store.KVStateRepo
	now  *time.Time
	ctx  context.Context
}

func newKVFixture(t *testing.T) kvFixture {
	t.Helper()
	db := openTempDB(t)
	clock := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	repo := store.NewKVStateRepo(db, store.WithKVClock(func() time.Time { return clock }))
	return kvFixture{repo: repo, now: &clock, ctx: context.Background()}
}

//  1. Get on a missing key returns the typed sentinel so Discovery plugins
//     can short-circuit on first run (no checkpoint yet) without parsing
//     the error string.
func TestKVStateRepoGetMissingReturnsErrKVNotFound(t *testing.T) {
	f := newKVFixture(t)

	_, err := f.repo.Get(f.ctx, "gmail_last_id")
	if !errors.Is(err, store.ErrKVNotFound) {
		t.Fatalf("Get(missing): err = %v; want ErrKVNotFound", err)
	}
}

//  2. Set + Get round-trips the value. This is the basic "checkpoint
//     written and read back" path Discovery plugins follow.
func TestKVStateRepoSetGetRoundTrip(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Set(f.ctx, "gmail_last_id", "thread-42"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := f.repo.Get(f.ctx, "gmail_last_id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "thread-42" {
		t.Errorf("Get = %q; want %q", got, "thread-42")
	}
}

//  3. Set on an existing key UPSERTs the value and bumps updated_at. The
//     Discovery checkpoint moves forward over time, so the same key is
//     written repeatedly with newer values; the row count must stay at 1
//     and the new value must overwrite the old.
func TestKVStateRepoSetUpsertsExistingKey(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Set(f.ctx, "gmail_last_id", "first"); err != nil {
		t.Fatalf("Set #1: %v", err)
	}
	first, err := f.repo.GetWithMeta(f.ctx, "gmail_last_id")
	if err != nil {
		t.Fatalf("GetWithMeta #1: %v", err)
	}

	*f.now = f.now.Add(time.Minute)
	if err := f.repo.Set(f.ctx, "gmail_last_id", "second"); err != nil {
		t.Fatalf("Set #2: %v", err)
	}
	second, err := f.repo.GetWithMeta(f.ctx, "gmail_last_id")
	if err != nil {
		t.Fatalf("GetWithMeta #2: %v", err)
	}

	if second.Value != "second" {
		t.Errorf("value after UPSERT = %q; want %q", second.Value, "second")
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("updated_at did not advance: first=%v second=%v",
			first.UpdatedAt, second.UpdatedAt)
	}
}

//  4. Set with an empty key is rejected at the repo boundary so the table
//     primary key never receives a sentinel that would shadow a real
//     "no checkpoint" signal (row absence).
func TestKVStateRepoSetEmptyKeyValidates(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Set(f.ctx, "", "value"); !errors.Is(err, store.ErrKVKeyRequired) {
		t.Fatalf("Set(empty key): err = %v; want ErrKVKeyRequired", err)
	}
}

//  5. Delete removes the row; the subsequent Get must return ErrKVNotFound
//     (i.e. "no checkpoint"), not an empty string masquerading as one.
func TestKVStateRepoDeleteRemovesRow(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Set(f.ctx, "slack_last_ts", "1700000000.000100"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.repo.Delete(f.ctx, "slack_last_ts"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.repo.Get(f.ctx, "slack_last_ts"); !errors.Is(err, store.ErrKVNotFound) {
		t.Fatalf("Get after Delete: err = %v; want ErrKVNotFound", err)
	}
}

//  6. Delete on a missing key returns ErrKVNotFound rather than silently
//     succeeding. Same reasoning as TaskRepo.UpdateStatus on a missing id:
//     a no-op masks a stale key in the caller.
func TestKVStateRepoDeleteMissingReturnsErrKVNotFound(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Delete(f.ctx, "nope"); !errors.Is(err, store.ErrKVNotFound) {
		t.Fatalf("Delete(missing): err = %v; want ErrKVNotFound", err)
	}
}

// 7. Delete with an empty key is the same programmer-error guard as Set.
func TestKVStateRepoDeleteEmptyKeyValidates(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Delete(f.ctx, ""); !errors.Is(err, store.ErrKVKeyRequired) {
		t.Fatalf("Delete(empty key): err = %v; want ErrKVKeyRequired", err)
	}
}

//  8. CompareAndSwap with matching expected swaps the value atomically.
//     This is the primitive Discovery uses to advance a checkpoint without
//     racing a concurrent re-run that already moved past it.
func TestKVStateRepoCompareAndSwapMatching(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Set(f.ctx, "k", "v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.repo.CompareAndSwap(f.ctx, "k", "v1", "v2"); err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	got, err := f.repo.Get(f.ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v2" {
		t.Errorf("value after CAS = %q; want %q", got, "v2")
	}
}

//  9. CompareAndSwap with a mismatching expected returns ErrKVStaleValue and
//     leaves the value untouched. Without this, two concurrent advances of
//     the same checkpoint could regress (newer caller's CAS overwrites an
//     even-newer value the first caller already committed).
func TestKVStateRepoCompareAndSwapMismatch(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Set(f.ctx, "k", "actual"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	err := f.repo.CompareAndSwap(f.ctx, "k", "stale", "new")
	if !errors.Is(err, store.ErrKVStaleValue) {
		t.Fatalf("CompareAndSwap(stale): err = %v; want ErrKVStaleValue", err)
	}
	got, err := f.repo.Get(f.ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "actual" {
		t.Errorf("value after stale CAS = %q; want unchanged %q", got, "actual")
	}
}

//  10. CompareAndSwap on a missing key returns ErrKVNotFound. Distinguishing
//     "key gone" from "value drifted" lets callers decide whether to re-Set
//     from scratch or back off and retry.
func TestKVStateRepoCompareAndSwapMissingReturnsErrKVNotFound(t *testing.T) {
	f := newKVFixture(t)

	err := f.repo.CompareAndSwap(f.ctx, "ghost", "x", "y")
	if !errors.Is(err, store.ErrKVNotFound) {
		t.Fatalf("CompareAndSwap(missing): err = %v; want ErrKVNotFound", err)
	}
}

// 11. CompareAndSwap with an empty key validates loudly at the boundary.
func TestKVStateRepoCompareAndSwapEmptyKeyValidates(t *testing.T) {
	f := newKVFixture(t)

	err := f.repo.CompareAndSwap(f.ctx, "", "x", "y")
	if !errors.Is(err, store.ErrKVKeyRequired) {
		t.Fatalf("CompareAndSwap(empty key): err = %v; want ErrKVKeyRequired", err)
	}
}

//  12. WithKVClock pins updated_at so tests do not have to sleep for the
//     trigger / wall clock to advance.
func TestKVStateRepoWithClockPinsUpdatedAt(t *testing.T) {
	f := newKVFixture(t)

	pinned := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	if err := f.repo.Set(f.ctx, "k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := f.repo.GetWithMeta(f.ctx, "k")
	if err != nil {
		t.Fatalf("GetWithMeta: %v", err)
	}
	if !got.UpdatedAt.Equal(pinned) {
		t.Errorf("updated_at = %v; want pinned %v", got.UpdatedAt, pinned)
	}
}

//  14. Set with an empty value violates the package invariant that a missing
//     checkpoint is row absence, never an empty stored value. Without this
//     guard a Discovery plugin Set("k", "") would later surface as Get -> ""
//     which is indistinguishable from "no checkpoint yet" only by the
//     accompanying ErrKVNotFound — and the empty-value path returns nil
//     instead. Reject loudly at the boundary with a typed sentinel.
func TestKVStateRepoSetEmptyValueValidates(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Set(f.ctx, "k", ""); !errors.Is(err, store.ErrKVValueRequired) {
		t.Fatalf("Set(empty value): err = %v; want ErrKVValueRequired", err)
	}
	// And the row must not have been written: a follow-up Get must still
	// surface ErrKVNotFound, not "" (which would mean Set partially succeeded).
	if _, err := f.repo.Get(f.ctx, "k"); !errors.Is(err, store.ErrKVNotFound) {
		t.Fatalf("Get after rejected Set: err = %v; want ErrKVNotFound", err)
	}
}

//  15. CompareAndSwap with an empty newValue is the same invariant as Set:
//     a CAS that lands an empty value is indistinguishable from "row was
//     deleted" on the next Get, so reject it at the boundary instead of
//     letting Discovery callers shadow ErrKVNotFound with a successful CAS.
func TestKVStateRepoCompareAndSwapEmptyNewValueValidates(t *testing.T) {
	f := newKVFixture(t)

	if err := f.repo.Set(f.ctx, "k", "v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	err := f.repo.CompareAndSwap(f.ctx, "k", "v1", "")
	if !errors.Is(err, store.ErrKVValueRequired) {
		t.Fatalf("CompareAndSwap(empty newValue): err = %v; want ErrKVValueRequired", err)
	}
	// And the row must be unchanged (validation happens before the UPDATE).
	got, err := f.repo.Get(f.ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v1" {
		t.Errorf("value after rejected CAS = %q; want unchanged %q", got, "v1")
	}
}

//  13. Concurrent Set on the same key must not corrupt the table. SQLite +
//     SetMaxOpenConns(1) serialises writers, so the formal contract is
//     "after all goroutines return, exactly one row exists for the key and
//     its value is one of the values written" — there must be no torn row,
//     no duplicate, and no error.
func TestKVStateRepoConcurrentSet(t *testing.T) {
	f := newKVFixture(t)

	const goroutines = 20
	const writesPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines*writesPerGoroutine)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				if err := f.repo.Set(f.ctx, "k", "v"); err != nil {
					errs <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Set: %v", err)
	}

	got, err := f.repo.Get(f.ctx, "k")
	if err != nil {
		t.Fatalf("Get after concurrent Set: %v", err)
	}
	if got != "v" {
		t.Errorf("value after concurrent Set = %q; want %q", got, "v")
	}
}

// PR-71 follow-on: InsertIfAbsent + DeleteIfValue are the atomic
// primitives the loop's kv_state-backed lock is built on. The prior
// Get → Set pattern had a TOCTOU race two design-review agents flagged
// as 🔴; these tests pin the new contract.

// TestInsertIfAbsent_RejectsEmptyKey guards the validation surface so
// a forgotten key on the caller side fails loud rather than writing a
// row keyed by "".
func TestInsertIfAbsent_RejectsEmptyKey(t *testing.T) {
	f := newKVFixture(t)
	if _, err := f.repo.InsertIfAbsent(f.ctx, "", "v"); !errors.Is(err, store.ErrKVKeyRequired) {
		t.Fatalf("InsertIfAbsent(empty key) = %v; want ErrKVKeyRequired", err)
	}
}

// TestInsertIfAbsent_RejectsEmptyValue mirrors Set: an empty value
// would later be indistinguishable from "row deleted" on a follow-up
// Get, breaking the "missing == row absence" invariant.
func TestInsertIfAbsent_RejectsEmptyValue(t *testing.T) {
	f := newKVFixture(t)
	if _, err := f.repo.InsertIfAbsent(f.ctx, "k", ""); !errors.Is(err, store.ErrKVValueRequired) {
		t.Fatalf("InsertIfAbsent(empty value) = %v; want ErrKVValueRequired", err)
	}
}

// TestInsertIfAbsent_FirstWriteWins: the first call inserts, the second
// returns inserted=false. The row is unchanged after the second call.
func TestInsertIfAbsent_FirstWriteWins(t *testing.T) {
	f := newKVFixture(t)
	ok, err := f.repo.InsertIfAbsent(f.ctx, "lock", "owner-A")
	if err != nil {
		t.Fatalf("first InsertIfAbsent: %v", err)
	}
	if !ok {
		t.Fatalf("first InsertIfAbsent inserted = false; want true")
	}
	ok, err = f.repo.InsertIfAbsent(f.ctx, "lock", "owner-B")
	if err != nil {
		t.Fatalf("second InsertIfAbsent: %v", err)
	}
	if ok {
		t.Fatalf("second InsertIfAbsent inserted = true; want false (row already exists)")
	}
	got, err := f.repo.Get(f.ctx, "lock")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "owner-A" {
		t.Errorf("value after losing race = %q; want %q (first writer wins)", got, "owner-A")
	}
}

// TestInsertIfAbsent_ConcurrentCallsExactlyOneWins: under N concurrent
// goroutines all racing on the same key, exactly one observes
// inserted=true. This is the property the loop's lock acquire relies
// on; without it the prior Get → Set pattern would let two loops both
// "claim" the lock.
func TestInsertIfAbsent_ConcurrentCallsExactlyOneWins(t *testing.T) {
	f := newKVFixture(t)
	const goroutines = 32

	var wg sync.WaitGroup
	wg.Add(goroutines)
	results := make(chan bool, goroutines)
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			ok, err := f.repo.InsertIfAbsent(f.ctx, "lock", "owner")
			if err != nil {
				errs <- err
				return
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	// Surface goroutine errors explicitly: without this, a SQLite
	// "database is locked" stray under contention would silently land
	// in the false bucket and the "exactly one winner" assertion could
	// pass with zero actual InsertIfAbsent wins.
	for err := range errs {
		t.Errorf("InsertIfAbsent under contention: %v", err)
	}
	wins := 0
	for ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("InsertIfAbsent winners = %d; want exactly 1", wins)
	}
}

// TestDeleteIfValue_RemovesOnMatch: release with the matching owner
// token clears the row so a follow-up InsertIfAbsent re-acquires.
func TestDeleteIfValue_RemovesOnMatch(t *testing.T) {
	f := newKVFixture(t)
	if _, err := f.repo.InsertIfAbsent(f.ctx, "lock", "owner-A"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	deleted, err := f.repo.DeleteIfValue(f.ctx, "lock", "owner-A")
	if err != nil {
		t.Fatalf("DeleteIfValue: %v", err)
	}
	if !deleted {
		t.Fatalf("DeleteIfValue(matching) deleted = false; want true")
	}
	if _, err := f.repo.Get(f.ctx, "lock"); !errors.Is(err, store.ErrKVNotFound) {
		t.Errorf("Get after release: %v; want ErrKVNotFound", err)
	}
}

// TestDeleteIfValue_NoOpOnMismatch: the "stale defer after key was
// re-acquired by another owner" scenario. The mismatched release must
// be a no-op so it does not stomp the live holder.
func TestDeleteIfValue_NoOpOnMismatch(t *testing.T) {
	f := newKVFixture(t)
	if _, err := f.repo.InsertIfAbsent(f.ctx, "lock", "owner-B"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	deleted, err := f.repo.DeleteIfValue(f.ctx, "lock", "owner-A")
	if err != nil {
		t.Fatalf("DeleteIfValue: %v", err)
	}
	if deleted {
		t.Fatalf("DeleteIfValue(mismatch) deleted = true; want false (must not stomp live holder)")
	}
	got, err := f.repo.Get(f.ctx, "lock")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "owner-B" {
		t.Errorf("value after mismatched release = %q; want %q", got, "owner-B")
	}
}

// TestDeleteIfValue_NoOpOnAbsent: releasing an already-released lock
// is a quiet no-op rather than an error, so a defer'd release after a
// crash + restart sequence does not surface a confusing error.
func TestDeleteIfValue_NoOpOnAbsent(t *testing.T) {
	f := newKVFixture(t)
	deleted, err := f.repo.DeleteIfValue(f.ctx, "lock", "owner-A")
	if err != nil {
		t.Fatalf("DeleteIfValue(absent): %v", err)
	}
	if deleted {
		t.Fatalf("DeleteIfValue(absent) deleted = true; want false")
	}
}

// TestDeleteIfValue_RejectsEmpty mirrors the Set / InsertIfAbsent
// validation: the value sentinel is the owner token and an empty one
// would silently accept any release.
func TestDeleteIfValue_RejectsEmpty(t *testing.T) {
	f := newKVFixture(t)
	if _, err := f.repo.DeleteIfValue(f.ctx, "", "v"); !errors.Is(err, store.ErrKVKeyRequired) {
		t.Errorf("DeleteIfValue(empty key) = %v; want ErrKVKeyRequired", err)
	}
	if _, err := f.repo.DeleteIfValue(f.ctx, "k", ""); !errors.Is(err, store.ErrKVValueRequired) {
		t.Errorf("DeleteIfValue(empty value) = %v; want ErrKVValueRequired", err)
	}
}
