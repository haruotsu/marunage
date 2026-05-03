package render

import (
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// fixedClock is the canonical generated-at timestamp every render test
// uses so the asserted output stays byte-stable across runs.
var fixedClock = time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)

// R1: An empty task list still produces a header, a generated-at line,
// and a "_No tasks._" placeholder so a fresh DB does not look like a
// failed render in the cmux markdown viewer.
func TestRender_EmptyShowsPlaceholder(t *testing.T) {
	got := Render(nil, fixedClock)

	for _, want := range []string{"# marunage view", "_No tasks._"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render(nil) missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// R2: A single pending task lands in a "## Pending" section as a
// canonical Markdown checklist line so the cmux viewer renders a
// real checkbox rather than literal text.
func TestRender_SinglePendingRendersChecklistRow(t *testing.T) {
	tasks := []store.Task{
		{ID: 1, Source: "manual", Title: "draft README", Status: store.StatusPending, Priority: 5},
	}

	got := Render(tasks, fixedClock)

	for _, want := range []string{"## Pending", "- [ ] #1", "draft README", "manual"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render(single pending) missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// R3: Sections appear in the canonical lifecycle order (Pending →
// Running → Waiting for human → Done → Failed → Skipped). Empty
// statuses are omitted entirely so a viewer is not buried under
// "## Skipped (0)" boilerplate.
func TestRender_SectionsAppearInLifecycleOrderEmptiesOmitted(t *testing.T) {
	tasks := []store.Task{
		{ID: 1, Source: "manual", Title: "p", Status: store.StatusPending},
		{ID: 2, Source: "gmail", Title: "r", Status: store.StatusRunning},
		{ID: 3, Source: "slack", Title: "w", Status: store.StatusWaitingHuman, JudgmentReason: "needs approval"},
		{ID: 4, Source: "manual", Title: "d", Status: store.StatusDone},
	}

	got := Render(tasks, fixedClock)

	wantOrder := []string{"## Pending", "## Running", "## Waiting for human", "## Done"}
	prev := -1
	for _, h := range wantOrder {
		idx := strings.Index(got, h)
		if idx == -1 {
			t.Errorf("missing section %q\n--- got ---\n%s", h, got)
			continue
		}
		if idx <= prev {
			t.Errorf("section %q appears before previous one (idx=%d, prev=%d)\n%s", h, idx, prev, got)
		}
		prev = idx
	}

	// Empty statuses must NOT appear as a header at all.
	for _, h := range []string{"## Failed", "## Skipped"} {
		if strings.Contains(got, h) {
			t.Errorf("empty status header %q should be omitted\n--- got ---\n%s", h, got)
		}
	}
}

// R8: A waiting_human row carries the judgment_reason on a quoted
// follow-up line ("> Reason: ..."). The whole point of view.md is to
// surface "what needs a human's attention right now"; hiding the
// reason would force the operator to bounce to `marunage show` for
// every escalation.
func TestRender_WaitingHumanShowsJudgmentReason(t *testing.T) {
	tasks := []store.Task{
		{
			ID:             7,
			Source:         "slack",
			Title:          "confirm payment",
			Status:         store.StatusWaitingHuman,
			JudgmentReason: "needs approval from billing",
		},
	}

	got := Render(tasks, fixedClock)

	if !strings.Contains(got, "> Reason: needs approval from billing") {
		t.Errorf("waiting_human row should quote the reason\n--- got ---\n%s", got)
	}
}

// R8b: An empty judgment_reason (the row was escalated by code that
// forgot to set one — never normal, but defensive) must NOT emit a
// bare "> Reason: " line; that looks like a bug to a human reader.
func TestRender_WaitingHumanWithoutReasonOmitsQuote(t *testing.T) {
	tasks := []store.Task{
		{ID: 8, Source: "slack", Title: "no reason", Status: store.StatusWaitingHuman},
	}

	got := Render(tasks, fixedClock)

	if strings.Contains(got, "> Reason:") {
		t.Errorf("empty reason must not produce a '> Reason:' line\n--- got ---\n%s", got)
	}
}

// R7: A title containing characters that are syntactic in Markdown
// (pipe, brackets, backticks, embedded newlines) must not break the
// surrounding layout — the section header that follows still renders,
// the row count for that section is correct, and no Render() call
// panics. This is the minimum safety bar before we worry about full
// escaping (which the cmux viewer treats as plain text anyway).
func TestRender_TitleWithMarkdownMetacharsStaysSafe(t *testing.T) {
	tasks := []store.Task{
		{ID: 1, Source: "manual", Title: "weird | [title] `boom`\nsecond line", Status: store.StatusPending},
		{ID: 2, Source: "manual", Title: "after weird", Status: store.StatusRunning},
	}

	got := Render(tasks, fixedClock)

	if !strings.Contains(got, "## Pending") {
		t.Errorf("Pending header missing\n--- got ---\n%s", got)
	}
	if !strings.Contains(got, "## Running") {
		t.Errorf("Running header missing — embedded newline likely leaked into next section\n--- got ---\n%s", got)
	}
	if !strings.Contains(got, "after weird") {
		t.Errorf("running row missing — surrounding layout corrupted\n--- got ---\n%s", got)
	}
	// Embedded newlines in titles must be sanitised so the title's tail
	// cannot end up on a line that isn't a checklist row — otherwise
	// "second line — manual" floats outside any task and the viewer's
	// per-task scan breaks.
	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "second line") && !strings.HasPrefix(trimmed, "- [") {
			t.Errorf("embedded newline leaked outside a checklist row: %q\n--- got ---\n%s", trimmed, got)
		}
	}
}

// R6: generatedAt becomes a single "_Generated: <RFC3339 UTC>_" line.
// The format is the contract a future "view.md is older than N minutes"
// detector (and the human reading the file) can depend on.
func TestRender_GeneratedAtFormatIsRFC3339UTC(t *testing.T) {
	got := Render(nil, fixedClock)
	want := "_Generated: 2026-05-03T12:00:00Z_"
	if !strings.Contains(got, want) {
		t.Errorf("expected exact line %q\n--- got ---\n%s", want, got)
	}

	// Non-UTC input must still be normalised to Z so the file does not
	// vary by the dispatcher's local timezone.
	jst := time.FixedZone("JST", 9*60*60)
	gotLocal := Render(nil, time.Date(2026, 5, 3, 21, 0, 0, 0, jst))
	if !strings.Contains(gotLocal, want) {
		t.Errorf("non-UTC input not normalised; want %q in:\n%s", want, gotLocal)
	}
}

// R5: Done and failed tasks render with a checked box `[x]`; everything
// else (pending / running / waiting_human / skipped) stays `[ ]`. The
// distinction is what makes the cmux viewer visually scannable: terminal-
// resolved rows look settled, in-flight rows look open.
func TestRender_CheckboxStateReflectsCompletion(t *testing.T) {
	tasks := []store.Task{
		{ID: 1, Source: "manual", Title: "p", Status: store.StatusPending},
		{ID: 2, Source: "manual", Title: "r", Status: store.StatusRunning},
		{ID: 3, Source: "manual", Title: "w", Status: store.StatusWaitingHuman, JudgmentReason: "x"},
		{ID: 4, Source: "manual", Title: "d", Status: store.StatusDone},
		{ID: 5, Source: "manual", Title: "f", Status: store.StatusFailed},
		{ID: 6, Source: "manual", Title: "s", Status: store.StatusSkipped},
	}

	got := Render(tasks, fixedClock)

	wantChecked := []string{"- [x] #4", "- [x] #5"}
	wantUnchecked := []string{"- [ ] #1", "- [ ] #2", "- [ ] #3", "- [ ] #6"}
	for _, w := range wantChecked {
		if !strings.Contains(got, w) {
			t.Errorf("expected checked row %q\n--- got ---\n%s", w, got)
		}
	}
	for _, w := range wantUnchecked {
		if !strings.Contains(got, w) {
			t.Errorf("expected unchecked row %q\n--- got ---\n%s", w, got)
		}
	}
}

// R4: Within one section rows sort by `priority DESC, created_at ASC,
// id ASC` — the same order store.List uses so view.md and `marunage
// list` agree on what the dispatcher would pick next.
func TestRender_WithinSectionOrdersByPriorityCreatedAtID(t *testing.T) {
	earlier := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	later := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	// Intentionally feed in shuffled order: Render must impose order, not rely
	// on the caller having pre-sorted.
	tasks := []store.Task{
		{ID: 3, Source: "manual", Title: "low", Status: store.StatusPending, Priority: 1, CreatedAt: earlier},
		{ID: 1, Source: "manual", Title: "high-early", Status: store.StatusPending, Priority: 5, CreatedAt: earlier},
		{ID: 2, Source: "manual", Title: "high-late", Status: store.StatusPending, Priority: 5, CreatedAt: later},
	}

	got := Render(tasks, fixedClock)

	// Locate each title in the output and assert relative order:
	// high-early < high-late < low
	wantOrder := []string{"high-early", "high-late", "low"}
	prev := -1
	for _, w := range wantOrder {
		idx := strings.Index(got, w)
		if idx == -1 {
			t.Errorf("missing %q\n--- got ---\n%s", w, got)
			continue
		}
		if idx <= prev {
			t.Errorf("ordering broken: %q appeared at %d but previous was at %d\n--- got ---\n%s", w, idx, prev, got)
		}
		prev = idx
	}
}
