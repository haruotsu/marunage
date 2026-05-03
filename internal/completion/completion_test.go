package completion_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/completion"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/store"
)

// PR-43 atomic sentinel completion watcher test list (t_wada TDD).
//
//   W1.  Empty running set: Tick is a no-op, no audit, no store writes.
//   W2.  Running row without sentinel: Tick is a no-op (file system probe
//        reports ENOENT, which is the steady state for an in-flight task).
//   W3.  Running row with sentinel "0\n": row marked done, audit
//        completion.detect with task id.
//   W4.  Running row with sentinel "1\n" (non-zero exit): row marked
//        failed, judgment_reason mentions exit_code=1, audit
//        completion.fail with task id.
//   W5.  Running row with garbage in .exit_code: row marked failed,
//        judgment_reason mentions parse failure, audit completion.fail.
//   W6.  .exit_code.tmp present but .exit_code absent: Tick no-ops
//        (atomicity contract — reader does not consume partial sentinel).
//   W7.  .result_summary present alongside .exit_code: trimmed contents
//        land in tasks.result_summary.
//   W8.  .result_summary absent on success: result_summary stays empty.
//   W9.  Multiple running rows are checked independently.
//   W10. Workspace dir missing entirely: Tick no-ops (PR-44 reaper's
//        domain — "ws参照があるが cmux 側に該当ワークスペースがない").
//   W11. Audit completion.detect Key=task:<id>, Value=exit code.
//   W12. Audit completion.fail Key=task:<id>, Value contains reason.
//   W13. Atomic write — write tmp, then rename — leaves the watcher
//        seeing only the final sentinel content (no half-written reads).
//   N1.  New fails ErrInvalidConfig when WithStore omitted.
//   N2.  New fails ErrInvalidConfig when WithWorkspaceDirs omitted.
//   N3.  New defaults clock to time.Now and auditor to NopAuditor.
//   R1.  Run invokes Tick repeatedly until ctx is cancelled.
//   R2.  ctx cancellation returns nil (clean shutdown for the daemon).
//   R3.  Run runs Tick immediately on entry (no first-poll wait).

// fakeAuditor mirrors the dispatcher's test double — see
// internal/dispatch/dispatch_test.go's fakeAuditor for the reference
// shape. Captured in a slice so order-sensitive assertions stay
// straightforward.
type fakeAuditor struct {
	mu     sync.Mutex
	events []config.AuditEvent
}

func (a *fakeAuditor) Record(e config.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}
func (a *fakeAuditor) Events() []config.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]config.AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

// dirsFunc adapts a closure into completion.WorkspaceDirs so tests can
// point each task id at its own t.TempDir() subdirectory.
type dirsFunc func(id int64) string

func (f dirsFunc) Dir(id int64) string { return f(id) }

// fixture wires a real on-disk SQLite store + an auditor + a watcher
// rooted at a per-test temp directory so sentinel files for task <id>
// live under <tempdir>/<id>/.exit_code.
type fixture struct {
	t       *testing.T
	repo    *store.TaskRepo
	dirs    dirsFunc
	au      *fakeAuditor
	watcher *completion.Watcher
	now     time.Time
	ctx     context.Context
	root    string
}

func newFixture(t *testing.T, opts ...completion.Option) fixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	root := t.TempDir()
	now := time.Date(2026, 5, 3, 13, 30, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return now }))
	au := &fakeAuditor{}
	dirs := dirsFunc(func(id int64) string {
		return filepath.Join(root, fmt.Sprintf("%d", id))
	})

	defOpts := []completion.Option{
		completion.WithStore(repo),
		completion.WithWorkspaceDirs(dirs),
		completion.WithClock(func() time.Time { return now }),
		completion.WithAuditor(au),
	}
	w, err := completion.New(append(defOpts, opts...)...)
	if err != nil {
		t.Fatalf("completion.New: %v", err)
	}
	return fixture{
		t:       t,
		repo:    repo,
		dirs:    dirs,
		au:      au,
		watcher: w,
		now:     now,
		ctx:     context.Background(),
		root:    root,
	}
}

// insertRunning inserts a row directly into status='running' so the
// watcher has something to scan without dragging the dispatcher in.
func (f *fixture) insertRunning(title string) int64 {
	f.t.Helper()
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "manual",
		Title:  title,
		Status: store.StatusRunning,
	})
	if err != nil {
		f.t.Fatalf("Insert(%s): %v", title, err)
	}
	return id
}

