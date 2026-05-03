package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// 1. show <id> on an existing row prints every populated field as
// "key: value" lines.
func TestTaskShow_PrintsAllFields(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[42] = store.Task{
		ID:        42,
		Source:    "gmail",
		Title:     "review PDF",
		Body:      "the contract",
		Status:    store.StatusRunning,
		Priority:  3,
		CWD:       "/tmp/work",
		CreatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 3, 12, 1, 0, 0, time.UTC),
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"show", "42"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("show exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"42", "gmail", "review PDF", "running", "the contract", "/tmp/work"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got %q", want, out)
		}
	}
}

// 2. Missing id -> exit 1, stderr message references the id.
//
// Pinning extra: cobra would normally prepend "Error: " to a non-nil RunE
// error. show.go silences errors and prints the friendly line itself so the
// stderr is a single human sentence; this test makes sure the silencing
// stays in place (a future refactor that drops cmd.SilenceErrors=true would
// double-print as "Task #999 not found.\nError: show: task not found").
func TestTaskShow_NotFoundExit1(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"show", "999"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("not-found exit=%d; want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "999") {
		t.Errorf("stderr should mention id; got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("stderr should say 'not found'; got %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "Error:") {
		t.Errorf("stderr should not contain cobra's 'Error:' prefix; got %q", stderr.String())
	}
}

// 3. Non-numeric id -> non-zero exit, helpful stderr.
func TestTaskShow_NonNumericIDFails(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"show", "abc"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "abc") {
		t.Errorf("stderr should reference the bad id; got %q", stderr.String())
	}
}

// 4. --json emits a parseable JSON object containing the task fields.
func TestTaskShow_JSONFlag(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[7] = store.Task{
		ID:     7,
		Source: "manual",
		Title:  "json task",
		Status: store.StatusPending,
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"show", "7", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("show --json exit=%d; stderr=%q", code, stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if got["id"].(float64) != 7 {
		t.Errorf("id = %v; want 7", got["id"])
	}
	if got["title"].(string) != "json task" {
		t.Errorf("title = %v; want 'json task'", got["title"])
	}
}

// 5. Empty fields render as "(empty)" in text mode so users can see them
// at a glance instead of a bare "key: " line.
func TestTaskShow_EmptyFieldsShowAsEmptyMarker(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID:     1,
		Source: "manual",
		Title:  "minimal",
		Status: store.StatusPending,
		// Body, Notes, CWD intentionally empty.
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"show", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("show exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "(empty)") {
		t.Errorf("expected '(empty)' marker for unset fields; got %q", out)
	}
}

// Repo error propagates.
func TestTaskShow_PropagatesRepoErrors(t *testing.T) {
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"show", "1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// Missing positional -> non-zero (cobra-level enforcement).
func TestTaskShow_RequiresIDArg(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"show"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when id missing")
	}
}
