package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/store"
)

// translateRepoError converts the typed store sentinels into CLI-shaped
// error messages. ErrNotFound carries an id so the caller forms the final
// message; this helper handles the validation sentinels (Source / Title)
// and falls through to the original error otherwise.
func translateRepoError(err error) error {
	switch {
	case errors.Is(err, store.ErrSourceRequired):
		return fmt.Errorf("--source: %w", err)
	case errors.Is(err, store.ErrTitleRequired):
		return fmt.Errorf("title: %w", err)
	case errors.Is(err, store.ErrInvalidStatus):
		return fmt.Errorf("--status: %w", err)
	default:
		return err
	}
}

// taskRepo is the narrow read/write surface every PR-20 subcommand needs
// against the tasks table. Keeping it as an interface (rather than the
// concrete *store.TaskRepo) is the test seam: production code goes through
// productionTaskRepoFactory and opens a real SQLite handle, while tests
// inject a fake via withTaskRepoFactory.
//
// The method set is intentionally a subset of *store.TaskRepo so that
// *store.TaskRepo satisfies taskRepo automatically.
type taskRepo interface {
	Insert(ctx context.Context, t store.Task) (int64, error)
	Get(ctx context.Context, id int64) (store.Task, error)
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
}

// taskRepoFactory opens a taskRepo plus a closer that the caller must run
// when done. The factory takes the resolved configPath so it can read
// core.db_path; tests can ignore configPath entirely and return an in-
// memory fake.
type taskRepoFactory func(ctx context.Context, configPath string) (taskRepo, func() error, error)

// taskRepoFactoryHook is the package-private slot tests use via
// withTaskRepoFactory. Production callers see nil and fall through to
// productionTaskRepoFactory.
var taskRepoFactoryHook taskRepoFactory

// withTaskRepoFactory swaps in a fake factory and restores the prior hook
// on test completion, mirroring withDoctorRuntime.
func withTaskRepoFactory(t interface{ Cleanup(func()) }, f taskRepoFactory) {
	prev := taskRepoFactoryHook
	taskRepoFactoryHook = f
	t.Cleanup(func() { taskRepoFactoryHook = prev })
}

// activeTaskRepoFactory returns the test override when one is installed,
// otherwise the production implementation.
func activeTaskRepoFactory() taskRepoFactory {
	if taskRepoFactoryHook != nil {
		return taskRepoFactoryHook
	}
	return productionTaskRepoFactory
}

// productionTaskRepoFactory loads the config at configPath, resolves
// core.db_path (with `~` expansion), opens the SQLite database, and
// returns a *store.TaskRepo plus a closer that closes the DB handle.
func productionTaskRepoFactory(_ context.Context, configPath string) (taskRepo, func() error, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: %w", configPath, err)
	}
	dbPath, err := expandHome(cfg.Core.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve core.db_path %q: %w", cfg.Core.DBPath, err)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	repo := store.NewTaskRepo(db)
	return repo, db.Close, nil
}

// stdinReaderHook overrides the stdin source `marunage add --body-stdin`
// reads from. Production code leaves it nil and gets os.Stdin; tests
// substitute a strings.Reader via withStdinReader.
var stdinReaderHook io.Reader

// withStdinReader installs r as the active stdin source for the duration
// of t and restores the prior hook on cleanup.
func withStdinReader(t interface{ Cleanup(func()) }, r io.Reader) {
	prev := stdinReaderHook
	stdinReaderHook = r
	t.Cleanup(func() { stdinReaderHook = prev })
}

// activeStdinReader returns the test override if installed, otherwise
// os.Stdin.
func activeStdinReader() io.Reader {
	if stdinReaderHook != nil {
		return stdinReaderHook
	}
	return os.Stdin
}

// expandHome resolves a leading `~` or `~/` to the user's home directory.
// Other inputs are returned unchanged. Mirrors the convention the example
// config in docs/requirement.md uses (`~/.marunage/tasks.db`).
func expandHome(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
