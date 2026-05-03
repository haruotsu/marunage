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
	ErrSourceRequired      = errors.New("store: Source is required")
	ErrTitleRequired       = errors.New("store: Title is required")
	ErrLockKeyRequired     = errors.New("store: lockKey is required")
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

// WithClock injects a deterministic clock. Insert reads from it to fill
// CreatedAt / UpdatedAt when the caller leaves them zero, so tests can
// assert exact timestamps without sleeping.
//
// UpdateStatus / SetWorkspace do NOT consult this clock: their UPDATE
// fires the tasks_set_updated_at trigger in 0001_init.sql, which calls
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now') against SQLite's wall clock.
// Tests that need to observe updated_at moving compare against a far-
// past pre-seeded value (see TestTaskRepoUpdateStatusSucceeds) rather
// than against the injected clock.
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
		return 0, ErrSourceRequired
	}
	if t.Title == "" {
		return 0, ErrTitleRequired
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
//
// MAINTAINER NOTE: column changes touch THREE places that must all move
// together:
//  1. migrations/0001_init.sql (or a new migration adding the column)
//  2. this constant (which scanTask iterates positionally)
//  3. Insert's INSERT statement column list + the matching VALUES
//     placeholder count + ExecContext arg list
//
// TestTaskRepoInsertAndGetAllFields catches a mismatch by round-tripping
// every column; this comment is the diff-reviewer-facing reminder.
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
// Scope: PR-11 keeps this method strictly status-only. started_at and
// completed_at are filled in by the callers that own the matching life-
// cycle moment so PR-11 does not have to encode a (status -> timestamp)
// map that PR-42 / PR-43 / PR-21 each disagree about subtly:
//
//   - PR-42 dispatch sets started_at when claiming pending -> running
//   - PR-43 atomic sentinel sets completed_at on done / failed
//   - PR-21 CLI manual `done` / `fail` sets completed_at the same way
//     as PR-43, and the eventual `reopen` clears it
//
// Future PRs will add SetStartedAt / SetCompletedAt helpers on this repo
// when those callers land. Inferring the timestamp from newStatus would
// also defeat the legitimate "force-set status without touching the
// time line" use case the import / migration tooling needs.
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
// when another pending or running task already holds the same key
// (docs/requirement.md "lock_key でのソフトロック取得 / 解放" + "同じ
// `lock_key` のタスクは順次実行される"). It is "soft" because a holder
// in done / failed / skipped is ignored: the next AcquireLock for the
// same key succeeds without an explicit ReleaseLock.
//
// Why pending counts as a conflict: the typical dispatcher pattern is
// "AcquireLock; UpdateStatus(running)". If the probe only looked at
// status='running', two callers could both observe "no running holder",
// both succeed, and the second silently overwrite the first claim.
//
// Atomicity: implemented as a single UPDATE with a NOT EXISTS guard so
// the conflict check and the write happen in one statement. SQLite
// statements are atomic across processes too, so this does not depend
// on SetMaxOpenConns(1) the way a probe-then-update pair would.
//
// Self re-acquire is intentionally idempotent: calling AcquireLock(id,
// k) twice on the same row leaves lock_key=k. The dispatcher's crash-
// recovery path relies on this.
func (r *TaskRepo) AcquireLock(ctx context.Context, id int64, lockKey string) error {
	if lockKey == "" {
		return ErrLockKeyRequired
	}

	const q = `
		UPDATE tasks SET lock_key = ?
		WHERE id = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM tasks
		      WHERE lock_key = ?
		        AND status IN ('pending', 'running')
		        AND id != ?
		  )`
	res, err := r.db.ExecContext(ctx, q, lockKey, id, lockKey, id)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("acquire lock rows: %w", err)
	}
	if n == 1 {
		return nil
	}

	// RowsAffected == 0 has two causes: the row does not exist, or the
	// NOT EXISTS guard fired. Distinguish so the caller knows whether to
	// retry (lock contention) or give up (stale id).
	var probe int64
	err = r.db.QueryRowContext(ctx,
		"SELECT id FROM tasks WHERE id = ?", id,
	).Scan(&probe)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("acquire lock probe: %w", err)
	}
	return ErrLockHeld
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

