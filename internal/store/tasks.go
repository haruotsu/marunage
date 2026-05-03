// Package store's tasks.go houses the repository layer that PR-11 promises:
// CRUD over the `tasks` table plus soft-lock acquire/release. It is the
// gateway every later PR (PR-20 add/list/show, PR-42 dispatch, PR-60
// render, etc.) goes through, so the contract is intentionally narrow:
//
//   - Required-only fields (Source, Title) plus opt-in defaults match the
//     `marunage add` UX described in docs/requirement.md.
//   - Nullable TEXT columns are surfaced as plain Go strings; the empty
//     string maps to NULL on the wire. This keeps callers from juggling
//     sql.NullString just to set or read a body / lock_key.
//   - Time columns use time.Time. Insert fills CreatedAt / UpdatedAt with
//     the injected clock when zero so tests can pin timestamps; the on-
//     disk format stays the millisecond-precision ISO8601 string the
//     package godoc and 0001_init.sql trigger agreed on.
//   - Errors are typed (ErrNotFound, ErrDuplicateExternalID, ErrLockHeld,
//     ErrInvalidStatus). PR-20 turns these into CLI exit codes; PR-42
//     turns ErrLockHeld into a "skip this task this round" branch. Neither
//     should have to parse error messages.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// Task mirrors a row in the `tasks` table. See package godoc for the
// empty-string-means-NULL and zero-time-means-NULL conventions.
type Task struct {
	ID             int64
	Source         string
	ExternalID     string
	ExternalURL    string
	Title          string
	Body           string
	Notes          string
	Status         string
	JudgmentReason string
	Priority       int
	LockKey        string
	CWD            string
	WS             string
	ResultSummary  string
	Reflection     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      time.Time
	CompletedAt    time.Time
}

// Status enum mirrors the CHECK constraint in 0001_init.sql. Centralising
// the names here keeps callers from typing "complete" instead of "done"
// and silently violating invariant #3 "Reversibility".
const (
	StatusPending      = "pending"
	StatusRunning      = "running"
	StatusDone         = "done"
	StatusFailed       = "failed"
	StatusSkipped      = "skipped"
	StatusWaitingHuman = "waiting_human"
)

var validStatuses = map[string]struct{}{
	StatusPending:      {},
	StatusRunning:      {},
	StatusDone:         {},
	StatusFailed:       {},
	StatusSkipped:      {},
	StatusWaitingHuman: {},
}

// Typed errors so PR-20 (CLI) and PR-42 (dispatch) can pattern-match
// without parsing strings.
var (
	ErrNotFound            = errors.New("store: task not found")
	ErrDuplicateExternalID = errors.New("store: duplicate (source, external_id)")
	ErrLockHeld            = errors.New("store: lock_key is held by another running task")
	ErrInvalidStatus       = errors.New("store: invalid status value")
)

// TaskRepo is the read/write gateway to the tasks table. It keeps a
// reference to *sql.DB but does not own its lifecycle; the caller (the
// process that called Open) closes it.
type TaskRepo struct {
	db  *sql.DB
	now func() time.Time
}

// Option mutates TaskRepo construction. The functional-option shape leaves
// room for future knobs (e.g. a logger) without breaking callers.
type Option func(*TaskRepo)

// WithClock injects a deterministic clock. Tests use this to pin the
// timestamps Insert / UpdateStatus / SetWorkspace stamp on rows so they
// can assert exact values without sleeping.
func WithClock(now func() time.Time) Option {
	return func(r *TaskRepo) { r.now = now }
}

