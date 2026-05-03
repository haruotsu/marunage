package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/store"
)

// taskJSON is the JSON serialisation shape every PR-20 subcommand emits
// under --json. It is a stable wire contract: the Web UI (PR-50+) and any
// CI tooling that pipes `marunage list --json | jq` rely on these field
// names. Keep it close to store.Task but use snake_case keys so the JSON
// matches the TOML / SQL world the rest of marunage speaks.
//
// Times serialise as RFC3339 UTC strings; zero values become null so
// consumers can distinguish "not started yet" from a real timestamp.
type taskJSON struct {
	ID             int64   `json:"id"`
	Source         string  `json:"source"`
	ExternalID     string  `json:"external_id,omitempty"`
	ExternalURL    string  `json:"external_url,omitempty"`
	Title          string  `json:"title"`
	Body           string  `json:"body,omitempty"`
	Notes          string  `json:"notes,omitempty"`
	Status         string  `json:"status"`
	JudgmentReason string  `json:"judgment_reason,omitempty"`
	Priority       int     `json:"priority"`
	LockKey        string  `json:"lock_key,omitempty"`
	CWD            string  `json:"cwd,omitempty"`
	WS             string  `json:"ws,omitempty"`
	ResultSummary  string  `json:"result_summary,omitempty"`
	Reflection     string  `json:"reflection,omitempty"`
	CreatedAt      *string `json:"created_at,omitempty"`
	UpdatedAt      *string `json:"updated_at,omitempty"`
	StartedAt      *string `json:"started_at,omitempty"`
	CompletedAt    *string `json:"completed_at,omitempty"`
}

// taskFromStore projects a store.Task into the JSON shape, encoding zero
// times as null (omitempty drops them) and non-zero ones as RFC3339.
func taskFromStore(t store.Task) taskJSON {
	return taskJSON{
		ID:             t.ID,
		Source:         t.Source,
		ExternalID:     t.ExternalID,
		ExternalURL:    t.ExternalURL,
		Title:          t.Title,
		Body:           t.Body,
		Notes:          t.Notes,
		Status:         t.Status,
		JudgmentReason: t.JudgmentReason,
		Priority:       t.Priority,
		LockKey:        t.LockKey,
		CWD:            t.CWD,
		WS:             t.WS,
		ResultSummary:  t.ResultSummary,
		Reflection:     t.Reflection,
		CreatedAt:      timePtr(t.CreatedAt),
		UpdatedAt:      timePtr(t.UpdatedAt),
		StartedAt:      timePtr(t.StartedAt),
		CompletedAt:    timePtr(t.CompletedAt),
	}
}

// timePtr returns nil for zero times so omitempty drops the field in
// JSON; non-zero times become RFC3339 UTC strings.
func timePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

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
