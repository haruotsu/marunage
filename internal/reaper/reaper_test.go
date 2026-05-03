package reaper_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/reaper"
	"github.com/haruotsu/marunage/internal/store"
)

// PR-44 reaper test list (t_wada TDD; ticked off as the matching test
// below goes green). Mirrors .test-list.md.
//
//   B1. New() requires WithStore.
//   B2. New() requires WithCmux.
//   B3. New() defaults stuck threshold to 24h.
//
//   C1. Run picks only running rows whose ws is non-empty.
//   C2. A row whose ws is missing from cmux.ListWorkspaces gets
//       MarkFailedWithReason(id, "workspace disappeared (reaper)").
//   C3. Live ws rows stay running.
//   C4. Empty ws running rows are skipped (no claim yet).
//   C5. Each failed transition records audit "reaper.failed" once.
//   C6. cmux.ListWorkspaces error propagates from Run.
//   C7. A row-level MarkFailedWithReason failure does not poison the
//       remaining rows.
//
//   D1+D2. started_at + threshold <= now → audit warn + judgment_reason
//          append.
//   D3.    Status stays running.
//   D4.    Rows still inside the threshold are untouched.
//   D5.    Rows with zero started_at are skipped.
//   D6.    Repeated Run does not double-append the warn token.
//   D7.    Disappeared ws + stuck on the same row → failed wins (no warn).

// fakeCmuxLister fakes cmux.Client.ListWorkspaces only — Reaper does not
// call NewWorkspace / WaitReady / Send so those return errors any caller
// would notice loudly. Keeping the fake narrow keeps the test seam tight.
type fakeCmuxLister struct {
	mu       sync.Mutex
	listResp []cmux.Workspace
	listErr  error
	calls    int
}

func (f *fakeCmuxLister) NewWorkspace(_ context.Context, _ cmux.NewWorkspaceOptions) (cmux.Workspace, error) {
	return cmux.Workspace{}, errors.New("fakeCmuxLister: NewWorkspace not used by reaper")
}
func (f *fakeCmuxLister) WaitReady(_ context.Context, _ cmux.Workspace) error {
	return errors.New("fakeCmuxLister: WaitReady not used by reaper")
}
func (f *fakeCmuxLister) Send(_ context.Context, _ cmux.Workspace, _ string) error {
	return errors.New("fakeCmuxLister: Send not used by reaper")
}
func (f *fakeCmuxLister) ListWorkspaces(_ context.Context) ([]cmux.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.listResp, f.listErr
}

// recordingAuditor captures every AuditEvent so Run-time assertions can
// pin which actions / keys / values were emitted.
type recordingAuditor struct {
	mu     sync.Mutex
	events []config.AuditEvent
}

func (a *recordingAuditor) Record(e config.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *recordingAuditor) snapshot() []config.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]config.AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

// fixture wires a real on-disk SQLite TaskRepo, a fake cmux lister, and
// a recording auditor with a deterministic clock. Tests opt in to a
// custom threshold via opts.
type fixture struct {
	repo    *store.TaskRepo
	cm      *fakeCmuxLister
	aud     *recordingAuditor
	reaper  *reaper.Reaper
	now     time.Time
	ctx     context.Context
	cleanup func()
}

func newFixture(t *testing.T, opts ...reaper.Option) *fixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return now }))
	cm := &fakeCmuxLister{}
	aud := &recordingAuditor{}

	defOpts := []reaper.Option{
		reaper.WithStore(repo),
		reaper.WithCmux(cm),
		reaper.WithClock(func() time.Time { return now }),
		reaper.WithAuditor(aud),
	}
	r, err := reaper.New(append(defOpts, opts...)...)
	if err != nil {
		t.Fatalf("reaper.New: %v", err)
	}
	return &fixture{
		repo: repo, cm: cm, aud: aud, reaper: r, now: now,
		ctx:     context.Background(),
		cleanup: func() { _ = db.Close() },
	}
}

// seedRunning inserts a row with status=running, the given ws + started_at,
// and returns its id. JudgmentReason is left empty unless overridden.
func (f *fixture) seedRunning(t *testing.T, title, ws string, startedAt time.Time) int64 {
	t.Helper()
	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "test",
		Title:  title,
		Status: store.StatusPending,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if ws != "" {
		if err := f.repo.SetWorkspace(f.ctx, id, ws); err != nil {
			t.Fatalf("SetWorkspace: %v", err)
		}
	}
	if !startedAt.IsZero() {
		if err := f.repo.SetStartedAt(f.ctx, id, startedAt); err != nil {
			t.Fatalf("SetStartedAt: %v", err)
		}
	}
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}
	return id
}