// writeSentinel writes <root>/<id>/.exit_code with the given contents,
// creating the parent dir on the fly. Mirrors "echo $? > .exit_code"
// shape (no atomic rename) — for the rename path use writeSentinelAtomic.
func (f *fixture) writeSentinel(id int64, contents string) {
	f.t.Helper()
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".exit_code"), []byte(contents), 0o600); err != nil {
		f.t.Fatalf("WriteFile: %v", err)
	}
}

// writeResultSummary writes <root>/<id>/.result_summary so the watcher
// picks it up alongside .exit_code on the happy path.
func (f *fixture) writeResultSummary(id int64, contents string) {
	f.t.Helper()
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".result_summary"), []byte(contents), 0o600); err != nil {
		f.t.Fatalf("WriteFile: %v", err)
	}
}

// W1: empty running set is a no-op.
func TestTickEmptyRunningSetIsNoop(t *testing.T) {
	f := newFixture(t)
	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := len(f.au.Events()); got != 0 {
		t.Errorf("audit events = %d; want 0 on empty queue", got)
	}
}

// W2: running row without sentinel stays running.
func TestTickWithoutSentinelLeavesRowRunning(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("in flight")
	// Pre-create the workspace dir so we exercise the "dir exists, file
	// missing" branch (distinct from W10's "dir missing entirely").
	if err := os.MkdirAll(f.dirs.Dir(id), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want %q", row.Status, store.StatusRunning)
	}
	if got := len(f.au.Events()); got != 0 {
		t.Errorf("audit events = %d; want 0 (no sentinel detected)", got)
	}
}

// W3: sentinel "0" → done.
func TestTickDetectsSuccessfulCompletion(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("happy path")
	f.writeSentinel(id, "0\n")

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusDone {
		t.Errorf("status = %q; want %q", row.Status, store.StatusDone)
	}
	if !row.CompletedAt.Equal(f.now) {
		t.Errorf("completed_at = %v; want %v", row.CompletedAt, f.now)
	}

	events := f.au.Events()
	var detect *config.AuditEvent
	for i := range events {
		if events[i].Action == "completion.detect" {
			detect = &events[i]
			break
		}
	}
	if detect == nil {
		t.Fatalf("completion.detect audit not recorded; got %+v", events)
	}
	wantKey := fmt.Sprintf("task:%d", id)
	if detect.Key != wantKey {
		t.Errorf("audit Key = %q; want %q", detect.Key, wantKey)
	}
	if detect.Value != "0" {
		t.Errorf("audit Value = %q; want %q", detect.Value, "0")
	}
}

// W4: non-zero exit code → failed with judgment_reason.
func TestTickDetectsNonZeroExitAsFailed(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("script crashed")
	f.writeSentinel(id, "1\n")

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status = %q; want %q", row.Status, store.StatusFailed)
	}
	if !strings.Contains(row.JudgmentReason, "exit_code=1") {
		t.Errorf("judgment_reason = %q; want it to mention exit_code=1", row.JudgmentReason)
	}
	if !row.CompletedAt.Equal(f.now) {
		t.Errorf("completed_at = %v; want %v on fail branch", row.CompletedAt, f.now)
	}

	events := f.au.Events()
	var fail *config.AuditEvent
	for i := range events {
		if events[i].Action == "completion.fail" {
			fail = &events[i]
			break
		}
	}
	if fail == nil {
		t.Fatalf("completion.fail audit not recorded; got %+v", events)
	}
	wantKey := fmt.Sprintf("task:%d", id)
	if fail.Key != wantKey {
		t.Errorf("audit Key = %q; want %q", fail.Key, wantKey)
	}
	if !strings.Contains(fail.Value, "exit_code=1") {
		t.Errorf("audit Value = %q; want it to mention exit_code=1", fail.Value)
	}
}

// W5: garbage sentinel → failed with parse failure reason.
func TestTickHandlesUnparseableSentinel(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("garbage")
	f.writeSentinel(id, "not-a-number\n")

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status = %q; want %q", row.Status, store.StatusFailed)
	}
	if !strings.Contains(row.JudgmentReason, "parse") {
		t.Errorf("judgment_reason = %q; want it to mention the parse failure", row.JudgmentReason)
	}

	events := f.au.Events()
	var fail *config.AuditEvent
	for i := range events {
		if events[i].Action == "completion.fail" {
			fail = &events[i]
			break
		}
	}
	if fail == nil {
		t.Fatalf("completion.fail audit not recorded; got %+v", events)
	}
}

