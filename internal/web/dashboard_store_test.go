package web_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/haruotsu/marunage/internal/store"
	"github.com/haruotsu/marunage/internal/web"
)

// dashboardSQLFixture spins up a fresh on-disk SQLite plus the two
// repos every dashboard SQL query reads through. Lives in
// internal/web_test (the external test package) so it can lean on
// store's exported helpers without exporting test plumbing back into
// the production package.
type dashboardSQLFixture struct {
	ctx       context.Context
	db        *sql.DB
	taskRepo  *store.TaskRepo
	kvRepo    *store.KVStateRepo
	sqlStore  web.DashboardStore
	closeFunc func()
}

// kvRepoExec is the test-only escape hatch for rewriting kv_state
// timestamps directly. The repo intentionally derives updated_at
// from the injected clock, but the latest-per-source assertion
// needs distinct timestamps for two adjacent Set calls.
func (f *dashboardSQLFixture) kvRepoExec(q string, args ...any) (sql.Result, error) {
	return f.db.ExecContext(f.ctx, q, args...)
}

func newDashboardSQLFixture(t *testing.T) *dashboardSQLFixture {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	clock := time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC)
	tasks := store.NewTaskRepo(db, store.WithClock(func() time.Time { return clock }))
	kv := store.NewKVStateRepo(db, store.WithKVClock(func() time.Time { return clock }))

	return &dashboardSQLFixture{
		ctx:       context.Background(),
		db:        db,
		taskRepo:  tasks,
		kvRepo:    kv,
		sqlStore:  web.NewSQLDashboardStore(db),
		closeFunc: func() { _ = db.Close() },
	}
}

