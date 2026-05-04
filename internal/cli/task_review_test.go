package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// seedReviewTasks populates the fake repo with a mix of skipped and non-skipped
// rows. The skipped rows have distinct JudgmentReason values so the
// --report flag's frequency detection can be verified.
func seedReviewTasks(repo *fakeTaskRepo) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.rows[10] = store.Task{
		ID: 10, Source: "slack", Title: "old skipped",
		Status:         store.StatusSkipped,
		JudgmentReason: "rule 4: FYI broadcast",
		CreatedAt:      base,
	}
	repo.rows[11] = store.Task{
		ID: 11, Source: "gmail", Title: "new skipped 1",
		Status:         store.StatusSkipped,
		JudgmentReason: "rule 4: FYI broadcast",
		CreatedAt:      base.Add(10 * 24 * time.Hour),
	}
	repo.rows[12] = store.Task{
		ID: 12, Source: "gmail", Title: "new skipped 2",
		Status:         store.StatusSkipped,
		JudgmentReason: "rule 2: addressed to others",
		CreatedAt:      base.Add(11 * 24 * time.Hour),
	}
	repo.rows[13] = store.Task{
		ID: 13, Source: "manual", Title: "pending task",
		Status:    store.StatusPending,
		CreatedAt: base.Add(5 * 24 * time.Hour),
	}
	repo.nextID = 13
}

// 1. review returns exit 0 and lists skipped rows.
func TestTaskReview_ShowsSkippedTasks(t *testing.T) {
	repo := installFakeRepo(t)
	seedReviewTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"review"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review exit=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "old skipped") {
		t.Errorf("expected 'old skipped' in output; got %q", out)
	}
	if !strings.Contains(out, "new skipped 1") {
		t.Errorf("expected 'new skipped 1' in output; got %q", out)
	}
	// pending tasks must not appear
	if strings.Contains(out, "pending task") {
		t.Errorf("pending task must not appear in review output; got %q", out)
	}
}

// 2. --since 7d restricts to rows created in the last 7 days from the
// reference clock. In the fake, the "old skipped" row is at base (day 0)
// and the new ones are at day 10 and 11. With --since 9d from day 12 the
// threshold is day 3 — only the day-10 and day-11 rows qualify.
//
// The test injects a fixed "now" via an env-var-style knob (reviewNowHook)
// so the threshold calculation is deterministic.
func TestTaskReview_SinceFiltersOldRows(t *testing.T) {
	repo := installFakeRepo(t)
	seedReviewTasks(repo)

	// day 12 = base + 12 days → --since 9d threshold = base + 3 days
	// only rows at day 10 and 11 qualify
	fixedNow := time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC) // base + 12
	withReviewNow(t, fixedNow)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"review", "--since", "9d"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review exit=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "old skipped") {
		t.Errorf("old skipped must be filtered out; got %q", out)
	}
	if !strings.Contains(out, "new skipped 1") {
		t.Errorf("new skipped 1 must appear; got %q", out)
	}
	if !strings.Contains(out, "new skipped 2") {
		t.Errorf("new skipped 2 must appear; got %q", out)
	}
}

// 3. --json emits a JSON array.
func TestTaskReview_JSONOutput(t *testing.T) {
	repo := installFakeRepo(t)
	seedReviewTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"review", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review --json exit=%d stderr=%q", code, stderr.String())
	}
	var arr []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("JSON decode: %v\nout=%q", err, stdout.String())
	}
	if len(arr) == 0 {
		t.Errorf("expected non-empty JSON array")
	}
	for _, item := range arr {
		if item["status"] != "skipped" {
			t.Errorf("non-skipped row in JSON output: %v", item)
		}
	}
}

// 4. No skipped rows → friendly message.
func TestTaskReview_EmptyPrintsFriendlyMessage(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"review"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No skipped") {
		t.Errorf("expected 'No skipped' message; got %q", stdout.String())
	}
}

// 5. --report includes a frequency section for recurring judgment_reason
// patterns.
func TestTaskReview_ReportShowsFrequencySection(t *testing.T) {
	repo := installFakeRepo(t)
	seedReviewTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"review", "--report"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review --report exit=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	// "rule 4: FYI broadcast" appears twice; must be flagged in the report.
	if !strings.Contains(out, "FYI broadcast") {
		t.Errorf("expected 'FYI broadcast' in report; got %q", out)
	}
	if !strings.Contains(out, "2") {
		t.Errorf("expected occurrence count '2' in report; got %q", out)
	}
}

// 6. List is called with status=skipped so non-skipped rows never reach
// the reviewer; the filter wiring is observable via listFilters.
func TestTaskReview_FilterPassesSkippedStatus(t *testing.T) {
	repo := installFakeRepo(t)

	var stdout bytes.Buffer
	Execute([]string{"review"}, &stdout, &bytes.Buffer{})

	if len(repo.listFilters) == 0 {
		t.Fatal("List was not called")
	}
	f := repo.listFilters[0]
	if !contains(f.Statuses, store.StatusSkipped) {
		t.Errorf("List filter Statuses = %v; want [skipped]", f.Statuses)
	}
}
