// Package store's kvstate.go houses the repository layer that PR-12
// promises: CRUD plus atomic CompareAndSwap over the `kv_state` table.
// Discovery plugins (PR-50 / PR-70 / PR-80) persist their per-source
// checkpoints — gmail_last_id, slack_last_ts, mtime, etc. — through this
// repo so a re-run after a crash does not double-process upstream events
// (docs/requirement.md "Discovery のチェックポイントは原子的に更新（古い
// タスクの取りこぼしを防ぐ）").
//
// Design contract:
//
//   - Set is implemented as a single SQL UPSERT (`INSERT ... ON CONFLICT
//     DO UPDATE`). One statement is atomic in SQLite, so a crash between
//     "key exists?" and "write" is impossible by construction.
//   - CompareAndSwap is a single conditional UPDATE; the WHERE clause
//     checks both the key and the expected current value, so two callers
//     racing to advance the same checkpoint cannot both succeed.
//   - Errors are typed (ErrKVNotFound, ErrKVKeyRequired, ErrKVValueRequired,
//     ErrKVStaleValue) so callers can distinguish "no checkpoint yet"
//     (first run), "drift" (concurrent advance), and "programmer error"
//     without parsing strings.
//
// The kv_state schema (migrations/0001_init.sql) is intentionally minimal:
// (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL).
// Missing checkpoint is row absence, never NULL or empty value, so the
// "no checkpoint yet" signal cannot collide with a legitimately empty
// stored value. The repo enforces the non-empty half at the boundary
// (Set / CompareAndSwap reject value=="" with ErrKVValueRequired) so the
// schema NOT NULL alone does not have to carry the invariant.
//
// Concurrency model: this repo runs against the *sql.DB returned by
// store.Open, which sets SetMaxOpenConns(1). All writers therefore go
// through a single SQLite connection, so the "follow-up SELECT" probe
// CompareAndSwap performs in its rare RowsAffected==0 branch sees a
// state consistent with the just-completed UPDATE — no other writer can
// have modified the row in between. CompareAndSwap's missing-vs-stale
// distinction relies on this; running this repo against a multi-writer
// connection pool would weaken that distinction without changing the
// atomicity of Set / CompareAndSwap themselves (each is one statement).

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Typed errors so Discovery callers (PR-50 / PR-70 / PR-80) can pattern-
// match on the failure mode.
//
// ErrKVNotFound is named distinctly from the tasks-side ErrNotFound so a
// caller using both repos in the same function (e.g. PR-42 dispatch
// reading a checkpoint and a task) can branch on the right one without
// shadowing.
var (
	ErrKVNotFound      = errors.New("store: kv_state key not found")
	ErrKVKeyRequired   = errors.New("store: kv_state key is required")
	ErrKVValueRequired = errors.New("store: kv_state value is required")
	ErrKVStaleValue    = errors.New("store: kv_state CompareAndSwap saw a stale value")
)

// KVStateRepo is the read/write gateway to the kv_state table. Mirrors
// TaskRepo's shape: holds a *sql.DB it does not own, plus an injectable
// clock used for the updated_at column on writes.
type KVStateRepo struct {
	db  *sql.DB
	now func() time.Time
}

// KVOption mutates KVStateRepo construction. Kept separate from tasks.go's
// Option type so the two repos can grow independent knobs (e.g. a logger
// scoped to one of them) without one constructor accidentally accepting
// options meant for the other.
type KVOption func(*KVStateRepo)

// WithKVClock injects a deterministic clock so tests can assert exact
// updated_at values without sleeping. Production callers leave this unset
// and get time.Now.
func WithKVClock(now func() time.Time) KVOption {
	return func(r *KVStateRepo) { r.now = now }
}