func TestSQLDashboardStore_RunningOrdersByStartedAt(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	first := time.Date(2026, 5, 4, 6, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	idEarly, err := f.taskRepo.Insert(f.ctx, store.Task{
		Source: "markdown",
		Title:  "early",
		Status: store.StatusRunning,
		WS:     "workspace:101",
		Body:   "early body content",
	})
	if err != nil {
		t.Fatalf("insert early: %v", err)
	}
	if err := f.taskRepo.SetStartedAt(f.ctx, idEarly, first); err != nil {
		t.Fatalf("set started_at early: %v", err)
	}

	idLate, err := f.taskRepo.Insert(f.ctx, store.Task{
		Source: "markdown",
		Title:  "late",
		Status: store.StatusRunning,
		WS:     "workspace:102",
		Body:   "late body content",
	})
	if err != nil {
		t.Fatalf("insert late: %v", err)
	}
	if err := f.taskRepo.SetStartedAt(f.ctx, idLate, second); err != nil {
		t.Fatalf("set started_at late: %v", err)
	}

	got, err := f.sqlStore.Running(f.ctx, 32, 64)
	if err != nil {
		t.Fatalf("Running: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("running len = %d; want 2", len(got))
	}
	if got[0].ID != idEarly || got[1].ID != idLate {
		t.Errorf("running order = [%d, %d]; want [%d, %d]", got[0].ID, got[1].ID, idEarly, idLate)
	}
	if got[0].WS != "workspace:101" {
		t.Errorf("running[0].WS = %q; want workspace:101", got[0].WS)
	}
	if got[0].OutputPreview == "" {
		t.Errorf("running[0].OutputPreview empty; want first bytes of body")
	}
}

func TestSQLDashboardStore_RunningTruncatesPreview(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	id, err := f.taskRepo.Insert(f.ctx, store.Task{
		Source: "markdown",
		Title:  "long",
		Status: store.StatusRunning,
		Body:   "abcdefghijklmnopqrstuvwxyz0123456789",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := f.taskRepo.SetStartedAt(f.ctx, id, time.Date(2026, 5, 4, 6, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("set started_at: %v", err)
	}

	rows, err := f.sqlStore.Running(f.ctx, 4, 5)
	if err != nil {
		t.Fatalf("Running: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	if got := rows[0].OutputPreview; len(got) > 5 {
		t.Errorf("preview = %q; want length <= 5", got)
	}
}

func TestSQLDashboardStore_PendingTopOrdersByPriorityThenCreated(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	older := time.Date(2026, 5, 4, 5, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	idHighOlder, err := f.taskRepo.Insert(f.ctx, store.Task{
		Source: "markdown", Title: "high-older", Priority: 5, CreatedAt: older,
	})
	if err != nil {
		t.Fatalf("insert high-older: %v", err)
	}
	idLowOlder, err := f.taskRepo.Insert(f.ctx, store.Task{
		Source: "markdown", Title: "low-older", Priority: 1, CreatedAt: older,
	})
	if err != nil {
		t.Fatalf("insert low-older: %v", err)
	}
	idHighNewer, err := f.taskRepo.Insert(f.ctx, store.Task{
		Source: "markdown", Title: "high-newer", Priority: 5, CreatedAt: newer,
	})
	if err != nil {
		t.Fatalf("insert high-newer: %v", err)
	}

	rows, err := f.sqlStore.PendingTop(f.ctx, 10)
	if err != nil {
		t.Fatalf("PendingTop: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d; want 3", len(rows))
	}
	if rows[0].ID != idHighOlder || rows[1].ID != idHighNewer || rows[2].ID != idLowOlder {
		t.Errorf("ordering = [%d %d %d]; want [%d %d %d]", rows[0].ID, rows[1].ID, rows[2].ID, idHighOlder, idHighNewer, idLowOlder)
	}
}

func TestSQLDashboardStore_PendingTopRespectsLimit(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	for i := 0; i < 4; i++ {
		if _, err := f.taskRepo.Insert(f.ctx, store.Task{
			Source: "markdown", Title: "p", Priority: i,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	rows, err := f.sqlStore.PendingTop(f.ctx, 2)
	if err != nil {
		t.Fatalf("PendingTop: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d; want 2", len(rows))
	}
}

func TestSQLDashboardStore_PendingCountIgnoresLimit(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	for i := 0; i < 5; i++ {
		if _, err := f.taskRepo.Insert(f.ctx, store.Task{
			Source: "markdown", Title: "p",
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	got, err := f.sqlStore.PendingCount(f.ctx)
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if got != 5 {
		t.Errorf("PendingCount = %d; want 5", got)
	}
}

func TestSQLDashboardStore_RecentAggregatesByStatusAndSource(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)

	// Within the window: 1 done markdown, 1 failed markdown, 1 skipped gmail
	id1, _ := f.taskRepo.Insert(f.ctx, store.Task{Source: "markdown", Title: "d"})
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, id1, "ok", now.Add(-time.Hour)); err != nil {
		t.Fatalf("done: %v", err)
	}
	id2, _ := f.taskRepo.Insert(f.ctx, store.Task{Source: "markdown", Title: "f"})
	if err := f.taskRepo.MarkFailedWithReason(f.ctx, id2, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := f.taskRepo.SetCompletedAt(f.ctx, id2, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("set completed_at: %v", err)
	}
	id3, _ := f.taskRepo.Insert(f.ctx, store.Task{Source: "gmail", Title: "s"})
	if err := f.taskRepo.UpdateStatus(f.ctx, id3, store.StatusSkipped); err != nil {
		t.Fatalf("skip: %v", err)
	}

	// Outside the window: a done row 25 hours ago — must NOT be counted.
	idOld, _ := f.taskRepo.Insert(f.ctx, store.Task{Source: "markdown", Title: "old"})
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, idOld, "old", now.Add(-25*time.Hour)); err != nil {
		t.Fatalf("done old: %v", err)
	}

	rec, err := f.sqlStore.Recent(f.ctx, since)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if rec.DoneCount != 1 || rec.FailedCount != 1 || rec.SkippedCount != 1 {
		t.Errorf("totals = %+v; want done=1 failed=1 skipped=1", rec)
	}
	if len(rec.BySource) == 0 {
		t.Fatalf("BySource empty; want at least markdown + gmail rows")
	}
	bySource := map[string]web.DashboardSourceCount{}
	for _, r := range rec.BySource {
		bySource[r.Source] = r
	}
	if md := bySource["markdown"]; md.Done != 1 || md.Failed != 1 || md.Skipped != 0 {
		t.Errorf("markdown breakdown = %+v; want done=1 failed=1 skipped=0", md)
	}
	if gm := bySource["gmail"]; gm.Skipped != 1 || gm.Done != 0 || gm.Failed != 0 {
		t.Errorf("gmail breakdown = %+v; want skipped=1", gm)
	}
}

func TestSQLDashboardStore_SourceCheckpointsReturnsLatestPerPrefix(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	if err := f.kvRepo.Set(f.ctx, "gmail_last_id", "abc"); err != nil {
		t.Fatalf("set gmail_last_id: %v", err)
	}
	if err := f.kvRepo.Set(f.ctx, "slack_last_ts", "1700000000.000100"); err != nil {
		t.Fatalf("set slack_last_ts: %v", err)
	}
	if err := f.kvRepo.Set(f.ctx, "gmail_state_other", "x"); err != nil {
		t.Fatalf("set gmail_state_other: %v", err)
	}

	got, err := f.sqlStore.SourceCheckpoints(f.ctx)
	if err != nil {
		t.Fatalf("SourceCheckpoints: %v", err)
	}
	if _, ok := got["gmail"]; !ok {
		t.Errorf("gmail missing from checkpoint map: %#v", got)
	}
	if _, ok := got["slack"]; !ok {
		t.Errorf("slack missing from checkpoint map: %#v", got)
	}
}

// TestSQLDashboardStore_SourceCheckpointsParsesMarkdownColonNamespace
// guards the kv_state key shape the markdown plugin actually writes
// (`markdown:mtime:<path>`).  A regex-only `_` split would silently
// drop every markdown checkpoint and leave the dashboard's source
// row stuck at "never" — the bug PR-63's first round-trip review
// caught.
func TestSQLDashboardStore_SourceCheckpointsParsesMarkdownColonNamespace(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	if err := f.kvRepo.Set(f.ctx, "markdown:mtime:/tmp/todo.md", "2026-05-04T07:30:00Z"); err != nil {
		t.Fatalf("set markdown checkpoint: %v", err)
	}
	got, err := f.sqlStore.SourceCheckpoints(f.ctx)
	if err != nil {
		t.Fatalf("SourceCheckpoints: %v", err)
	}
	if _, ok := got["markdown"]; !ok {
		t.Errorf("markdown source missing from checkpoint map: %#v", got)
	}
}

// TestSQLDashboardStore_SourceCheckpointsKeepsLatestPerSource pins
// the "newest updated_at wins" contract for sources that publish
// multiple kv_state entries (e.g. markdown writing one row per file).
// Without this the dashboard might surface a stale per-file mtime
// rather than the freshest discovery cycle.
func TestSQLDashboardStore_SourceCheckpointsKeepsLatestPerSource(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	// Two writes against the same source under the deterministic clock
	// share the same updated_at; sleep equivalent: rewind the older
	// one to a known earlier point via raw SQL so the assertion below
	// can compare timestamps deterministically.
	if err := f.kvRepo.Set(f.ctx, "markdown:mtime:/tmp/old.md", "v1"); err != nil {
		t.Fatalf("set old: %v", err)
	}
	older := "2026-05-04T05:00:00.000Z"
	newer := "2026-05-04T07:00:00.000Z"
	exec := func(q string, args ...any) {
		t.Helper()
		// Reach into the underlying *sql.DB through the fixture's
		// kvRepo by issuing a fresh statement; the fixture exposes
		// neither, so fall through to a second Set + manual rewrite.
		if _, err := f.kvRepoExec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec(`UPDATE kv_state SET updated_at = ? WHERE key = ?`, older, "markdown:mtime:/tmp/old.md")
	if err := f.kvRepo.Set(f.ctx, "markdown:mtime:/tmp/new.md", "v2"); err != nil {
		t.Fatalf("set new: %v", err)
	}
	exec(`UPDATE kv_state SET updated_at = ? WHERE key = ?`, newer, "markdown:mtime:/tmp/new.md")

	got, err := f.sqlStore.SourceCheckpoints(f.ctx)
	if err != nil {
		t.Fatalf("SourceCheckpoints: %v", err)
	}
	want, err := time.Parse("2006-01-02T15:04:05.000Z", newer)
	if err != nil {
		t.Fatalf("parse want: %v", err)
	}
	if !got["markdown"].Equal(want) {
		t.Errorf("markdown checkpoint = %v; want %v", got["markdown"], want)
	}
}

// TestSQLDashboardStore_RecentIncludesBoundary pins the half-open
// window contract: a row whose completed_at equals the cutoff must be
// counted (the SQL uses `>=`).  Off-by-one here would silently miss
// the row that finished exactly 24h ago on every refresh.
func TestSQLDashboardStore_RecentIncludesBoundary(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	cutoff := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	id, err := f.taskRepo.Insert(f.ctx, store.Task{Source: "markdown", Title: "boundary"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := f.taskRepo.MarkDoneWithSummary(f.ctx, id, "ok", cutoff); err != nil {
		t.Fatalf("done: %v", err)
	}
	rec, err := f.sqlStore.Recent(f.ctx, cutoff)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if rec.DoneCount != 1 {
		t.Errorf("DoneCount on cutoff = %d; want 1 (>= boundary)", rec.DoneCount)
	}
}

// TestSQLDashboardStore_RunningTruncatesPreviewOnUTF8Boundary guards
// the preview-truncation helper against splitting a multi-byte rune
// in half — the truncation is meant to land on a UTF-8 boundary so
// the rendered preview never contains broken bytes.
func TestSQLDashboardStore_RunningTruncatesPreviewOnUTF8Boundary(t *testing.T) {
	f := newDashboardSQLFixture(t)
	t.Cleanup(f.closeFunc)

	// "あいうえお" — five 3-byte runes (15 bytes total). A naive
	// byte-based truncate at 7 would leave a half rune.
	id, err := f.taskRepo.Insert(f.ctx, store.Task{
		Source: "markdown",
		Title:  "japanese",
		Status: store.StatusRunning,
		Body:   "あいうえお",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := f.taskRepo.SetStartedAt(f.ctx, id, time.Date(2026, 5, 4, 6, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("set started_at: %v", err)
	}
	rows, err := f.sqlStore.Running(f.ctx, 4, 7)
	if err != nil {
		t.Fatalf("Running: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	if got := rows[0].OutputPreview; !utf8.ValidString(got) {
		t.Errorf("preview = %q; want valid UTF-8", got)
	}
}