// W6: .exit_code.tmp on its own is invisible — atomicity contract.
func TestTickIgnoresTmpSentinel(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("partial write")
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Only the .tmp half of the documented atomic write exists.
	if err := os.WriteFile(filepath.Join(dir, ".exit_code.tmp"), []byte("0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want still %q (partial write must not flip the row)", row.Status, store.StatusRunning)
	}
	if got := len(f.au.Events()); got != 0 {
		t.Errorf("audit events = %d; want 0 (no consumption of partial sentinel)", got)
	}
}

// W7: result_summary picked up alongside .exit_code on success.
func TestTickStoresResultSummaryWhenPresent(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("with summary")
	f.writeResultSummary(id, "  Built 3 PRs, all green\n")
	f.writeSentinel(id, "0\n")

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	const want = "Built 3 PRs, all green"
	if row.ResultSummary != want {
		t.Errorf("result_summary = %q; want %q (trimmed)", row.ResultSummary, want)
	}
}

// W8: missing .result_summary leaves the column empty.
func TestTickLeavesResultSummaryEmptyWhenAbsent(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("no summary file")
	f.writeSentinel(id, "0\n")

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.ResultSummary != "" {
		t.Errorf("result_summary = %q; want empty", row.ResultSummary)
	}
}

// W9: multiple running rows are checked independently in one tick.
func TestTickHandlesMultipleRowsIndependently(t *testing.T) {
	f := newFixture(t)
	doneID := f.insertRunning("will be done")
	failID := f.insertRunning("will fail")
	stillID := f.insertRunning("still running")

	f.writeSentinel(doneID, "0\n")
	f.writeSentinel(failID, "2\n")
	// stillID gets no sentinel.

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	rows := map[string]int64{
		store.StatusDone:    doneID,
		store.StatusFailed:  failID,
		store.StatusRunning: stillID,
	}
	for wantStatus, id := range rows {
		row, err := f.repo.Get(f.ctx, id)
		if err != nil {
			t.Fatalf("Get(%d): %v", id, err)
		}
		if row.Status != wantStatus {
			t.Errorf("row %d: status = %q; want %q", id, row.Status, wantStatus)
		}
	}
}

// W10: workspace dir missing → row stays running, no audit.
func TestTickIgnoresMissingWorkspaceDir(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("orphan workspace")
	// Do NOT mkdir; this is the PR-44 reaper's signal, not the watcher's.

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want still %q (reaper owns this signal, not us)", row.Status, store.StatusRunning)
	}
	if got := len(f.au.Events()); got != 0 {
		t.Errorf("audit events = %d; want 0", got)
	}
}

// W18 (audit-correctness): when MarkDoneWithSummary fails mid-tick the
// row stays running (next tick retries). The audit entry must NOT be
// labelled `completion.fail` — that label is reserved for "row actually
// flipped to failed status". Confusing labels every tick on a wedged
// row would bloat audit.log and break invariant #2's forensic value.
//
// Pin the contract: a transient store failure carries a distinct
// `completion.transition_failed` action so post-mortem can tell apart
// "Claude exited non-zero" from "store write rejected".
type alwaysFailMarkDoneStore struct {
	completionStore
}

// completionStore is the minimal embedding shim so the fake can override
// just the one method we care about while delegating List / SetCompletedAt
// / MarkFailedWithReason to the real repo.
type completionStore struct {
	inner *store.TaskRepo
}

func (c completionStore) List(ctx context.Context, f store.ListFilter) ([]store.Task, error) {
	return c.inner.List(ctx, f)
}
func (c completionStore) MarkDoneWithSummary(ctx context.Context, id int64, s string, t time.Time) error {
	return c.inner.MarkDoneWithSummary(ctx, id, s, t)
}
func (c completionStore) MarkFailedWithReason(ctx context.Context, id int64, r string) error {
	return c.inner.MarkFailedWithReason(ctx, id, r)
}
func (c completionStore) SetCompletedAt(ctx context.Context, id int64, t time.Time) error {
	return c.inner.SetCompletedAt(ctx, id, t)
}

func (s alwaysFailMarkDoneStore) MarkDoneWithSummary(ctx context.Context, id int64, sum string, t time.Time) error {
	return errors.New("simulated store failure")
}