// B1: missing WithStore is rejected.
func TestNewRequiresWithStore(t *testing.T) {
	_, err := reaper.New(
		reaper.WithCmux(&fakeCmuxLister{}),
	)
	if !errors.Is(err, reaper.ErrInvalidConfig) {
		t.Fatalf("err = %v; want ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "WithStore") {
		t.Errorf("err = %v; want it to name WithStore", err)
	}
}

// B2: missing WithCmux is rejected.
func TestNewRequiresWithCmux(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	repo := store.NewTaskRepo(db)
	_, err = reaper.New(reaper.WithStore(repo))
	if !errors.Is(err, reaper.ErrInvalidConfig) {
		t.Fatalf("err = %v; want ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "WithCmux") {
		t.Errorf("err = %v; want it to name WithCmux", err)
	}
}

// C1 + C2 + C5: a running row whose ws is gone from cmux flips to failed
// with the documented reason and emits one audit "reaper.failed" event.
func TestRunMarksDisappearedWorkspaceAsFailed(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-1 * time.Hour) // well within 24h
	id := f.seedRunning(t, "lost task", "workspace:100", startedAt)
	// cmux only knows about ws-101, NOT ws-100.
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:101"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusFailed {
		t.Errorf("status = %q; want failed", got.Status)
	}
	if !strings.Contains(got.JudgmentReason, "workspace disappeared") {
		t.Errorf("judgment_reason = %q; want it to mention 'workspace disappeared'", got.JudgmentReason)
	}

	events := f.aud.snapshot()
	if len(events) != 1 {
		t.Fatalf("audit events = %d; want 1 (got %v)", len(events), events)
	}
	if events[0].Action != "reaper.failed" {
		t.Errorf("Action = %q; want %q", events[0].Action, "reaper.failed")
	}
	wantKey := fmt.Sprintf("task:%d", id)
	if events[0].Key != wantKey {
		t.Errorf("Key = %q; want %q", events[0].Key, wantKey)
	}
	if events[0].Value != "workspace:100" {
		t.Errorf("Value = %q; want %q", events[0].Value, "workspace:100")
	}
}

// C3: a running row whose ws is in cmux.live stays running.
func TestRunLeavesLiveWorkspacesAlone(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-30 * time.Minute)
	id := f.seedRunning(t, "alive", "workspace:200", startedAt)
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:200"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusRunning {
		t.Errorf("status = %q; want running (live ws)", got.Status)
	}
	if got.JudgmentReason != "" {
		t.Errorf("judgment_reason = %q; want empty (untouched)", got.JudgmentReason)
	}
	if events := f.aud.snapshot(); len(events) != 0 {
		t.Errorf("audit events = %v; want none for a live ws", events)
	}
}

// C4: a running row whose ws column is empty is skipped — there is no
// workspace to disappear yet (dispatcher race window).
func TestRunSkipsRunningRowsWithoutWorkspace(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-30 * time.Minute)
	id := f.seedRunning(t, "no-ws", "", startedAt)
	f.cm.listResp = nil

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusRunning {
		t.Errorf("status = %q; want running (empty ws skipped)", got.Status)
	}
}

