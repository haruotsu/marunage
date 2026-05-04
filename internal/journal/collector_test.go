package journal

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// fakeRunner returns canned stdout/stderr for each call in order.
type fakeRunner struct {
	responses []fakeResponse
}

type fakeResponse struct {
	stdout []byte
	err    error
}

func (f *fakeRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
	if len(f.responses) == 0 {
		return nil, nil, errors.New("fakeRunner: unexpected call")
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return r.stdout, nil, r.err
}

// --- GitCollector tests ---

func TestGitCollectorName(t *testing.T) {
	t.Parallel()
	c := NewGitCollector(WithGitRunner(&fakeRunner{}))
	if c.Name() != "git" {
		t.Errorf("Name() = %q, want %q", c.Name(), "git")
	}
}

func TestGitCollectorCollectsCommits(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{responses: []fakeResponse{
		{stdout: []byte("feat: add thing\nfix: resolve bug\n")},
	}}
	c := NewGitCollector(WithGitRunner(r))

	since := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	items, err := c.Collect(context.Background(), since)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].Text != "feat: add thing" {
		t.Errorf("items[0].Text = %q, want %q", items[0].Text, "feat: add thing")
	}
	if items[1].Text != "fix: resolve bug" {
		t.Errorf("items[1].Text = %q, want %q", items[1].Text, "fix: resolve bug")
	}
}

func TestGitCollectorEmptyOutput(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("")}}}
	c := NewGitCollector(WithGitRunner(r))

	items, err := c.Collect(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(items))
	}
}

func TestGitCollectorErrorPropagated(t *testing.T) {
	t.Parallel()
	wantErr := &exec.ExitError{}
	r := &fakeRunner{responses: []fakeResponse{{err: wantErr}}}
	c := NewGitCollector(WithGitRunner(r))

	_, err := c.Collect(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- TaskCollector tests ---

type fakeTaskLister struct {
	tasks []store.Task
	err   error
}

func (f *fakeTaskLister) List(_ context.Context, _ store.ListFilter) ([]store.Task, error) {
	return f.tasks, f.err
}

func TestTaskCollectorName(t *testing.T) {
	t.Parallel()
	c := NewTaskCollector(&fakeTaskLister{})
	if c.Name() != "marunage" {
		t.Errorf("Name() = %q, want %q", c.Name(), "marunage")
	}
}

func TestTaskCollectorCollectsDoneAndFailed(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	after := since.Add(5 * time.Minute)
	before := since.Add(-5 * time.Minute)

	lister := &fakeTaskLister{tasks: []store.Task{
		{ID: 42, Title: "Fix bug", Status: store.StatusDone, CompletedAt: after},
		{ID: 43, Title: "Old task", Status: store.StatusDone, CompletedAt: before},
		{ID: 44, Title: "Failed one", Status: store.StatusFailed, CompletedAt: after},
	}}
	c := NewTaskCollector(lister)

	items, err := c.Collect(context.Background(), since)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (tasks after since)", len(items))
	}
	if !bytes.Contains([]byte(items[0].Text), []byte("#42")) {
		t.Errorf("items[0].Text = %q should contain #42", items[0].Text)
	}
	if !bytes.Contains([]byte(items[1].Text), []byte("#44")) {
		t.Errorf("items[1].Text = %q should contain #44", items[1].Text)
	}
}

func TestTaskCollectorSkipsZeroCompletedAt(t *testing.T) {
	t.Parallel()
	lister := &fakeTaskLister{tasks: []store.Task{
		{ID: 1, Title: "No completed_at", Status: store.StatusDone, CompletedAt: time.Time{}},
	}}
	c := NewTaskCollector(lister)

	items, err := c.Collect(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0 (zero CompletedAt skipped)", len(items))
	}
}

func TestTaskCollectorErrorPropagated(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("db error")
	lister := &fakeTaskLister{err: wantErr}
	c := NewTaskCollector(lister)

	_, err := c.Collect(context.Background(), time.Now())
	if !errors.Is(err, wantErr) {
		t.Errorf("Collect error = %v, want %v", err, wantErr)
	}
}

// --- GitHubCollector tests ---

func TestGitHubCollectorName(t *testing.T) {
	t.Parallel()
	c := NewGitHubCollector(WithGitHubRunner(&fakeRunner{}))
	if c.Name() != "github" {
		t.Errorf("Name() = %q, want %q", c.Name(), "github")
	}
}

func TestGitHubCollectorParsesJSON(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	prJSON := `[{"number":46,"title":"review/promote 強化","mergedAt":"2026-05-04T14:30:00Z"}]`
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte(prJSON)}}}
	c := NewGitHubCollector(WithGitHubRunner(r))

	items, err := c.Collect(context.Background(), since)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if !bytes.Contains([]byte(items[0].Text), []byte("#46")) {
		t.Errorf("items[0].Text = %q should contain #46", items[0].Text)
	}
}

func TestGitHubCollectorEmptyJSON(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{responses: []fakeResponse{{stdout: []byte("[]")}}}
	c := NewGitHubCollector(WithGitHubRunner(r))

	items, err := c.Collect(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(items))
	}
}