func TestTickRecordsDistinctActionWhenMarkDoneFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 5, 3, 13, 30, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return now }))
	wrapped := alwaysFailMarkDoneStore{completionStore{inner: repo}}

	root := t.TempDir()
	dirs := dirsFunc(func(id int64) string { return filepath.Join(root, fmt.Sprintf("%d", id)) })
	au := &fakeAuditor{}
	w, err := completion.New(
		completion.WithStore(wrapped),
		completion.WithWorkspaceDirs(dirs),
		completion.WithAuditor(au),
		completion.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, err := repo.Insert(context.Background(), store.Task{
		Source: "manual", Title: "transition fail", Status: store.StatusRunning,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	dir := dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".exit_code"), []byte("0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	row, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Errorf("status = %q; want still %q (transient store failure must not flip the row)", row.Status, store.StatusRunning)
	}

	for _, ev := range au.Events() {
		if ev.Action == "completion.fail" {
			t.Errorf("audit recorded completion.fail for a row that did NOT transition to failed: %+v", ev)
		}
	}
	var found bool
	for _, ev := range au.Events() {
		if ev.Action == "completion.transition_failed" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one completion.transition_failed audit; got %+v", au.Events())
	}
}

// W14 (security): a symlink at the sentinel path must NOT be followed.
// Threat model: a prompt-injected Claude session writes
// .exit_code as a symlink to ~/.marunage/secrets/gmail.json, hoping the
// watcher will read the target and persist its contents into
// tasks.judgment_reason and audit.log (both readable through the Web
// UI / dashboard). The watcher must reject the symlink without ever
// touching the target file.
func TestTickRejectsSymlinkSentinel(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("symlink attack")
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Plant a "secret" file outside the workspace dir whose contents
	// must NEVER appear in judgment_reason / audit.log.
	const secret = "API_TOKEN=super-secret-value-do-not-leak"
	secretPath := filepath.Join(t.TempDir(), "fake-secret.txt")
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		t.Fatalf("WriteFile secret: %v", err)
	}
	if err := os.Symlink(secretPath, filepath.Join(dir, ".exit_code")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status = %q; want %q (symlink sentinel must mark row failed)", row.Status, store.StatusFailed)
	}
	if strings.Contains(row.JudgmentReason, secret) || strings.Contains(row.JudgmentReason, "API_TOKEN") {
		t.Errorf("judgment_reason leaks symlink target contents: %q", row.JudgmentReason)
	}
	if !strings.Contains(strings.ToLower(row.JudgmentReason), "symlink") {
		t.Errorf("judgment_reason = %q; want it to mention the symlink rejection", row.JudgmentReason)
	}
	for _, ev := range f.au.Events() {
		if strings.Contains(ev.Value, secret) || strings.Contains(ev.Value, "API_TOKEN") {
			t.Errorf("audit event leaks symlink target contents: %+v", ev)
		}
	}
}

// W15 (security): a symlink at the result-summary path must not leak.
// Same threat model as W14, but the watcher's policy on .result_summary
// is "optional — empty string when missing or rejected" so the happy
// path still proceeds. The point under test is the no-leak invariant.
func TestTickRejectsSymlinkResultSummary(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("summary symlink")
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const secret = "DB_PASSWORD=hunter2-do-not-leak"
	secretPath := filepath.Join(t.TempDir(), "fake-secret.txt")
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		t.Fatalf("WriteFile secret: %v", err)
	}
	if err := os.Symlink(secretPath, filepath.Join(dir, ".result_summary")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".exit_code"), []byte("0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile exit_code: %v", err)
	}

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(row.ResultSummary, secret) || strings.Contains(row.ResultSummary, "DB_PASSWORD") {
		t.Errorf("result_summary leaks symlink target contents: %q", row.ResultSummary)
	}
}

// W16 (security): an oversized sentinel file must not be slurped into
// memory + the audit log. 1 MiB of "0" digits would technically parse
// to 0 (Atoi accepts leading zeros) but inflates the audit raw= field
// without bound. Cap the read.
func TestTickRejectsOversizedSentinel(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("oversized")
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	huge := strings.Repeat("A", 100*1024) // 100 KiB of garbage
	if err := os.WriteFile(filepath.Join(dir, ".exit_code"), []byte(huge), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Errorf("status = %q; want %q (oversized sentinel must mark row failed)", row.Status, store.StatusFailed)
	}
	if len(row.JudgmentReason) > 512 {
		t.Errorf("judgment_reason length = %d; want bounded (got %q...)", len(row.JudgmentReason), row.JudgmentReason[:128])
	}
	for _, ev := range f.au.Events() {
		if len(ev.Value) > 512 {
			t.Errorf("audit Value length = %d; want bounded", len(ev.Value))
		}
	}
}

