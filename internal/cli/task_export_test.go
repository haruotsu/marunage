package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// 1. Empty repo + default flags emits the JSON empty-array sentinel "[]".
// This pins two contracts at once: the default --format is "json", and the
// empty-result encoding is "[]" (not "null") so consumers can rely on
// `len(arr)` without a nil check, mirroring `marunage list --json`.
func TestTaskExport_JSONEmptyIsEmptyArrayNotNull(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export exit=%d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "[]" {
		t.Errorf("empty export = %q; want %q", got, "[]")
	}
}

// 2. Default --format is "json" and the output is a parseable JSON array
// of taskJSON-shaped objects. Pinning the wire shape AND the values
// (id, title, source, status) keeps export aligned with `list --json` /
// `show --json` and catches a regression where taskFromStore is replaced
// by a zero-value taskJSON{} (key-presence-only assertions would still
// pass against that mutation).
func TestTaskExport_JSONFormatEmitsTaskJSONArray(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "first",
		Status: store.StatusPending, Priority: 5,
	}
	repo.rows[2] = store.Task{
		ID: 2, Source: "gmail", Title: "second",
		Status: store.StatusDone,
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export exit=%d; stderr=%q", code, stderr.String())
	}

	var arr []struct {
		ID     int64  `json:"id"`
		Source string `json:"source"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(arr) != 2 {
		t.Fatalf("export array len = %d; want 2", len(arr))
	}
	byID := map[int64]struct {
		Source, Title, Status string
	}{
		arr[0].ID: {arr[0].Source, arr[0].Title, arr[0].Status},
		arr[1].ID: {arr[1].Source, arr[1].Title, arr[1].Status},
	}
	if got := byID[1]; got.Source != "manual" || got.Title != "first" || got.Status != store.StatusPending {
		t.Errorf("row id=1 = %+v; want {manual first pending}", got)
	}
	if got := byID[2]; got.Source != "gmail" || got.Title != "second" || got.Status != store.StatusDone {
		t.Errorf("row id=2 = %+v; want {gmail second done}", got)
	}
}

// 3. By default export covers ALL statuses (unlike `list`, which defaults
// to pending+running). Archive use cases need the full history in one
// pass.
func TestTaskExport_DefaultIncludesAllStatuses(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "p", Status: store.StatusPending}
	repo.rows[2] = store.Task{ID: 2, Source: "manual", Title: "r", Status: store.StatusRunning}
	repo.rows[3] = store.Task{ID: 3, Source: "manual", Title: "d", Status: store.StatusDone}
	repo.rows[4] = store.Task{ID: 4, Source: "manual", Title: "f", Status: store.StatusFailed}
	repo.rows[5] = store.Task{ID: 5, Source: "manual", Title: "s", Status: store.StatusSkipped}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export exit=%d; stderr=%q", code, stderr.String())
	}

	var arr []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(arr) != 5 {
		t.Errorf("default export array len = %d; want 5 (all statuses)", len(arr))
	}
	// And the wiring: SUT must NOT pass a default Statuses or Sources
	// filter so the store-level scan returns every row. Pinning Sources
	// too catches a "Sources defaulting to [\"manual\"]" regression that
	// the all-manual test fixture would otherwise hide.
	if len(repo.listFilters) != 1 {
		t.Fatalf("listFilters captured = %d; want 1", len(repo.listFilters))
	}
	if len(repo.listFilters[0].Statuses) != 0 {
		t.Errorf("default Statuses filter = %v; want empty (no filter)",
			repo.listFilters[0].Statuses)
	}
	if len(repo.listFilters[0].Sources) != 0 {
		t.Errorf("default Sources filter = %v; want empty (no filter)",
			repo.listFilters[0].Sources)
	}
}

// 4. --format markdown renders a human-readable section per task.
// Pinning the structural markers (`## #42` H2 header, `- source:` /
// `- status:` / `- priority:` list items) catches a regression where
// the renderer drops to plain text or downshifts heading levels — the
// loose substring assertions alone would still pass against either.
func TestTaskExport_MarkdownFormatRendersHumanReadable(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[42] = store.Task{
		ID: 42, Source: "gmail", Title: "review PDF",
		Status: store.StatusDone, Body: "the contract",
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export", "--format", "markdown"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export markdown exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"## #42 review PDF",
		"- source: gmail",
		"- status: done",
		"- priority: 0",
		"the contract",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown output missing %q; got %q", want, out)
		}
	}
}

// 4b. --status accepts repeated occurrences (cobra StringSliceVar
// semantics). Pinning this keeps a future flag-redeclaration to
// StringVar from silently dropping all but the last value.
func TestTaskExport_StatusFlagAcceptsRepeated(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "p", Status: store.StatusPending}
	repo.rows[2] = store.Task{ID: 2, Source: "manual", Title: "d", Status: store.StatusDone}
	repo.rows[3] = store.Task{ID: 3, Source: "manual", Title: "f", Status: store.StatusFailed}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export", "--status", "pending", "--status", "done"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export --status pending --status done exit=%d; stderr=%q", code, stderr.String())
	}
	var arr []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(arr) != 2 {
		t.Errorf("array len = %d; want 2 (pending+done, NOT failed)", len(arr))
	}
}

// 5. --format markdown on an empty repo emits a friendly "No tasks." line
// instead of a blank document. Mirrors `list`'s empty-case behaviour so
// the operator can tell a working command from one that exited silently.
func TestTaskExport_MarkdownEmptyShowsFriendlyMessage(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export", "--format", "markdown"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export markdown exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No tasks.") {
		t.Errorf("expected 'No tasks.' in markdown empty output; got %q", stdout.String())
	}
}

// 6. --status narrows the result the same way `list --status` does.
func TestTaskExport_StatusFlagFiltersResults(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "p", Status: store.StatusPending}
	repo.rows[2] = store.Task{ID: 2, Source: "manual", Title: "d", Status: store.StatusDone}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export", "--status", "done"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export --status done exit=%d; stderr=%q", code, stderr.String())
	}
	var arr []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(arr) != 1 || arr[0]["title"].(string) != "d" {
		t.Errorf("--status done arr = %+v; want one row with title=d", arr)
	}
}

// 7. --source narrows by source.
func TestTaskExport_SourceFlagFiltersResults(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "m", Status: store.StatusPending}
	repo.rows[2] = store.Task{ID: 2, Source: "gmail", Title: "g", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export", "--source", "gmail"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export --source gmail exit=%d; stderr=%q", code, stderr.String())
	}
	var arr []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(arr) != 1 || arr[0]["title"].(string) != "g" {
		t.Errorf("--source gmail arr = %+v; want one row with title=g", arr)
	}
}

// 8. Unknown --format value is rejected with a helpful diagnostic naming
// the flag and the accepted values, before any DB call. Pinning both the
// flag name and the allowed-list keeps a future "factor out the format
// switch" rewrite from accidentally degrading the message to a vague
// "unsupported format" line.
func TestTaskExport_InvalidFormatRejected(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export", "--format", "yaml"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "format") {
		t.Errorf("stderr should mention --format; got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "json") || !strings.Contains(stderr.String(), "markdown") {
		t.Errorf("stderr should list accepted formats (json, markdown); got %q", stderr.String())
	}
}

// 9. Repo open errors propagate as non-zero exit.
func TestTaskExport_PropagatesRepoErrors(t *testing.T) {
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"export"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
