// Package store owns marunage's SQLite-backed persistence. The package
// exports a single Open entry point that returns a *sql.DB ready for
// downstream packages to query against:
//
//   - WAL mode is forced on every connection so readers (Web UI, render)
//     never block the dispatcher's writes (docs/requirement.md "tasks
//     テーブル (SQLite, WAL mode)").
//   - The schema in migrations/ is applied automatically on Open and
//     versioned via PRAGMA user_version so future PRs (PR-12 kv_state
//     repository helpers, later column adds) drop in as 0002_*.sql etc.
//   - tasks.db / tasks.db-wal / tasks.db-shm are chmoded to 0600 so the
//     Gmail / Slack content that lands in tasks.body / notes is not
//     readable by other local users.
//   - Writers are serialised via SetMaxOpenConns(1). SQLite in WAL mode
//     supports many concurrent readers but only one writer, and letting
//     database/sql open a second writer just to surface SQLITE_BUSY (or
//     reorder under busy_timeout) is worse than queueing here.
//
// Timestamp convention: TEXT columns hold ISO8601 UTC with millisecond
// precision (`YYYY-MM-DDTHH:MM:SS.sssZ`) so lexicographic ORDER BY matches
// chronological order. The 0001_init.sql trigger uses
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now') which produces the same shape; Go
// callers should use time.UTC().Format("2006-01-02T15:04:05.000Z").
//
// PR-10 only ships Open + the migration runner. Repository helpers
// (Insert/Get/List/UpdateStatus/SetWorkspace and the kv_state CRUD) land in
// PR-11 / PR-12; callers in this PR talk to the returned *sql.DB directly.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (or creates) the SQLite database at path, applies any pending
// migrations, and returns the *sql.DB. The parent directory is created with
// 0700 because tasks.db can carry sensitive task bodies fetched from Gmail /
// Slack; the DB file itself plus its -wal / -shm sidecars are then chmoded
// to 0600 so they do not inherit the user umask.
//
// The DSN turns on WAL once for the file plus per-connection foreign_keys,
// synchronous=NORMAL (the documented WAL pairing), and a 5s busy_timeout so
// a competing writer does not surface as SQLITE_BUSY to callers.
func Open(path string) (*sql.DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// MkdirAll is a no-op on an existing directory and will not narrow its
	// mode, so explicitly tighten an already-present parent (e.g. /tmp/foo
	// created at 0755 by an earlier step) down to 0700.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod parent %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Serialise writers (single connection). WAL allows concurrent reads
	// transparently through this same connection; the busy_timeout in the
	// DSN handles the rare cases where a read momentarily blocks a write.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := tightenPerms(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// tightenPerms sets the SQLite file plus its WAL/SHM sidecars to 0600.
// It is called after migrate() so the WAL/SHM files have been materialised
// by the migration writes; missing sidecars are tolerated (SQLite may have
// already checkpointed).
func tightenPerms(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		if err := os.Chmod(p, 0o600); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("chmod %s: %w", p, err)
		}
	}
	return nil
}

// buildDSN composes the modernc.org/sqlite DSN. Path is escaped so a path
// that happens to contain `?` / `#` / a space (macOS "iCloud Drive" etc.)
// does not collide with the query string. Pragmas are listed in their own
// function so the set is reviewable in one place.
func buildDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")

	u := url.URL{
		Scheme:   "file",
		Opaque:   path,
		RawQuery: q.Encode(),
	}
	return u.String()
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

// migrationName matches NNNN_<description>.sql where description is
// alphanumeric / underscore / hyphen. Sscanf would also accept malformed
// names like "0001init.sql"; the regex pins the convention.
var migrationName = regexp.MustCompile(`^(\d+)_[A-Za-z0-9_-]+\.sql$`)

// loadMigrations enumerates the embedded migrations directory and returns
// the entries sorted by version.
func loadMigrations() ([]migration, error) {
	return loadMigrationsFromFS(migrationsFS, "migrations")
}

// loadMigrationsFromFS is the testable core of loadMigrations: it walks
// dir inside fsys and returns the entries sorted by version. Splitting it
// out lets internal tests feed an in-memory fstest.MapFS to exercise the
// "bad filename" / "duplicate version" error paths without having to ship
// broken files in the embedded set.
func loadMigrationsFromFS(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		match := migrationName.FindStringSubmatch(e.Name())
		if match == nil {
			return nil, fmt.Errorf("migration %q: filename must match NNNN_<description>.sql", e.Name())
		}
		v, err := strconv.Atoi(match[1])
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %q: version must be a positive integer", e.Name())
		}
		body, err := fs.ReadFile(fsys, dir+"/"+e.Name())
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

// applyMigration executes the entire migration body as a single Exec inside
// one transaction. The contract for migration files is "1 file = 1
// transaction; statements are separated by `;` and handed to the driver
// verbatim", so multi-statement files (CREATE TABLE plus indices plus
// triggers) work out of the box. The user_version bump is part of the same
// transaction so a crash mid-apply leaves the DB at the prior version.
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