// W17 (security): an unparseable sentinel that fits under the size cap
// but is still long (e.g. 256 chars of garbage) must be truncated in
// the displayed reason to keep audit.log forensically useful — without
// the cap a malicious Claude can stuff the audit trail with arbitrary
// strings ("No silent execution" trustworthiness).
func TestTickTruncatesUnparseableSentinelDisplay(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("long garbage")
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// 200 bytes of non-numeric content; under the size cap so the read
	// succeeds, but the display must still be capped.
	garbage := strings.Repeat("X", 200)
	if err := os.WriteFile(filepath.Join(dir, ".exit_code"), []byte(garbage), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusFailed {
		t.Fatalf("status = %q; want %q", row.Status, store.StatusFailed)
	}
	// The display field must not echo the entire 200-byte raw verbatim.
	if strings.Contains(row.JudgmentReason, garbage) {
		t.Errorf("judgment_reason embeds the entire raw sentinel without truncation: %q", row.JudgmentReason)
	}
}

// W13: write-tmp-then-rename produces a single, complete read for the
// watcher. Reproduces the documented `echo $? > .exit_code.tmp && mv
// .exit_code.tmp .exit_code` flow with intermediate Tick calls so a
// reader observing the tmp halfway through never sees a partial file.
func TestTickReadsAtomicallyAfterRename(t *testing.T) {
	f := newFixture(t)
	id := f.insertRunning("atomic")
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	tmpPath := filepath.Join(dir, ".exit_code.tmp")
	finalPath := filepath.Join(dir, ".exit_code")

	// 1. tmp file appears (Claude is mid-write).
	if err := os.WriteFile(tmpPath, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile tmp: %v", err)
	}
	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick (tmp visible): %v", err)
	}
	row, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get pre-rename: %v", err)
	}
	if row.Status != store.StatusRunning {
		t.Fatalf("status before rename = %q; want still running", row.Status)
	}

	// 2. atomic rename to .exit_code (matches mv on the same FS).
	if err := os.Rename(tmpPath, finalPath); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := f.watcher.Tick(f.ctx); err != nil {
		t.Fatalf("Tick (final visible): %v", err)
	}
	row, err = f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get post-rename: %v", err)
	}
	if row.Status != store.StatusDone {
		t.Errorf("status after rename = %q; want %q", row.Status, store.StatusDone)
	}
}

// N1: WithStore is required.
func TestNewRequiresStore(t *testing.T) {
	dirs := dirsFunc(func(int64) string { return "/tmp" })
	_, err := completion.New(completion.WithWorkspaceDirs(dirs))
	if !errors.Is(err, completion.ErrInvalidConfig) {
		t.Fatalf("New(no Store): err = %v; want ErrInvalidConfig", err)
	}
}

// N2: WithWorkspaceDirs is required.
func TestNewRequiresWorkspaceDirs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := store.NewTaskRepo(db)
	_, err = completion.New(completion.WithStore(repo))
	if !errors.Is(err, completion.ErrInvalidConfig) {
		t.Fatalf("New(no WorkspaceDirs): err = %v; want ErrInvalidConfig", err)
	}
}

// N3: defaults are time.Now / NopAuditor / 5s poll. Defaults are
// behavioural; the only one tests can inspect without exporting state
// is the "no panic" baseline plus the auditor (which the watcher
// exercises on a happy-path Tick).
func TestNewDefaultsAreSafe(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := store.NewTaskRepo(db)
	dirs := dirsFunc(func(int64) string { return t.TempDir() })

	w, err := completion.New(
		completion.WithStore(repo),
		completion.WithWorkspaceDirs(dirs),
	)
	if err != nil {
		t.Fatalf("New(defaults): %v", err)
	}
	if w == nil {
		t.Fatal("New returned nil watcher")
	}
	// Tick on an empty queue must not panic with the default auditor /
	// clock.
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick on default-config watcher: %v", err)
	}
}

// R1 + R3: Run calls Tick immediately and then on each poll interval;
// ctx cancellation returns nil.
func TestRunInvokesTickAndExitsOnContextCancel(t *testing.T) {
	f := newFixture(t, completion.WithPollInterval(20*time.Millisecond))
	id := f.insertRunning("loop test")
	// Pre-write the sentinel so the very first Tick can detect it.
	f.writeSentinel(id, "0\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- f.watcher.Run(ctx) }()

	// Wait briefly for the loop to detect the sentinel, then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row, err := f.repo.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if row.Status == store.StatusDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned err = %v; want nil on cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel")
	}

	row, err := f.repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Status != store.StatusDone {
		t.Errorf("status = %q; want %q (loop should have detected sentinel)", row.Status, store.StatusDone)
	}
}
