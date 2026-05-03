package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// fakeTaskRepo is the in-memory taskRepo every PR-20 CLI test injects via
// withTaskRepoFactory. It is intentionally minimal — just enough state for
// the assertions in this file — and explicitly NOT goroutine safe beyond
// the single-mutex coarse lock; the CLI is single-threaded per process so
// this matches reality.
type fakeTaskRepo struct {
	mu     sync.Mutex
	rows   map[int64]store.Task
	nextID int64
	// listFilters records every ListFilter the SUT passed in, so tests
	// can assert filter wiring without driving a real query plan.
	listFilters []store.ListFilter
}

func newFakeTaskRepo() *fakeTaskRepo {
	return &fakeTaskRepo{rows: make(map[int64]store.Task)}
}

func (f *fakeTaskRepo) Insert(_ context.Context, t store.Task) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Source == "" {
		return 0, store.ErrSourceRequired
	}
	if t.Title == "" {
		return 0, store.ErrTitleRequired
	}
	f.nextID++
	t.ID = f.nextID
	if t.Status == "" {
		t.Status = store.StatusPending
	}
	f.rows[t.ID] = t
	return t.ID, nil
}

func (f *fakeTaskRepo) Get(_ context.Context, id int64) (store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.rows[id]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeTaskRepo) List(_ context.Context, filter store.ListFilter) ([]store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listFilters = append(f.listFilters, filter)

	out := make([]store.Task, 0, len(f.rows))
	for _, t := range f.rows {
		if len(filter.Statuses) > 0 && !contains(filter.Statuses, t.Status) {
			continue
		}
		if len(filter.Sources) > 0 && !contains(filter.Sources, t.Source) {
			continue
		}
		out = append(out, t)
	}
	// Stable order: by ID ASC. Tests that need dispatch-order specifics
	// can sort the result themselves; CLI assertions here only care
	// about set membership.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// installFakeRepo is the standard test harness: register a fresh fake as
// the active factory and return it so the test can read state back.
func installFakeRepo(t *testing.T) *fakeTaskRepo {
	t.Helper()
	repo := newFakeTaskRepo()
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return repo, func() error { return nil }, nil
	})
	return repo
}

// 1. `marunage add "buy milk"` registers a task and prints the assigned id.
func TestTaskAdd_RegistersTaskAndEchoesID(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "buy milk"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add exit=%d; stderr=%q", code, stderr.String())
	}

	if len(repo.rows) != 1 {
		t.Fatalf("repo rows = %d; want 1", len(repo.rows))
	}
	var saved store.Task
	for _, t := range repo.rows {
		saved = t
	}
	if saved.Title != "buy milk" {
		t.Errorf("saved Title = %q; want %q", saved.Title, "buy milk")
	}
	out := stdout.String()
	if !strings.Contains(out, "buy milk") {
		t.Errorf("stdout missing title; got %q", out)
	}
	if !strings.Contains(out, "#1") {
		t.Errorf("stdout missing id; got %q", out)
	}
}

// 8. --source default is "manual"
func TestTaskAdd_DefaultSourceIsManual(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "x"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add exit=%d; stderr=%q", code, stderr.String())
	}
	for _, saved := range repo.rows {
		if saved.Source != "manual" {
			t.Errorf("default Source = %q; want %q", saved.Source, "manual")
		}
	}
}

// 2. --body sets the body field
func TestTaskAdd_BodyFlagPopulatesBody(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "do thing", "--body", "with details"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add exit=%d; stderr=%q", code, stderr.String())
	}
	for _, saved := range repo.rows {
		if saved.Body != "with details" {
			t.Errorf("Body = %q; want %q", saved.Body, "with details")
		}
	}
}

// 3. --body-stdin reads stdin; we cannot easily inject stdin without
// rewiring Execute, so the stdin-reader is also overridable via a hook.
func TestTaskAdd_BodyStdinReadsInjectedReader(t *testing.T) {
	repo := installFakeRepo(t)
	withStdinReader(t, strings.NewReader("body from stdin"))

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "do thing", "--body-stdin"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add exit=%d; stderr=%q", code, stderr.String())
	}
	for _, saved := range repo.rows {
		if saved.Body != "body from stdin" {
			t.Errorf("Body = %q; want %q", saved.Body, "body from stdin")
		}
	}
}

// 4. --body and --body-stdin are mutually exclusive.
func TestTaskAdd_BodyFlagsAreMutuallyExclusive(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "do thing", "--body", "x", "--body-stdin"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for conflicting flags; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") &&
		!strings.Contains(stderr.String(), "body") {
		t.Errorf("stderr should mention conflicting body flags; got %q", stderr.String())
	}
}

// 4b. --body and --body-edit are also mutually exclusive.
func TestTaskAdd_BodyAndBodyEditAreMutuallyExclusive(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "x", "--body", "x", "--body-edit"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q", stdout.String())
	}
}

// 5. --priority is persisted.
func TestTaskAdd_PriorityFlagPersists(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "x", "--priority", "5"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add exit=%d; stderr=%q", code, stderr.String())
	}
	for _, saved := range repo.rows {
		if saved.Priority != 5 {
			t.Errorf("Priority = %d; want 5", saved.Priority)
		}
	}
}

// 6. --notes accepts a JSON string.
func TestTaskAdd_NotesAcceptsValidJSON(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "x", "--notes", `{"foo":1}`}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add exit=%d; stderr=%q", code, stderr.String())
	}
	for _, saved := range repo.rows {
		if saved.Notes != `{"foo":1}` {
			t.Errorf("Notes = %q; want %q", saved.Notes, `{"foo":1}`)
		}
	}
}

// 7. --notes rejects invalid JSON before talking to the repo.
func TestTaskAdd_NotesRejectsInvalidJSON(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "x", "--notes", "not json"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for invalid JSON")
	}
	if len(repo.rows) != 0 {
		t.Errorf("invalid notes should not insert a row; rows=%d", len(repo.rows))
	}
	if !strings.Contains(stderr.String(), "notes") {
		t.Errorf("stderr should mention notes; got %q", stderr.String())
	}
}

// --source override takes effect.
func TestTaskAdd_SourceFlagOverridesDefault(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "x", "--source", "gmail"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add exit=%d; stderr=%q", code, stderr.String())
	}
	for _, saved := range repo.rows {
		if saved.Source != "gmail" {
			t.Errorf("Source = %q; want %q", saved.Source, "gmail")
		}
	}
}

// --cwd is persisted on the row.
func TestTaskAdd_CwdFlagPersists(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "x", "--cwd", "/tmp/work"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add exit=%d; stderr=%q", code, stderr.String())
	}
	for _, saved := range repo.rows {
		if saved.CWD != "/tmp/work" {
			t.Errorf("CWD = %q; want %q", saved.CWD, "/tmp/work")
		}
	}
}

// Repo errors propagate as non-zero exit with stderr message.
func TestTaskAdd_PropagatesRepoErrors(t *testing.T) {
	repo := newFakeTaskRepo()
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return repo, func() error { return nil }, errors.New("boom")
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add", "x"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// Title positional arg is required.
func TestTaskAdd_RequiresTitleArg(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"add"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when title missing; stdout=%q", stdout.String())
	}
}