// NewKVStateRepo returns a KVStateRepo bound to db.
func NewKVStateRepo(db *sql.DB, opts ...KVOption) *KVStateRepo {
	r := &KVStateRepo{db: db, now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// KVEntry surfaces the row plus its updated_at so callers that care about
// the freshness of a checkpoint (the dispatcher staleness probe, the
// tests pinning the clock) do not have to issue a second query.
type KVEntry struct {
	Key       string
	Value     string
	UpdatedAt time.Time
}

// Get returns the value for key. Returns ErrKVNotFound when the row is
// absent — this is the documented "no checkpoint yet" signal Discovery
// plugins read on their first run.
func (r *KVStateRepo) Get(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", ErrKVKeyRequired
	}
	var value string
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM kv_state WHERE key = ?`, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrKVNotFound
	}
	if err != nil {
		return "", fmt.Errorf("kv_state get: %w", err)
	}
	return value, nil
}

// GetWithMeta returns the row plus its updated_at, so a caller comparing
// "is my checkpoint older than X?" does not have to issue a second query.
func (r *KVStateRepo) GetWithMeta(ctx context.Context, key string) (KVEntry, error) {
	if key == "" {
		return KVEntry{}, ErrKVKeyRequired
	}
	var (
		value     string
		updatedAt string
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT value, updated_at FROM kv_state WHERE key = ?`, key,
	).Scan(&value, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return KVEntry{}, ErrKVNotFound
	}
	if err != nil {
		return KVEntry{}, fmt.Errorf("kv_state get with meta: %w", err)
	}
	t, err := parseTime(updatedAt)
	if err != nil {
		return KVEntry{}, fmt.Errorf("kv_state parse updated_at: %w", err)
	}
	return KVEntry{Key: key, Value: value, UpdatedAt: t}, nil
}

// Set inserts or updates the row for key. updated_at is stamped from the
// injected clock. Implemented as a single UPSERT so the "key exists?" /
// "write" pair cannot tear: SQLite executes a single statement atomically,
// so a crash between the conflict check and the write is impossible.
//
// An empty value is rejected with ErrKVValueRequired: the package godoc
// promises "missing checkpoint is row absence, never NULL or empty value",
// so a stored "" would shadow the ErrKVNotFound signal Discovery plugins
// rely on for "first run, no checkpoint yet".
func (r *KVStateRepo) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return ErrKVKeyRequired
	}
	if value == "" {
		return ErrKVValueRequired
	}
	now := formatTime(r.now().UTC())
	const q = `INSERT INTO kv_state(key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value      = excluded.value,
			updated_at = excluded.updated_at`
	if _, err := r.db.ExecContext(ctx, q, key, value, now); err != nil {
		return fmt.Errorf("kv_state set: %w", err)
	}
	return nil
}

// Delete removes the row for key. Returns ErrKVNotFound on a missing key
// rather than silently no-opping, so a caller cleaning up a stale
// checkpoint sees the typo instead of believing it succeeded.
func (r *KVStateRepo) Delete(ctx context.Context, key string) error {
	if key == "" {
		return ErrKVKeyRequired
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM kv_state WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("kv_state delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("kv_state delete rows: %w", err)
	}
	if n == 0 {
		return ErrKVNotFound
	}
	return nil
}

// CompareAndSwap atomically updates value to newValue only when the row's
// current value still equals expected. Returns ErrKVStaleValue when the
// precondition fails (another writer raced ahead) and ErrKVNotFound when
// the key is missing entirely.
//
// This is the primitive Discovery uses to advance a checkpoint without a
// regression: two concurrent runs that both observed checkpoint=v1 must
// not both advance it to (potentially different) v2 / v3 values; whichever
// loses the race sees ErrKVStaleValue and re-reads.
//
// Implemented as a single conditional UPDATE so the precondition check
// and the write happen in one statement — same atomicity argument as Set.
// Distinguishing "missing" from "stale" requires a follow-up SELECT only
// in the rare RowsAffected==0 branch.
func (r *KVStateRepo) CompareAndSwap(ctx context.Context, key, expected, newValue string) error {
	if key == "" {
		return ErrKVKeyRequired
	}
	if newValue == "" {
		// Same invariant as Set: an empty stored value would later be
		// indistinguishable from "row deleted" on a follow-up Get.
		return ErrKVValueRequired
	}
	now := formatTime(r.now().UTC())
	const q = `UPDATE kv_state
		SET value = ?, updated_at = ?
		WHERE key = ? AND value = ?`
	res, err := r.db.ExecContext(ctx, q, newValue, now, key, expected)
	if err != nil {
		return fmt.Errorf("kv_state cas: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("kv_state cas rows: %w", err)
	}
	if n == 1 {
		return nil
	}

	// RowsAffected == 0 has two causes: the key does not exist, or the
	// expected value did not match. Distinguish so the caller can choose
	// re-Set vs. retry. The follow-up SELECT is safe against an interleaved
	// writer because store.Open pins SetMaxOpenConns(1), so another Set /
	// Delete cannot run between the UPDATE and this SELECT on the same
	// connection (see package godoc "Concurrency model").
	var probe string
	err = r.db.QueryRowContext(ctx,
		`SELECT value FROM kv_state WHERE key = ?`, key,
	).Scan(&probe)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrKVNotFound
	}
	if err != nil {
		return fmt.Errorf("kv_state cas probe: %w", err)
	}
	return ErrKVStaleValue
}
