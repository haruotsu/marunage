package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/haruotsu/marunage/internal/store"

	_ "modernc.org/sqlite"
)

// planColumn captures the PRAGMA table_info shape PR-R04 promises for each
// management-layer column: name, declared type, and nullability.
type planColumn struct {
	declType string
	nullable bool
}

var wantPlanColumns = map[string]planColumn{
	"plan_label":  {declType: "TEXT", nullable: true},
	"plan_reason": {declType: "TEXT", nullable: true},
	"plan_score":  {declType: "REAL", nullable: true},
	"plan_rank":   {declType: "INTEGER", nullable: true},
	"planned_at":  {declType: "TEXT", nullable: true},
}

// tableInfo returns name -> (declared type, nullable) for the given table.
func tableInfo(t *testing.T, db *sql.DB, table string) map[string]planColumn {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]planColumn{}
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &primaryKey); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		out[name] = planColumn{declType: typ, nullable: notNull == 0}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows.Err: %v", err)
	}
	return out
}

// TestMigration0002AddsPlanColumns pins that a fresh Open carries the
// management-layer verdict columns with the documented types, all nullable
// so existing rows are unaffected (redesign §5.2).
func TestMigration0002AddsPlanColumns(t *testing.T) {
	db := openTempDB(t)

	info := tableInfo(t, db, "tasks")
	for name, want := range wantPlanColumns {
		got, ok := info[name]
		if !ok {
			t.Errorf("tasks.%s column missing after Open", name)
			continue
		}
		if got.declType != want.declType {
			t.Errorf("tasks.%s declared type = %q; want %q", name, got.declType, want.declType)
		}
		if !got.nullable {
			t.Errorf("tasks.%s must be nullable (NOT NULL would break existing rows)", name)
		}
	}
}

// TestMigration0002UserVersionIsTwo pins the user_version bump so a future
// migration cannot reuse version 2 and silently skip this one.
func TestMigration0002UserVersionIsTwo(t *testing.T) {
	db := openTempDB(t)

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 2 {
		t.Errorf("user_version = %d; want 2 after 0002 migration", version)
	}
}

// TestMigration0002UpgradesExistingV1DB is the strangler-fig pin: a database
// that already exists at user_version=1 (the previous release) must gain the
// new columns without disturbing its rows. The pre-existing row reads back
// intact with every plan_* column NULL — "新カラム nullable で既存行に影響なし".
func TestMigration0002UpgradesExistingV1DB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")

	// Build the on-disk shape an upgrading user has: 0001 applied, pinned at
	// user_version=1, with one legacy row already persisted.
	body, err := os.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatalf("read 0001_init.sql: %v", err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(string(body)); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO tasks(source, title, status, created_at, updated_at)
		 VALUES (?,?,?,?,?)`,
		"manual", "legacy row", "pending",
		"2026-05-03T00:00:00.000Z", "2026-05-03T00:00:00.000Z",
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("pin user_version=1: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open (should upgrade v1 -> v2): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 2 {
		t.Errorf("user_version after upgrade = %d; want 2", version)
	}

	var (
		title     string
		planLabel sql.NullString
		planScore sql.NullFloat64
		planRank  sql.NullInt64
		plannedAt sql.NullString
	)
	if err := db.QueryRow(
		`SELECT title, plan_label, plan_score, plan_rank, planned_at
		 FROM tasks WHERE source='manual'`,
	).Scan(&title, &planLabel, &planScore, &planRank, &plannedAt); err != nil {
		t.Fatalf("read upgraded legacy row: %v", err)
	}
	if title != "legacy row" {
		t.Errorf("legacy row title = %q; want %q", title, "legacy row")
	}
	if planLabel.Valid || planScore.Valid || planRank.Valid || plannedAt.Valid {
		t.Errorf("legacy row must have NULL plan_* columns, got label=%v score=%v rank=%v planned_at=%v",
			planLabel, planScore, planRank, plannedAt)
	}
}

// TestMigration0002PlanLabelHasNoCheckConstraint guards the extensibility
// principle (redesign D6/D8 原則1): the verdict vocabulary grows over time,
// so plan_label must accept arbitrary strings — unlike status, it carries no
// CHECK enum. A future verdict like "delegate" must insert without a
// migration.
func TestMigration0002PlanLabelHasNoCheckConstraint(t *testing.T) {
	db := openTempDB(t)

	if _, err := db.Exec(
		`INSERT INTO tasks(source, title, plan_label, created_at, updated_at)
		 VALUES (?,?,?,?,?)`,
		"manual", "future verdict", "delegate", "t", "t",
	); err != nil {
		t.Errorf("plan_label must accept arbitrary verdicts (no CHECK enum): %v", err)
	}
}
