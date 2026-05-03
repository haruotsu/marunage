package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// openTempDB is a small helper so the per-test boilerplate (pick a temp
// path, Open, schedule Close) does not drown the actual assertions.
func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestOpenCreatesSchema pins the smallest contract: a fresh Open call
// produces a SQLite file that already has the documented tasks and kv_state
// tables. Downstream PRs (PR-11 repository layer, PR-12 kv_state layer) must
// be able to assume the schema is in place — running any DDL of their own
// would defeat the migration mechanism this PR introduces.
func TestOpenCreatesSchema(t *testing.T) {
	db := openTempDB(t)

	for _, table := range []string{"tasks", "kv_state"} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after Open: %v", table, err)
			continue
		}
		if name != table {
			t.Errorf("sqlite_master returned %q for table %q", name, table)
		}
	}
}

// TestOpenForcesWALMode pins the documented invariant "tasks テーブル
// (SQLite, WAL mode)": readers (Web UI, render, status) must be able to
// observe rows while the dispatcher writes, which only WAL gives us. Falling
// back to the default "delete" journal silently would be a regression that
// only surfaces under load, so we encode the expectation as a unit test.
func TestOpenForcesWALMode(t *testing.T) {
	db := openTempDB(t)

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q; want wal", mode)
	}
}

// TestTasksRoundTrip is the PR-10 acceptance test from pr_split_plan.md
// ("スキーマ作成→INSERT→SELECT の往復"). It also locks down the column set
// the spec promises so that an accidental rename or drop in a future
// migration shows up here, not in PR-11's repository code.
func TestTasksRoundTrip(t *testing.T) {
	db := openTempDB(t)

	const insert = `INSERT INTO tasks
        (source, external_id, title, body, status, priority, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	res, err := db.Exec(insert,
		"gmail", "thread-123", "Reply to alice", "body text",
		"pending", 5, "2026-05-03T00:00:00Z", "2026-05-03T00:00:00Z")
	if err != nil {
		t.Fatalf("INSERT tasks: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	if id <= 0 {
		t.Fatalf("LastInsertId = %d; want positive", id)
	}

	var (
		gotID                int64
		source, externalID   string
		title, status        string
		priority             int
		createdAt, updatedAt string
	)
	err = db.QueryRow(
		`SELECT id, source, external_id, title, status, priority, created_at, updated_at
         FROM tasks WHERE id=?`, id,
	).Scan(&gotID, &source, &externalID, &title, &status, &priority, &createdAt, &updatedAt)
	if err != nil {
		t.Fatalf("SELECT tasks: %v", err)
	}
	if gotID != id || source != "gmail" || externalID != "thread-123" ||
		title != "Reply to alice" || status != "pending" || priority != 5 {
		t.Errorf("round trip mismatch: id=%d source=%q ext=%q title=%q status=%q prio=%d",
			gotID, source, externalID, title, status, priority)
	}
}

// TestTasksSourceExternalIDUnique guards invariant #4 "Idempotency": a
// Discovery plugin that re-fetches the same Gmail thread or Slack message
// must hit the unique index and not produce a duplicate row. Manually-added
// rows (external_id IS NULL) intentionally bypass the constraint.
func TestTasksSourceExternalIDUnique(t *testing.T) {
	db := openTempDB(t)

	const insert = `INSERT INTO tasks
        (source, external_id, title, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)`

	if _, err := db.Exec(insert, "gmail", "msg-1", "first", "t", "t"); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}
	if _, err := db.Exec(insert, "gmail", "msg-1", "dup", "t", "t"); err == nil {
		t.Fatalf("second INSERT with same (source, external_id) should fail")
	}

	// NULL external_id must remain insertable repeatedly so manual `marunage
	// add` calls without an upstream id are not blocked by this index.
	if _, err := db.Exec(insert, "gmail", nil, "manual a", "t", "t"); err != nil {
		t.Fatalf("manual INSERT #1 (NULL external_id): %v", err)
	}
	if _, err := db.Exec(insert, "gmail", nil, "manual b", "t", "t"); err != nil {
		t.Fatalf("manual INSERT #2 (NULL external_id): %v", err)
	}
}