// Non-running rows are never inspected (pending / done / failed / etc.).
// Pin pending here as the canonical case so a regression that widens the
// query to "any row with a ws column" is caught.
func TestRunIgnoresNonRunningRows(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	id, err := f.repo.Insert(f.ctx, store.Task{
		Source: "test",
		Title:  "pending row with stale ws hint",
		Status: store.StatusPending,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.SetWorkspace(f.ctx, id, "workspace:999"); err != nil {
		t.Fatalf("SetWorkspace: %v", err)
	}
	f.cm.listResp = nil

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusPending {
		t.Errorf("status = %q; want pending (non-running rows are off-limits)", got.Status)
	}
}

// C6: cmux.ListWorkspaces failures bubble up so the operator sees what
// blocked the sweep — Run must not silently no-op on a broken probe.
func TestRunPropagatesListWorkspacesError(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()
	f.cm.listErr = errors.New("cmux sad")

	err := f.reaper.Run(f.ctx)
	if err == nil {
		t.Fatalf("Run = nil; want error from cmux probe")
	}
	if !strings.Contains(err.Error(), "cmux sad") {
		t.Errorf("err = %v; want it to wrap the underlying message", err)
	}
}

// D1 + D2 + D3: a running row whose started_at is older than the
// threshold gets a "stuck running" warn appended to judgment_reason and a
// matching audit "reaper.warn" event. Status stays running (human
// judgement).
func TestRunWarnsOnStuckRunningPast24h(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-25 * time.Hour) // past 24h default
	id := f.seedRunning(t, "stuck", "workspace:300", startedAt)
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:300"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusRunning {
		t.Errorf("status = %q; want running (warn must not auto-fail)", got.Status)
	}
	if !strings.Contains(got.JudgmentReason, "[reaper] stuck running over 24h") {
		t.Errorf("judgment_reason = %q; want it to mention '[reaper] stuck running over 24h'", got.JudgmentReason)
	}
	events := f.aud.snapshot()
	if len(events) != 1 {
		t.Fatalf("audit events = %d; want 1 (got %v)", len(events), events)
	}
	if events[0].Action != "reaper.warn" {
		t.Errorf("Action = %q; want %q", events[0].Action, "reaper.warn")
	}
	wantKey := fmt.Sprintf("task:%d", id)
	if events[0].Key != wantKey {
		t.Errorf("Key = %q; want %q", events[0].Key, wantKey)
	}
}

// D4: rows still inside the threshold window are untouched.
func TestRunDoesNotWarnInsideThreshold(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-23 * time.Hour) // inside the 24h default
	id := f.seedRunning(t, "inside", "workspace:400", startedAt)
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:400"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.JudgmentReason != "" {
		t.Errorf("judgment_reason = %q; want empty (still within threshold)", got.JudgmentReason)
	}
	if events := f.aud.snapshot(); len(events) != 0 {
		t.Errorf("audit events = %v; want none", events)
	}
}

// D5: defensive: a running row with zero started_at must not be flagged
// (dispatcher invariant says it cannot happen, but reaper should not
// crash if it does).
func TestRunSkipsStuckCheckWhenStartedAtZero(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	id := f.seedRunning(t, "no-started", "workspace:500", time.Time{})
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:500"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.JudgmentReason != "" {
		t.Errorf("judgment_reason = %q; want empty (zero started_at is opaque)", got.JudgmentReason)
	}
}

// D6: re-running the reaper does not double-append the warn token nor
// emit a second "reaper.warn" event for the same row. Cron / loop calls
// the sweep on every tick; without idempotency the audit log and the
// judgment_reason column would balloon over time.
func TestRunWarnIsIdempotent(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-25 * time.Hour)
	id := f.seedRunning(t, "stuck", "workspace:600", startedAt)
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:600"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run #1: %v", err)
	}
	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run #2: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	count := strings.Count(got.JudgmentReason, "[reaper] stuck running over 24h")
	if count != 1 {
		t.Errorf("warn token appears %d time(s) in judgment_reason %q; want 1", count, got.JudgmentReason)
	}
	warnEvents := 0
	for _, e := range f.aud.snapshot() {
		if e.Action == "reaper.warn" {
			warnEvents++
		}
	}
	if warnEvents != 1 {
		t.Errorf("reaper.warn audit events = %d; want 1 (idempotent)", warnEvents)
	}
}

// D7: a row that is BOTH stuck AND has a disappeared ws gets the harder
// transition (failed) and skips the warn — recording both would double-
// log the same condition, and the failure already carries the reason.
func TestRunFailedWinsOverWarn(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-30 * time.Hour)
	id := f.seedRunning(t, "both", "workspace:700", startedAt)
	f.cm.listResp = nil // ws disappeared

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusFailed {
		t.Errorf("status = %q; want failed (failed wins)", got.Status)
	}
	if strings.Contains(got.JudgmentReason, "stuck running") {
		t.Errorf("judgment_reason = %q; should NOT carry the warn token when row also failed", got.JudgmentReason)
	}
	for _, e := range f.aud.snapshot() {
		if e.Action == "reaper.warn" {
			t.Errorf("emitted reaper.warn for a row that was also failed: %v", e)
		}
	}
}