// NewTaskRepo returns a TaskRepo bound to db. Defaults to time.Now for
// timestamps; pass WithClock in tests.
func NewTaskRepo(db *sql.DB, opts ...Option) *TaskRepo {
	r := &TaskRepo{db: db, now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// tsLayout is the ISO8601 UTC layout with millisecond precision the
// package godoc promises. Stored as TEXT so lexicographic ORDER BY matches
// chronological order for `marunage list`.
const tsLayout = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(tsLayout)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(tsLayout, s)
}

// nullable converts a Go empty string into a SQL NULL bind parameter.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueViolation matches modernc.org/sqlite's typed error against
// SQLITE_CONSTRAINT_UNIQUE (extended code 2067). Callers translate this
// into the user-visible idempotency signal ErrDuplicateExternalID.
func isUniqueViolation(err error) bool {
	const sqliteConstraintUnique = 2067
	var sErr *sqlite.Error
	if errors.As(err, &sErr) {
		return sErr.Code() == sqliteConstraintUnique
	}
	return false
}

// Insert writes a new tasks row and returns the assigned id. Required:
// Source, Title. Defaults filled in by the repo: Status = "pending",
// CreatedAt / UpdatedAt = injected clock when zero.
//
// Returns ErrDuplicateExternalID when (Source, ExternalID) collides with
// an existing row — Discovery plugins re-running on the same upstream id
// rely on this for idempotency (invariant #4).
func (r *TaskRepo) Insert(ctx context.Context, t Task) (int64, error) {
	if t.Source == "" {
		return 0, fmt.Errorf("store: Source is required")
	}
	if t.Title == "" {
		return 0, fmt.Errorf("store: Title is required")
	}
	if t.Status == "" {
		t.Status = StatusPending
	}
	if _, ok := validStatuses[t.Status]; !ok {
		return 0, ErrInvalidStatus
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = r.now().UTC()
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}

	const q = `INSERT INTO tasks
		(source, external_id, external_url, title, body, notes, status,
		 judgment_reason, priority, lock_key, cwd, ws,
		 result_summary, reflection,
		 created_at, updated_at, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	res, err := r.db.ExecContext(ctx, q,
		t.Source,
		nullable(t.ExternalID),
		nullable(t.ExternalURL),
		t.Title,
		nullable(t.Body),
		nullable(t.Notes),
		t.Status,
		nullable(t.JudgmentReason),
		t.Priority,
		nullable(t.LockKey),
		nullable(t.CWD),
		nullable(t.WS),
		nullable(t.ResultSummary),
		nullable(t.Reflection),
		formatTime(t.CreatedAt),
		formatTime(t.UpdatedAt),
		nullable(formatTime(t.StartedAt)),
		nullable(formatTime(t.CompletedAt)),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrDuplicateExternalID
		}
		return 0, fmt.Errorf("insert task: %w", err)
	}
	return res.LastInsertId()
}

// taskColumns is the canonical SELECT projection used by every read path
// so Get / List share scanTask and stay in sync with the column order.
const taskColumns = `id, source, external_id, external_url, title, body, notes,
	status, judgment_reason, priority, lock_key, cwd, ws,
	result_summary, reflection,
	created_at, updated_at, started_at, completed_at`

// Get fetches a task by id. Returns ErrNotFound when the row is missing.
func (r *TaskRepo) Get(ctx context.Context, id int64) (Task, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE id = ?", id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// UpdateStatus transitions a task to newStatus. newStatus is validated
// against the documented enum here so callers see the typed
// ErrInvalidStatus instead of a generic SQLite CHECK violation, and
// missing ids surface ErrNotFound rather than silently succeeding (which
// a naked UPDATE would do — RowsAffected=0 with no error).
//
// PR-11 keeps this method strictly status-only. The dispatcher
// (PR-42) sets started_at as part of its claim, and PR-43's atomic
// sentinel sets completed_at when it sees `.exit_code` materialise. That
// split means tests for those side effects live with the code that
// performs them, not here.
func (r *TaskRepo) UpdateStatus(ctx context.Context, id int64, newStatus string) error {
	if _, ok := validStatuses[newStatus]; !ok {
		return ErrInvalidStatus
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET status = ? WHERE id = ?", newStatus, id)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update status rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AcquireLock claims lockKey for the row with the given id, blocking
// when another running task already holds the same key (docs/
// requirement.md "lock_key でのソフトロック取得 / 解放"). It is "soft"
// because the conflict probe checks status='running': as soon as the
// holder transitions to done / failed / skipped the next AcquireLock for
// the same key succeeds without an explicit ReleaseLock.
//
// Atomicity: the row probe, conflict probe, and UPDATE all run inside a
// single transaction so a concurrent dispatcher cannot squeeze between
// the probe and the write. The store-wide SetMaxOpenConns(1) gives this
// transaction full writer serialisation per process; cross-process races
// fall back on SQLite's busy_timeout.
func (r *TaskRepo) AcquireLock(ctx context.Context, id int64, lockKey string) error {
	if lockKey == "" {
		return fmt.Errorf("store: lockKey is required")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("acquire lock begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var probe int64
	if err := tx.QueryRowContext(ctx,
		"SELECT id FROM tasks WHERE id = ?", id,
	).Scan(&probe); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("acquire lock probe row: %w", err)
	}

	var conflict int64
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM tasks WHERE lock_key = ? AND status = ? AND id != ? LIMIT 1",
		lockKey, StatusRunning, id,
	).Scan(&conflict)
	switch {
	case err == nil:
		return ErrLockHeld
	case errors.Is(err, sql.ErrNoRows):
		// no conflict, fall through to claim
	default:
		return fmt.Errorf("acquire lock probe key: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE tasks SET lock_key = ? WHERE id = ?", lockKey, id,
	); err != nil {
		return fmt.Errorf("acquire lock write: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("acquire lock commit: %w", err)
	}
	return nil
}

// ReleaseLock clears lock_key on a row. Soft locks are normally released
// implicitly by the status transition out of running (see AcquireLock);
// this method exists for the reaper / clean flows that need to drop a
// stale claim left behind by a crashed dispatcher.
func (r *TaskRepo) ReleaseLock(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET lock_key = NULL WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("release lock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("release lock rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWorkspace records the cmux ws reference for a dispatched task. It is
// the immediate "claim" PR-42 writes after `cmux new-workspace` returns so
// a parallel dispatch loop iteration cannot pick the same row twice.
// Empty ws clears the column (NULL on the wire) so reaper / clean flows
// can drop a stale reference.
func (r *TaskRepo) SetWorkspace(ctx context.Context, id int64, ws string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET ws = ? WHERE id = ?", nullable(ws), id)
	if err != nil {
		return fmt.Errorf("set workspace: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set workspace rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFilter narrows the rows List returns. Empty slices and zero Limit
// mean "no constraint", matching the way `marunage list` falls back to a
// full scan when no flags are passed.
type ListFilter struct {
	Statuses []string
	Sources  []string
	Limit    int
}

// List returns rows matching f, ordered the same way the dispatcher
// scans the queue: priority DESC, created_at ASC, id ASC. PR-42 calls
// this with Statuses=[pending] and Limit=N to pick the next batch; PR-60
// renders the unfiltered call. Sharing the order keeps "what `list`
// shows" and "what `dispatch` would pick" consistent.
//
// Unknown status / source values are not validated here — the empty
// result set they produce is the right answer, and the CHECK constraint
// in 0001_init.sql means a writer cannot persist them anyway.
func (r *TaskRepo) List(ctx context.Context, f ListFilter) ([]Task, error) {
	var (
		clauses []string
		args    []any
	)
	if len(f.Statuses) > 0 {
		clauses = append(clauses, "status IN ("+placeholders(len(f.Statuses))+")")
		for _, s := range f.Statuses {
			args = append(args, s)
		}
	}
	if len(f.Sources) > 0 {
		clauses = append(clauses, "source IN ("+placeholders(len(f.Sources))+")")
		for _, s := range f.Sources {
			args = append(args, s)
		}
	}
	q := "SELECT " + taskColumns + " FROM tasks"
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY priority DESC, created_at ASC, id ASC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("list scan: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list rows: %w", err)
	}
	return out, nil
}

// placeholders returns "?,?,?" with n entries for use in IN (...) clauses.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// rowScanner is the subset of *sql.Row / *sql.Rows scanTask needs.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanTask decodes one row of taskColumns into a Task, applying the same
// empty-string-means-NULL and zero-time-means-NULL conventions the writes
// honour.
func scanTask(row rowScanner) (Task, error) {
	var t Task
	var (
		externalID, externalURL sql.NullString
		body, notes             sql.NullString
		judgment, lockKey       sql.NullString
		cwd, ws                 sql.NullString
		result, reflection      sql.NullString
		createdAt, updatedAt    string
		startedAt, completedAt  sql.NullString
	)
	if err := row.Scan(
		&t.ID, &t.Source, &externalID, &externalURL,
		&t.Title, &body, &notes, &t.Status, &judgment,
		&t.Priority, &lockKey, &cwd, &ws,
		&result, &reflection,
		&createdAt, &updatedAt, &startedAt, &completedAt,
	); err != nil {
		return Task{}, err
	}
	t.ExternalID = externalID.String
	t.ExternalURL = externalURL.String
	t.Body = body.String
	t.Notes = notes.String
	t.JudgmentReason = judgment.String
	t.LockKey = lockKey.String
	t.CWD = cwd.String
	t.WS = ws.String
	t.ResultSummary = result.String
	t.Reflection = reflection.String

	var err error
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return Task{}, fmt.Errorf("parse created_at: %w", err)
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Task{}, fmt.Errorf("parse updated_at: %w", err)
	}
	if t.StartedAt, err = parseTime(startedAt.String); err != nil {
		return Task{}, fmt.Errorf("parse started_at: %w", err)
	}
	if t.CompletedAt, err = parseTime(completedAt.String); err != nil {
		return Task{}, fmt.Errorf("parse completed_at: %w", err)
	}
	return t, nil
}