// TestKVStateRoundTrip pins the kv_state contract: a key written with one
// value and re-written via INSERT OR REPLACE returns the latest value.
// Discovery plugins use this exact pattern for checkpoints (e.g.
// gmail_last_id), so the test doubles as a usage example.
func TestKVStateRoundTrip(t *testing.T) {
	db := openTempDB(t)

	const upsert = `INSERT INTO kv_state(key, value, updated_at) VALUES (?,?,?)
        ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`

	if _, err := db.Exec(upsert, "gmail_last_id", "abc", "2026-05-03T00:00:00Z"); err != nil {
		t.Fatalf("upsert #1: %v", err)
	}
	if _, err := db.Exec(upsert, "gmail_last_id", "def", "2026-05-03T00:01:00Z"); err != nil {
		t.Fatalf("upsert #2: %v", err)
	}

	var value, updatedAt string
	err := db.QueryRow(`SELECT value, updated_at FROM kv_state WHERE key=?`, "gmail_last_id").
		Scan(&value, &updatedAt)
	if err != nil {
		t.Fatalf("SELECT kv_state: %v", err)
	}
	if value != "def" || updatedAt != "2026-05-03T00:01:00Z" {
		t.Errorf("kv_state = (%q, %q); want (%q, %q)",
			value, updatedAt, "def", "2026-05-03T00:01:00Z")
	}
}

// TestMigrationsIdempotentAcrossReopens locks the actual production
// lifecycle: every `marunage` invocation reopens tasks.db, so re-running
// CREATE TABLE on a populated database would either fail (no IF NOT EXISTS)
// or, worse, silently mask a forgotten user_version bump. The user_version
// must equal the highest embedded migration number after the first Open and
// stay there on subsequent Opens, with previously inserted rows preserved.
func TestMigrationsIdempotentAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if _, err := first.Exec(
		`INSERT INTO tasks(source, title, created_at, updated_at) VALUES (?,?,?,?)`,
		"manual", "survives reopen", "t", "t",
	); err != nil {
		t.Fatalf("seed INSERT: %v", err)
	}
	var versionBefore int
	if err := first.QueryRow("PRAGMA user_version").Scan(&versionBefore); err != nil {
		t.Fatalf("read user_version #1: %v", err)
	}
	if versionBefore < 1 {
		t.Fatalf("user_version after fresh Open = %d; want >= 1", versionBefore)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	var versionAfter int
	if err := second.QueryRow("PRAGMA user_version").Scan(&versionAfter); err != nil {
		t.Fatalf("read user_version #2: %v", err)
	}
	if versionAfter != versionBefore {
		t.Errorf("user_version drifted across reopen: %d -> %d", versionBefore, versionAfter)
	}

	var count int
	if err := second.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatalf("COUNT(*): %v", err)
	}
	if count != 1 {
		t.Errorf("row count after reopen = %d; want 1 (data must survive a no-op migrate)", count)
	}
}

// TestOpenChmodsDBFile0600 closes the gap between the 0700 parent directory
// and the SQLite-created files (tasks.db / -wal / -shm), which would
// otherwise inherit the user umask (often 0644). tasks.body / notes carry
// Gmail / Slack content per docs/requirement.md, so file-level 0600 is the
// security-design baseline. Walked by the security/data-model design review.
func TestOpenChmodsDBFile0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Touch a write so SQLite materialises the WAL / SHM sidecars too.
	if _, err := db.Exec(
		`INSERT INTO tasks(source, title, created_at, updated_at) VALUES (?,?,?,?)`,
		"manual", "perm check", "t", "t",
	); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		info, err := os.Stat(p)
		if err != nil {
			// -wal / -shm may not exist if SQLite has flushed; that is fine.
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s perm = %o; want 0600 (Gmail/Slack content lives here)", p, perm)
		}
	}
}

// TestOpenSerializesWriters: SQLite + WAL allows many concurrent readers but
// a single writer at a time. Without SetMaxOpenConns(1), database/sql is
// free to hand out a second connection mid-transaction and surface
// SQLITE_BUSY (or worse, succeed under the busy_timeout but reorder writes).
// Pinning MaxOpenConnections == 1 keeps writers strictly serialised; reads
// still go through the same connection without contention at this scale.
func TestOpenSerializesWriters(t *testing.T) {
	db := openTempDB(t)

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d; want 1 (writer must be serialised on WAL SQLite)", got)
	}
}

