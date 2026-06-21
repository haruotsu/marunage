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

// ValidStatuses is the canonical set of allowed task statuses. Handlers that
// need to validate user-supplied status values should reference this map rather
// than duplicating the list.
var ValidStatuses = map[string]struct{}{
	StatusPending:      {},
	StatusRunning:      {},
	StatusDone:         {},
	StatusFailed:       {},
	StatusSkipped:      {},
	StatusWaitingHuman: {},
}

var validStatuses = ValidStatuses

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
	// ErrInvalidTransition is returned when a status-changing helper is
	// called from a state it does not service. Distinct from
	// ErrInvalidStatus: that one means "the name is not a valid status
	// at all", whereas this one means "the name is fine but the move
	// from the row's current state is not allowed by policy". CLI exit
	// messages for the two diverge.
	ErrInvalidTransition = errors.New("store: status transition is not allowed from the current state")
	// ErrReasonRequired is returned when a reason-recording helper gets
	// an empty string; the Web UI / Slack DM has nothing to show then.
	ErrReasonRequired = errors.New("store: reason is required")
	// ErrDeadlineRequired guards bulk expiry helpers from a zero time.Time
	// silently expiring nothing.
	ErrDeadlineRequired = errors.New("store: deadline is required")
	// ErrWSRequired is returned by ClaimWorkspace when the caller passed
	// an empty ws — silently NULL-ing the column would defeat the
	// atomic-claim semantics callers (PR-42b dispatcher) depend on.
	ErrWSRequired = errors.New("store: ws is required")
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

// EscalateToHuman flips a row from running (or already waiting_human)
// into waiting_human and records why. Allowed source states are running
// and waiting_human only — idempotent re-call refreshes the reason, and
// any other state returns ErrInvalidTransition so a stale dispatcher
// cannot reanimate a terminal row.
//
// reason is stored verbatim for audit; downstream display layers
// (Slack DM / Web UI) own sanitisation of newlines / control chars.
//
// Atomicity follows AcquireLock: a single UPDATE with a status guard
// does the conflict check + the write, and a follow-up SELECT
// distinguishes ErrNotFound from ErrInvalidTransition when
// RowsAffected reports zero.
func (r *TaskRepo) EscalateToHuman(ctx context.Context, id int64, reason string) error {
	if reason == "" {
		return ErrReasonRequired
	}

	const q = `
		UPDATE tasks
		   SET status = ?, judgment_reason = ?
		 WHERE id = ?
		   AND status IN ('running', 'waiting_human')`
	res, err := r.db.ExecContext(ctx, q, StatusWaitingHuman, reason, id)
	if err != nil {
		return fmt.Errorf("escalate to human: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("escalate to human rows: %w", err)
	}
	if n == 1 {
		return nil
	}

	// RowsAffected == 0: either id is missing, or current status falls
	// outside {running, waiting_human}. Probe to give the caller the
	// precise sentinel — same disambiguation pattern AcquireLock uses.
	var current string
	err = r.db.QueryRowContext(ctx,
		"SELECT status FROM tasks WHERE id = ?", id,
	).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("escalate to human probe: %w", err)
	}
	return fmt.Errorf("%w: cannot escalate from %q", ErrInvalidTransition, current)
}