// maxFilterValues caps Statuses / Sources slice length so a caller (or a
// CLI flag accepting repeated `--status`) cannot blow past
// SQLITE_MAX_VARIABLE_NUMBER (32766 by default) or balloon memory by
// expanding an unbounded IN (?, ?, ...) clause. The Status enum is six
// values and Sources realistically tops out in the dozens; 64 leaves
// headroom for both without enabling abuse.
const maxFilterValues = 64

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
	if len(f.Statuses) > maxFilterValues {
		return nil, fmt.Errorf("store: Statuses filter too large (%d > %d)",
			len(f.Statuses), maxFilterValues)
	}
	if len(f.Sources) > maxFilterValues {
		return nil, fmt.Errorf("store: Sources filter too large (%d > %d)",
			len(f.Sources), maxFilterValues)
	}

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

// ErrInvalidTransition is returned by TransitionStatus when (current, new)
// is not in the documented allow-list. The CLI translates this into a
// human-readable rejection ("cannot mark a done task as failed") rather
// than the row silently flipping into an inconsistent state.
//
// Kept separate from ErrInvalidStatus on purpose: the latter signals
// "this is not a valid status name at all", whereas this one signals
// "the name is fine but the move from where this row currently sits is
// not allowed by policy". CLI exit messages for the two diverge.
var ErrInvalidTransition = errors.New("store: invalid status transition")

// allowedTransitions is the (from -> set of to) policy matrix the CLI
// `done` / `fail` / `promote` / `reopen` subcommands enforce. Lifecycle
// moves owned by other PRs intentionally stay out of this map so a future
// feature cannot bypass them by routing through TransitionStatus:
//
//   - pending -> running is PR-42's dispatch responsibility.
//   - any -> waiting_human is PR-41's permission/escalation responsibility.
//   - any -> skipped is the discovery / triage path (PR-30 / PR-31).
//
// docs/pr_split_plan.md PR-21 is the authoritative source for this table;
// keep this comment and that section in sync.
var allowedTransitions = map[string]map[string]struct{}{
	StatusPending: {
		StatusDone:   {},
		StatusFailed: {},
	},
	StatusRunning: {
		StatusDone:   {},
		StatusFailed: {},
	},
	StatusWaitingHuman: {
		StatusDone:   {},
		StatusFailed: {},
	},
	StatusDone: {
		StatusPending: {},
	},
	StatusFailed: {
		StatusPending: {},
	},
	StatusSkipped: {
		StatusPending: {},
	},
}

// TransitionStatus is the policy-aware sibling of UpdateStatus. It loads
// the row's current status, checks (current, newStatus) against
// allowedTransitions, and only then performs the UPDATE. Callers that
// need to bypass policy (PR-42 dispatch, PR-41 escalation, migrations)
// continue to use UpdateStatus directly.
//
// Errors:
//   - ErrInvalidStatus  : newStatus is not a known status name
//   - ErrNotFound       : id does not exist
//   - ErrInvalidTransition : (current, newStatus) is not in the allow-list
func (r *TaskRepo) TransitionStatus(ctx context.Context, id int64, newStatus string) error {
	if _, ok := validStatuses[newStatus]; !ok {
		return ErrInvalidStatus
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	allowed, ok := allowedTransitions[current.Status]
	if !ok {
		return fmt.Errorf("%w: from %q to %q", ErrInvalidTransition, current.Status, newStatus)
	}
	if _, ok := allowed[newStatus]; !ok {
		return fmt.Errorf("%w: from %q to %q", ErrInvalidTransition, current.Status, newStatus)
	}
	return r.UpdateStatus(ctx, id, newStatus)
}

// Delete removes the row with the given id regardless of status. Callers
// (the `marunage rm` CLI, the reaper) get ErrNotFound when the id does
// not exist so a stale id in a script does not silently no-op.
//
// Soft-delete is intentionally not used: docs/requirement.md invariant
// "Reversibility" is satisfied at the source-of-truth level (the upstream
// markdown / Slack thread / etc.), not at the local SQLite mirror. A
// re-discovery run will re-insert the row if it is still relevant
// upstream.
func (r *TaskRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete task rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