// TestStatusEnumEnforced rejects unknown status values at the DB layer so an
// app-side typo (e.g. 'complete' instead of 'done') cannot silently violate
// invariants #1 (No silent loss) and #3 (Reversibility). The enum mirrors
// docs/requirement.md "データモデル".
func TestStatusEnumEnforced(t *testing.T) {
	db := openTempDB(t)

	_, err := db.Exec(
		`INSERT INTO tasks(source, title, status, created_at, updated_at) VALUES (?,?,?,?,?)`,
		"gmail", "bad status", "garbage", "t", "t",
	)
	if err == nil {
		t.Fatalf("INSERT with status='garbage' should fail at the DB layer")
	}

	for _, s := range []string{"pending", "running", "done", "failed", "skipped", "waiting_human"} {
		_, err := db.Exec(
			`INSERT INTO tasks(source, title, status, created_at, updated_at) VALUES (?,?,?,?,?)`,
			"gmail", "ok status", s, "t", "t",
		)
		if err != nil {
			t.Errorf("INSERT with documented status %q failed: %v", s, err)
		}
	}
}

// TestNotesMustBeJSONOrNull guards the eventual `json_extract(notes,
// '$.lock_hint')` consumer in PR-42 dispatch: an invalid JSON literal would
// silently scan as NULL there, masking misuse. Allowing NULL keeps the
// "manual add without metadata" path open.
func TestNotesMustBeJSONOrNull(t *testing.T) {
	db := openTempDB(t)

	const insert = `INSERT INTO tasks(source, title, notes, created_at, updated_at)
        VALUES (?,?,?,?,?)`

	if _, err := db.Exec(insert, "gmail", "no notes", nil, "t", "t"); err != nil {
		t.Errorf("notes=NULL must be accepted: %v", err)
	}
	if _, err := db.Exec(insert, "gmail", "good json", `{"channel":"#foo"}`, "t", "t"); err != nil {
		t.Errorf("notes=valid JSON must be accepted: %v", err)
	}
	if _, err := db.Exec(insert, "gmail", "bad json", `not json`, "t", "t"); err == nil {
		t.Fatalf("notes='not json' should be rejected by json_valid CHECK")
	}
}

// TestKVStateValueRequired pins kv_state.value as NOT NULL: "checkpoint not
// set" is represented by row absence, not by a NULL value, so the column
// being NOT NULL eliminates a class of "ghost row with empty value" bugs in
// the Discovery plugins.
func TestKVStateValueRequired(t *testing.T) {
	db := openTempDB(t)

	_, err := db.Exec(`INSERT INTO kv_state(key, value, updated_at) VALUES (?,?,?)`,
		"slack_last_ts", nil, "t")
	if err == nil {
		t.Fatalf("INSERT kv_state with NULL value should fail")
	}
}

// TestUpdatedAtAutoBumps verifies the AFTER UPDATE trigger that keeps
// updated_at honest even when callers forget to set it. This eliminates the
// "I edited the row but the list view still shows yesterday" failure mode
// before PR-11 / PR-20 surface it in the CLI.
func TestUpdatedAtAutoBumps(t *testing.T) {
	db := openTempDB(t)

	res, err := db.Exec(
		`INSERT INTO tasks(source, title, created_at, updated_at) VALUES (?,?,?,?)`,
		"manual", "trigger test",
		"2026-05-03T00:00:00.000Z", "2026-05-03T00:00:00.000Z",
	)
	if err != nil {
		t.Fatalf("seed INSERT: %v", err)
	}
	id, _ := res.LastInsertId()

	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE id=?`, id); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	var updatedAt string
	if err := db.QueryRow(`SELECT updated_at FROM tasks WHERE id=?`, id).Scan(&updatedAt); err != nil {
		t.Fatalf("SELECT updated_at: %v", err)
	}
	if updatedAt == "2026-05-03T00:00:00.000Z" {
		t.Errorf("updated_at unchanged after UPDATE: %q (trigger must bump it)", updatedAt)
	}
}