// Integration: the docs/pr_split_plan PR-44 acceptance scenario. cmux
// returns only ws-101; DB has [ws-100 running, ws-101 running] →
// only ws-100 flips to failed.
func TestRunIntegrationOnlyDeadWorkspaceFails(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-15 * time.Minute)
	deadID := f.seedRunning(t, "dead", "workspace:100", startedAt)
	liveID := f.seedRunning(t, "live", "workspace:101", startedAt)
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:101"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	dead, err := f.repo.Get(f.ctx, deadID)
	if err != nil {
		t.Fatalf("Get(dead): %v", err)
	}
	if dead.Status != store.StatusFailed {
		t.Errorf("dead.Status = %q; want failed", dead.Status)
	}
	live, err := f.repo.Get(f.ctx, liveID)
	if err != nil {
		t.Fatalf("Get(live): %v", err)
	}
	if live.Status != store.StatusRunning {
		t.Errorf("live.Status = %q; want running", live.Status)
	}
}

// WithStuckThreshold honoured: feed a 30-minute threshold and a row that
// started 31 minutes ago — must warn even though it would not at 24h.
func TestRunHonoursCustomStuckThreshold(t *testing.T) {
	f := newFixture(t, reaper.WithStuckThreshold(30*time.Minute))
	defer f.cleanup()

	startedAt := f.now.Add(-31 * time.Minute)
	id := f.seedRunning(t, "stuck-fast", "workspace:800", startedAt)
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:800"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(got.JudgmentReason, "[reaper] stuck running over 30m0s") {
		t.Errorf("judgment_reason = %q; want it to mention the configured 30m0s threshold", got.JudgmentReason)
	}
}

// E2 (race): when a concurrent writer (dispatcher / atomic sentinel /
// human) moves a row out of running between Reaper's List snapshot and
// the per-row write, the status-guarded MarkFailedFromRunningWithReason
// must NOT overwrite the new state. This pins the regression scenario
// PR-43 atomic sentinel will create once it lands: reaper sees the row
// as running in its snapshot, but by the time markDisappeared fires
// the row is already done — the failed write must be suppressed so the
// terminal state survives.
func TestRunDoesNotCrashWhenRowMovedConcurrently(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-15 * time.Minute)
	id := f.seedRunning(t, "racy", "workspace:1000", startedAt)
	// Simulate "operator hits done before reaper's per-row write":
	// move the row to done after seeding but before Run reads it. We
	// drive the race by transitioning here, then setting cmux to claim
	// the ws is gone so Run would otherwise mark it failed.
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusDone); err != nil {
		t.Fatalf("UpdateStatus(done): %v", err)
	}
	f.cm.listResp = nil

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run after concurrent move: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The List(running) probe ran AFTER the move, so the row was never
	// in scope and stays done. This pins the "List is the snapshot"
	// contract: reaper does not re-read on a per-row basis.
	if got.Status != store.StatusDone {
		t.Errorf("status = %q; want done (List excluded the row)", got.Status)
	}
}

// Idempotency robustness: a row whose pre-existing JudgmentReason
// happens to contain the warn token literal as a *substring of an
// operator note* (e.g. "investigated [reaper] stuck running over 24h
// false alarm") must still earn a warn — otherwise an attacker / over-
// helpful operator could silently disable reaper warnings for a row by
// pre-poisoning judgment_reason. The reaper's idempotency check must
// match the warn token only when it appears as its own appended note,
// not as embedded substring text.
func TestRunWarnIsNotSuppressedByEmbeddedSubstring(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-25 * time.Hour)
	id := f.seedRunning(t, "embedded", "workspace:1300", startedAt)
	// Pre-poison judgment_reason with a literal that happens to contain
	// the warn token as a substring inside operator prose.
	embedded := "investigated [reaper] stuck running over 24h false alarm"
	if err := f.repo.AppendJudgmentReason(f.ctx, id, embedded); err != nil {
		t.Fatalf("AppendJudgmentReason setup: %v", err)
	}
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:1300"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Embedded substring must not have suppressed the genuine append:
	// the row's reason should now end with a real warn token, joined
	// by "; " from the prior operator prose. The threshold renders as
	// time.Duration.String() output, which keeps trailing zero units
	// (24h0m0s rather than 24h) — that's the same literal idempotency
	// matches against, so it does not affect the contract.
	want := embedded + "; [reaper] stuck running over 24h0m0s"
	if got.JudgmentReason != want {
		t.Errorf("judgment_reason = %q; want %q", got.JudgmentReason, want)
	}
	warnEvents := 0
	for _, e := range f.aud.snapshot() {
		if e.Action == "reaper.warn" {
			warnEvents++
		}
	}
	if warnEvents != 1 {
		t.Errorf("reaper.warn events = %d; want 1 (embedded substring must not suppress)", warnEvents)
	}
}

