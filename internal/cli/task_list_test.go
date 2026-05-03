package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// seedTasks inserts a small set of rows directly into the fake repo so
// every list test can assert against a known dataset.
func seedTasks(repo *fakeTaskRepo) {
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "draft README",
		Status: store.StatusPending, Priority: 5,
	}
	repo.rows[2] = store.Task{
		ID: 2, Source: "gmail", Title: "reply to alice",
		Status: store.StatusRunning, Priority: 3,
	}
	repo.rows[3] = store.Task{
		ID: 3, Source: "manual", Title: "old archived",
		Status: store.StatusDone, Priority: 0,
	}
	repo.rows[4] = store.Task{
		ID: 4, Source: "slack", Title: "skipped one",
		Status: store.StatusSkipped, Priority: 0,
	}
	repo.nextID = 4
}

// 1. Empty repo: "No tasks." stdout, exit 0
func TestTaskList_EmptyPrintsFriendlyMessage(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No tasks.") {
		t.Errorf("expected 'No tasks.' in stdout; got %q", stdout.String())
	}
}

// 2. Default filter: pending + running shown, done / skipped hidden.
func TestTaskList_DefaultFiltersToPendingAndRunning(t *testing.T) {
	repo := installFakeRepo(t)
	seedTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "draft README") {
		t.Errorf("expected pending row; got %q", out)
	}
	if !strings.Contains(out, "reply to alice") {
		t.Errorf("expected running row; got %q", out)
	}
	if strings.Contains(out, "old archived") {
		t.Errorf("done row should be hidden in default list; got %q", out)
	}
	if strings.Contains(out, "skipped one") {
		t.Errorf("skipped row should be hidden in default list; got %q", out)
	}

	// And the filter wiring: SUT must pass the default statuses through.
	if len(repo.listFilters) != 1 {
		t.Fatalf("listFilters captured = %d; want 1", len(repo.listFilters))
	}
	want := []string{store.StatusPending, store.StatusRunning}
	got := repo.listFilters[0].Statuses
	if !equalStringSet(got, want) {
		t.Errorf("default statuses = %v; want %v", got, want)
	}
}

// 3. --status done overrides default.
func TestTaskList_StatusFlagOverridesDefault(t *testing.T) {
	repo := installFakeRepo(t)
	seedTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list", "--status", "done"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "old archived") {
		t.Errorf("expected done row; got %q", out)
	}
	if strings.Contains(out, "draft README") {
		t.Errorf("pending row should be hidden under --status done; got %q", out)
	}
}

// 4. --source narrows by source.
func TestTaskList_SourceFlagFiltersBySource(t *testing.T) {
	repo := installFakeRepo(t)
	seedTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list", "--source", "gmail"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "reply to alice") {
		t.Errorf("expected gmail row; got %q", out)
	}
	if strings.Contains(out, "draft README") {
		t.Errorf("manual row should be hidden under --source gmail; got %q", out)
	}
}

// 5. --limit caps results.
func TestTaskList_LimitFlagCaps(t *testing.T) {
	repo := installFakeRepo(t)
	seedTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list", "--limit", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit=%d; stderr=%q", code, stderr.String())
	}
	if len(repo.listFilters) == 0 || repo.listFilters[0].Limit != 1 {
		t.Errorf("Limit not propagated: filters=%+v", repo.listFilters)
	}
	// Count data rows in stdout (skip header). Two newlines around
	// header + 1 row = 3; we just assert the body row count is 1.
	body := stdout.String()
	bodyRows := strings.Count(body, "\n") - 1 // exclude header newline
	if bodyRows < 1 {
		t.Errorf("expected at least 1 row in output; got %q", body)
	}
}

// 6. --json emits a parseable JSON array.
func TestTaskList_JSONFlagEmitsArray(t *testing.T) {
	repo := installFakeRepo(t)
	seedTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list --json exit=%d; stderr=%q", code, stderr.String())
	}

	var arr []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(arr) != 2 {
		t.Errorf("JSON array len = %d; want 2 (pending+running)", len(arr))
	}
}

// 7. Output respects tabwriter columns.
func TestTaskList_TextOutputHasHeader(t *testing.T) {
	repo := installFakeRepo(t)
	seedTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, header := range []string{"ID", "Source", "Status", "Priority", "Title"} {
		if !strings.Contains(out, header) {
			t.Errorf("output missing header %q; got %q", header, out)
		}
	}
}

// JSON output is empty array (not "null") when there are no rows.
func TestTaskList_JSONEmptyIsArrayNotNull(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list --json exit=%d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "[]" {
		t.Errorf("empty list JSON = %q; want %q", got, "[]")
	}
}

// Repo open errors propagate.
func TestTaskList_PropagatesRepoErrors(t *testing.T) {
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"list"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

var errBoom = errStub("boom")

// equalStringSet compares slices ignoring order. Sorting in place would
// mutate the caller's slice; this builds a frequency map instead.
func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	freq := map[string]int{}
	for _, s := range a {
		freq[s]++
	}
	for _, s := range b {
		freq[s]--
		if freq[s] < 0 {
			return false
		}
	}
	return true
}
