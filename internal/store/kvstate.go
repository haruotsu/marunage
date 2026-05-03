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
//   - Errors are typed (ErrKVNotFound, ErrKVKeyRequired) so callers can
//     distinguish "no checkpoint yet" (first run) from programmer error
//     without parsing strings.
//
// The kv_state schema (migrations/0001_init.sql) is intentionally minimal:
// (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL).
// Missing checkpoint is row absence, never NULL or empty value, so the
// "no checkpoint yet" signal cannot collide with a legitimately empty
// stored value.

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
	ErrKVNotFound    = errors.New("store: kv_state key not found")
	ErrKVKeyRequired = errors.New("store: kv_state key is required")
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
func (r *KVStateRepo) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return ErrKVKeyRequired
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