// E1 (status guard): a "race latch" Store wrapper transitions the row
// to done between List and the per-row markDisappeared call. The
// status-guarded helper must refuse the failed write so the terminal
// done state survives — pinning the PR-43 atomic-sentinel race the
// design doc §E discusses.
type raceLatchStore struct {
	*store.TaskRepo
	t       *testing.T
	flipID  int64
	flipped bool
}

func (s *raceLatchStore) MarkFailedFromRunningWithReason(ctx context.Context, id int64, reason string) error {
	if id == s.flipID && !s.flipped {
		s.flipped = true
		if err := s.UpdateStatus(ctx, id, store.StatusDone); err != nil {
			s.t.Fatalf("race latch UpdateStatus(done): %v", err)
		}
	}
	return s.TaskRepo.MarkFailedFromRunningWithReason(ctx, id, reason)
}

func TestRunRefusesToOverwriteDoneRowMidSweep(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	repo := store.NewTaskRepo(db, store.WithClock(func() time.Time { return now }))
	cm := &fakeCmuxLister{}
	aud := &recordingAuditor{}

	id, err := repo.Insert(context.Background(), store.Task{
		Source: "test", Title: "race", Status: store.StatusPending,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := repo.SetWorkspace(context.Background(), id, "workspace:1100"); err != nil {
		t.Fatalf("SetWorkspace: %v", err)
	}
	if err := repo.SetStartedAt(context.Background(), id, now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("SetStartedAt: %v", err)
	}
	if err := repo.UpdateStatus(context.Background(), id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}
	cm.listResp = nil // ws disappeared

	latch := &raceLatchStore{TaskRepo: repo, t: t, flipID: id}
	r, err := reaper.New(
		reaper.WithStore(latch),
		reaper.WithCmux(cm),
		reaper.WithClock(func() time.Time { return now }),
		reaper.WithAuditor(aud),
	)
	if err != nil {
		t.Fatalf("reaper.New: %v", err)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusDone {
		t.Errorf("status = %q; want done (status guard must refuse the overwrite)", got.Status)
	}
	for _, e := range aud.snapshot() {
		if e.Action == "reaper.failed" {
			t.Errorf("emitted reaper.failed for a row that left running mid-sweep: %v", e)
		}
	}
}

// Idempotency double of C5: re-running reaper against the same
// disappeared workspace must not emit a second reaper.failed event —
// after the first sweep the row is already failed, and the status
// guard means the second sweep skips it. Pins the daemon / loop tick
// behaviour against audit.log spam.
func TestRunFailedIsIdempotent(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-15 * time.Minute)
	id := f.seedRunning(t, "dead", "workspace:1200", startedAt)
	f.cm.listResp = nil

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run #1: %v", err)
	}
	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run #2: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusFailed {
		t.Errorf("status = %q; want failed", got.Status)
	}
	failedEvents := 0
	for _, e := range f.aud.snapshot() {
		if e.Action == "reaper.failed" && e.Key == fmt.Sprintf("task:%d", id) {
			failedEvents++
		}
	}
	if failedEvents != 1 {
		t.Errorf("reaper.failed events for task = %d; want exactly 1 (idempotent)", failedEvents)
	}
}

// Existing JudgmentReason on the row is preserved when reaper appends
// the warn token — we never want to clobber a triage note an operator
// already wrote.
func TestRunWarnPreservesExistingJudgmentReason(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup()

	startedAt := f.now.Add(-25 * time.Hour)
	id := f.seedRunning(t, "had-note", "workspace:900", startedAt)
	// Stamp an operator note via MarkFailedWithReason then bring the row
	// back to running; this simulates a row with a prior triage trail.
	if err := f.repo.MarkFailedWithReason(f.ctx, id, "operator note"); err != nil {
		t.Fatalf("MarkFailedWithReason: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}
	f.cm.listResp = []cmux.Workspace{{ID: "workspace:900"}}

	if err := f.reaper.Run(f.ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := f.repo.Get(f.ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(got.JudgmentReason, "operator note") {
		t.Errorf("judgment_reason = %q; want existing 'operator note' preserved", got.JudgmentReason)
	}
	if !strings.Contains(got.JudgmentReason, "[reaper] stuck running over 24h") {
		t.Errorf("judgment_reason = %q; want warn token appended", got.JudgmentReason)
	}
}
