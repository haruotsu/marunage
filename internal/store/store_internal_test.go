package store

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// TestLoadMigrationsRejectsBadFilename guards the documented filename
// contract NNNN_<description>.sql. Without this, a typo like "0001init.sql"
// would be silently skipped (or worse, accepted as version 0) and a future
// migration would never run on databases that were Opened against the typo
// file. We exercise loadMigrationsFromFS directly so the regression surfaces
// without having to ship a broken file via embed.
func TestLoadMigrationsRejectsBadFilename(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0001init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE x(id INTEGER);")},
	}
	if _, err := loadMigrationsFromFS(fsys, "m"); err == nil {
		t.Fatalf("expected error for filename violating NNNN_<description>.sql convention")
	} else if !strings.Contains(err.Error(), "NNNN_<description>.sql") {
		t.Errorf("error should mention the naming convention; got %v", err)
	}
}

// TestLoadMigrationsRejectsDuplicateVersion guards against two migrations
// claiming the same version number. Without the check, only one would apply
// (whichever sorts first) and the other's DDL would silently never run on
// fresh databases - a class of bug that is invisible until a column is
// queried in production.
func TestLoadMigrationsRejectsDuplicateVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0001_a.sql": &fstest.MapFile{Data: []byte("CREATE TABLE a(id INTEGER);")},
		"m/0001_b.sql": &fstest.MapFile{Data: []byte("CREATE TABLE b(id INTEGER);")},
	}
	if _, err := loadMigrationsFromFS(fsys, "m"); err == nil {
		t.Fatalf("expected duplicate-version error")
	} else if !strings.Contains(err.Error(), "duplicate migration version") {
		t.Errorf("error should mention duplicate version; got %v", err)
	}
}

// TestApplyMigrationRollbacksOnExecError pins the contract called out in the
// applyMigration doc comment: "a crash mid-apply leaves the DB at the prior
// version". A failing migration body must roll back the user_version bump
// together with whatever DDL it managed to issue, so re-Opening the DB does
// not see version N when version N never fully applied.
func TestApplyMigrationRollbacksOnExecError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var before int
	if err := db.QueryRow("PRAGMA user_version").Scan(&before); err != nil {
		t.Fatalf("read user_version before: %v", err)
	}

	bad := migration{
		version: before + 100,
		name:    "9999_bad.sql",
		body:    "DEFINITELY NOT VALID SQL;",
	}
	if err := applyMigration(db, bad); err == nil {
		t.Fatalf("applyMigration with malformed SQL should fail")
	}

	var after int
	if err := db.QueryRow("PRAGMA user_version").Scan(&after); err != nil {
		t.Fatalf("read user_version after: %v", err)
	}
	if after != before {
		t.Errorf("user_version must roll back on Exec failure: before=%d after=%d", before, after)
	}
}