// ExpireWaitingHuman flips every waiting_human row whose updated_at is
// strictly before deadline into failed, and returns the affected count.
// The caller computes deadline as `now - human_wait_timeout` and calls
// this on each tick.
//
// Strict less-than is intentional: a row that landed in waiting_human at
// exactly `deadline` has not yet passed the timeout window. judgment_reason
// is preserved so the post-mortem in `marunage review` can still see why
// the row was escalated. ErrDeadlineRequired guards against a zero
// time.Time silently expiring nothing.
func (r *TaskRepo) ExpireWaitingHuman(ctx context.Context, deadline time.Time) (int64, error) {
	if deadline.IsZero() {
		return 0, ErrDeadlineRequired
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET status = ? WHERE status = ? AND updated_at < ?",
		StatusFailed, StatusWaitingHuman, formatTime(deadline.UTC()),
	)
	if err != nil {
		return 0, fmt.Errorf("expire waiting_human: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("expire waiting_human rows: %w", err)
	}
	return n, nil
}

// ErrStartedAtRequired guards SetStartedAt against a zero time.Time
// silently leaving started_at NULL. The reaper's 24h-stuck probe relies
// on started_at; an unstamped row would never trip the timeout.
var ErrStartedAtRequired = errors.New("store: started_at is required")

// ErrCompletedAtRequired guards SetCompletedAt / MarkDoneWithSummary
// against a zero time.Time silently leaving completed_at NULL. PR-43's
// completion watcher reads back completed_at to render durations; an
// unstamped row would surface as "still running" on every dashboard.
var ErrCompletedAtRequired = errors.New("store: completed_at is required")

// SetStartedAt stamps started_at on a row. PR-42 dispatch calls this when
// claiming pending -> running so the reaper (PR-44) can later detect a
// row that has been running past the 24h threshold. The package godoc on
// UpdateStatus deliberately defers this write to a caller-owned helper
// rather than trying to infer started_at from a status transition; this
// is that helper.
//
// Zero time.Time rejects with ErrStartedAtRequired: a fresh time.Time{}
// would silently become NULL on the wire (see formatTime), and the
// resulting "stamp succeeded" return would mask the missing dispatcher
// clock. ErrNotFound surfaces a stale id the same way SetWorkspace does.
func (r *TaskRepo) SetStartedAt(ctx context.Context, id int64, t time.Time) error {
	if t.IsZero() {
		return ErrStartedAtRequired
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET started_at = ? WHERE id = ?", formatTime(t.UTC()), id)
	if err != nil {
		return fmt.Errorf("set started_at: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set started_at rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCompletedAt stamps completed_at on a row. PR-43's completion
// watcher calls this on the failed branch (non-zero exit_code / parse
// failure) so the row carries an end-time even when MarkFailedWithReason
// was already issued. Mirrors SetStartedAt: zero time.Time fails loud
// with ErrCompletedAtRequired, and a missing id surfaces ErrNotFound
// rather than a silent no-op.
func (r *TaskRepo) SetCompletedAt(ctx context.Context, id int64, t time.Time) error {
	if t.IsZero() {
		return ErrCompletedAtRequired
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET completed_at = ? WHERE id = ?", formatTime(t.UTC()), id)
	if err != nil {
		return fmt.Errorf("set completed_at: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set completed_at rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkDoneWithSummary atomically flips a row to done and records the
// final summary plus completion timestamp. PR-43's completion watcher
// uses this on the happy path (sentinel exit_code == 0): a single
// UPDATE means a concurrent reader cannot observe a row in done with
// completed_at IS NULL, which would mis-classify it as "still running"
// in the dashboard.
//
// summary may be empty — Claude is permitted to finish without a final
// summary, and the watcher still wants the done transition to land.
// completedAt zero rejects with ErrCompletedAtRequired so a forgotten
// clock injection fails loud rather than writing NULL on the wire (see
// formatTime). Missing id surfaces ErrNotFound the same way SetStartedAt
// / SetWorkspace do.
//
// There is intentionally no source-state guard: the watcher's only call
// site is "row was running and the sentinel just appeared", so the
// matrix lives in TransitionStatus and this helper is the unguarded
// escape hatch (mirroring MarkFailedWithReason).
func (r *TaskRepo) MarkDoneWithSummary(ctx context.Context, id int64, summary string, completedAt time.Time) error {
	if completedAt.IsZero() {
		return ErrCompletedAtRequired
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET status = ?, result_summary = ?, completed_at = ? WHERE id = ?",
		StatusDone, nullable(summary), formatTime(completedAt.UTC()), id)
	if err != nil {
		return fmt.Errorf("mark done with summary: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark done with summary rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailedWithReason flips a row to failed and records reason into
// judgment_reason atomically. PR-42 dispatch uses this when SetWorkspace
// + UpdateStatus(running) have already committed but Send / WaitReady
// then fail: leaving the row in running with a ws reference would be a
// "phantom" the reaper has to wait for, so we mark it failed loudly here
// instead.
//
// reason is required (ErrReasonRequired on empty) so `marunage review`
// always has something to display in the post-mortem column. Missing id
// surfaces ErrNotFound. There is intentionally no source-state guard:
// the dispatcher's failure path can fire from running, but
// MarkFailedWithReason is also reusable from non-dispatch error sinks
// (e.g. a future "abort" CLI), so the matrix lives in TransitionStatus
// and this helper is the unguarded escape hatch.
func (r *TaskRepo) MarkFailedWithReason(ctx context.Context, id int64, reason string) error {
	if reason == "" {
		return ErrReasonRequired
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET status = ?, judgment_reason = ? WHERE id = ?",
		StatusFailed, reason, id)
	if err != nil {
		return fmt.Errorf("mark failed with reason: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark failed with reason rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetReflection writes reflection text on a terminal row (done /
// failed). The PR-102 reflection hook calls this from its async
// goroutine after Claude has written its review answer to the workspace
// dir; the status guard means a `marunage reopen` that flipped the row
// back to pending mid-flight cannot have the late answer pinned onto
// what is now a fresh dispatch.
//
// Empty text clears the column to NULL so a re-run that produced no
// answer does not leave a stale value behind. Missing id surfaces
// ErrNotFound; a non-terminal current state surfaces ErrInvalidTransition
// (mirroring EscalateToHuman's two-step probe so callers can branch on
// the typed sentinel).
func (r *TaskRepo) SetReflection(ctx context.Context, id int64, text string) error {
	const q = `
		UPDATE tasks
		   SET reflection = ?
		 WHERE id = ?
		   AND status IN ('done', 'failed')`
	res, err := r.db.ExecContext(ctx, q, nullable(text), id)
	if err != nil {
		return fmt.Errorf("set reflection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set reflection rows: %w", err)
	}
	if n == 1 {
		return nil
	}
	// Disambiguate "id missing" vs "wrong status" so callers can decide
	// whether a stale id is the cause or a reopen race.
	var current string
	err = r.db.QueryRowContext(ctx,
		"SELECT status FROM tasks WHERE id = ?", id,
	).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set reflection probe: %w", err)
	}
	return fmt.Errorf("%w: cannot set reflection from %q", ErrInvalidTransition, current)
}

// MarkSkippedWithReason atomically flips a row to skipped and writes
// the triage rationale into judgment_reason. PR-72's triage hook calls
// this when the embedded marunage-triage skill judges that the row is
// not addressed to us / is informational only — recording the reason
// in one UPDATE keeps `marunage review` from observing a row in
// skipped with an empty judgment_reason (which would defeat the audit
// trail the OODA Orient phase is supposed to leave behind).
//
// reason is required (ErrReasonRequired on empty) for the same reason
// MarkFailedWithReason / EscalateToHuman insist on it: the post-mortem
// surface has nothing to display otherwise. Missing id surfaces
// ErrNotFound rather than a silent no-op so a triage hook bug fails
// loud at the call site. There is intentionally no source-state guard:
// the realistic call site is "row is still pending and Discovery's
// triage just rejected it", but TransitionStatus enforces the broader
// matrix and this helper mirrors MarkFailedWithReason as the unguarded
// escape hatch.
func (r *TaskRepo) MarkSkippedWithReason(ctx context.Context, id int64, reason string) error {
	if reason == "" {
		return ErrReasonRequired
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET status = ?, judgment_reason = ? WHERE id = ?",
		StatusSkipped, reason, id)
	if err != nil {
		return fmt.Errorf("mark skipped with reason: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark skipped with reason rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// JudgmentReasonSeparator is the canonical join token between the prior
// judgment_reason and any newly appended note.
const JudgmentReasonSeparator = "; "

// AppendJudgmentReason concatenates suffix onto judgment_reason for the
// given row in a single atomic UPDATE.
func (r *TaskRepo) AppendJudgmentReason(ctx context.Context, id int64, suffix string) error {
	if suffix == "" {
		return ErrReasonRequired
	}
	const q = `
		UPDATE tasks
		   SET judgment_reason = CASE
		       WHEN judgment_reason IS NULL OR judgment_reason = '' THEN ?
		       ELSE judgment_reason || ? || ?
		   END
		 WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, suffix, JudgmentReasonSeparator, suffix, id)
	if err != nil {
		return fmt.Errorf("append judgment_reason: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("append judgment_reason rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailedFromRunningWithReason transitions a row to failed only if
// the current status is running. Used by the reaper so a row another
// writer (atomic sentinel, manual done) has moved past running is not
// overwritten by a stale snapshot.
func (r *TaskRepo) MarkFailedFromRunningWithReason(ctx context.Context, id int64, reason string) error {
	if reason == "" {
		return ErrReasonRequired
	}
	const q = `
		UPDATE tasks
		   SET status = ?,
		       judgment_reason = CASE
		           WHEN judgment_reason IS NULL OR judgment_reason = '' THEN ?
		           ELSE judgment_reason || ? || ?
		       END
		 WHERE id = ?
		   AND status = ?`
	res, err := r.db.ExecContext(ctx, q,
		StatusFailed,
		reason, JudgmentReasonSeparator, reason,
		id, StatusRunning)
	if err != nil {
		return fmt.Errorf("mark failed from running: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark failed from running rows: %w", err)
	}
	if n == 1 {
		return nil
	}
	var current string
	err = r.db.QueryRowContext(ctx, "SELECT status FROM tasks WHERE id = ?", id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("mark failed from running probe: %w", err)
	}
	return fmt.Errorf("%w: cannot mark failed from %q", ErrInvalidTransition, current)
}

// ClaimWorkspace atomically attaches ws to a row that is still pending
// AND has no prior ws reference. PR-42b's race-safety primitive.
func (r *TaskRepo) ClaimWorkspace(ctx context.Context, id int64, ws string) (bool, error) {
	if ws == "" {
		return false, ErrWSRequired
	}
	const q = `
		UPDATE tasks SET ws = ?
		WHERE id = ?
		  AND status = ?
		  AND (ws IS NULL OR ws = '')`
	res, err := r.db.ExecContext(ctx, q, ws, id, StatusPending)
	if err != nil {
		return false, fmt.Errorf("claim workspace: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim workspace rows: %w", err)
	}
	if n == 1 {
		return true, nil
	}
	var probe int64
	err = r.db.QueryRowContext(ctx,
		"SELECT id FROM tasks WHERE id = ?", id).Scan(&probe)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("claim workspace probe: %w", err)
	}
	return false, nil
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

// ListFilter narrows the rows List returns. Empty slices, zero Limit, and
// zero CreatedAfter mean "no constraint".
type ListFilter struct {
	Statuses []string
	Sources  []string
	Limit    int
	// CreatedAfter, when non-zero, restricts results to rows whose created_at
	// is strictly after this time. Used by `marunage review --since Xd`.
	CreatedAfter time.Time
	// DispatchableOnly restricts results to rows the management layer
	// (internal/manage, PR-R03+) considers dispatchable. redesign §5.2 wants
	// the dispatch query narrowed to plan_label='ready', but doing that
	// outright would strand every legacy row — nothing populates plan_label
	// until PR-R05 — and break動作不変. So the strangler-fig form keeps rows
	// the manager has NOT yet evaluated (plan_label IS NULL) dispatchable
	// alongside the ones it marked ready, while excluding hold / defer /
	// needs-human / drop. Once the manager fills plan_label for every row
	// (PR-R05+), the NULL branch naturally stops matching and the filter
	// converges on the strict ready-only query. Off (zero value) leaves List
	// unchanged for all other callers.
	DispatchableOnly bool
}

// dispatchablePlanLabel is the verdict the management layer assigns to rows
// that are cleared for immediate dispatch (redesign §3.1). config's
// [manage.verdicts] mapping carries the same intent (the "ready" entry with
// Dispatchable=true), but PR-R04 keeps the store filter on this literal so the
// strangler-fig query stays self-contained while the management layer is still
// inert. PR-R05, when it teaches the manager to write plan_label and to honour
// the config-driven Dispatchable flags, is expected to source the dispatchable
// set from config and retire this constant — until then the two must agree
// that "ready" is the sole dispatchable verdict.
const dispatchablePlanLabel = "ready"

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
	if !f.CreatedAfter.IsZero() {
		clauses = append(clauses, "created_at > ?")
		args = append(args, formatTime(f.CreatedAfter))
	}
	if f.DispatchableOnly {
		clauses = append(clauses, "(plan_label = ? OR plan_label IS NULL)")
		args = append(args, dispatchablePlanLabel)
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
