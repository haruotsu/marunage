// Package store owns marunage's SQLite-backed persistence. The package
// exports a single Open entry point that returns a *sql.DB ready for
// downstream packages to query against:
//
//   - WAL mode is forced on every connection so readers (Web UI, render)
//     never block the dispatcher's writes (docs/requirement.md "tasks
//     テーブル (SQLite, WAL mode)").
//   - The schema in migrations/ is applied automatically on Open and
//     versioned via PRAGMA user_version so future PRs (PR-12 kv_state
//     additions, later column adds) drop in as 0002_*.sql etc.
//
// PR-10 only ships Open + the migration runner. Repository helpers
// (Insert/Get/List/UpdateStatus/SetWorkspace and the kv_state CRUD) land in
// PR-11 / PR-12; callers in this PR talk to the returned *sql.DB directly.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (or creates) the SQLite database at path, applies any pending
// migrations, and returns the *sql.DB. The parent directory is created with
// 0700 because tasks.db can carry sensitive task bodies fetched from Gmail /
// Slack.
//
// The DSN turns on WAL once for the file plus per-connection foreign_keys,
// synchronous=NORMAL (the documented WAL pairing), and a 5s busy_timeout so
// a competing writer does not surface as SQLITE_BUSY to callers.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}

	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// buildDSN composes the modernc.org/sqlite DSN. Keeping it in its own
// function makes the pragma list reviewable in one place and lets tests
// build the same URL if they ever need to open a second handle.
func buildDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + path + "?" + q.Encode()
}

// migrate applies every embedded migration whose version is greater than
// the file's current PRAGMA user_version. Each migration runs inside a
// single transaction together with its user_version bump so a crash mid-
// apply leaves the DB at the prior version, not in a half-applied state.
func migrate(db *sql.DB) error {
	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}
	return nil
}

type migration struct {
	version int
	name    string
	body    string
}

// loadMigrations enumerates the embedded migrations directory and returns
// the entries sorted by version. Filenames must match NNNN_<description>.sql
// so the version is parseable from the name.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(e.Name(), "%d_", &v); err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %q: filename must start with a positive version (e.g. 0001_init.sql)", e.Name())
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), body: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })

	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d (%s and %s)", out[i].version, out[i-1].name, out[i].name)
		}
	}
	return out, nil
}

func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", m.name, err)
	}
	if _, err := tx.Exec(m.body); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("exec %s: %w", m.name, err)
	}
	// PRAGMA user_version does not accept bound parameters; the version came
	// from a filename we already validated as a positive integer, so direct
	// formatting is safe here.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set user_version for %s: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", m.name, err)
	}
	return nil
}
