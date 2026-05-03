// Package render builds the Markdown body that `marunage render` writes to
// ~/.marunage/view.md so the cmux markdown viewer can show every task on a
// single screen (docs/pr_split_plan.md PR-60).
//
// The renderer is a pure function: callers (the CLI) do the I/O. This keeps
// the layout testable without temp files and lets future surfaces (the Web
// UI fragment, a `marunage open` preview) reuse the same body.
package render

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// section ties a status enum value to its human-readable header. The slice
// order is the on-screen order; statuses with no rows are skipped at render
// time so an empty DB does not produce six "(0)" headers.
type section struct {
	status string
	header string
}

// sectionOrder is the canonical display order: actionable states first
// (Pending → Running → Waiting for human), then terminal ones grouped by
// resolution (Done → Failed → Skipped). Mirrors the lifecycle a reader
// scans top-to-bottom in the cmux viewer.
var sectionOrder = []section{
	{status: store.StatusPending, header: "## Pending"},
	{status: store.StatusRunning, header: "## Running"},
	{status: store.StatusWaitingHuman, header: "## Waiting for human"},
	{status: store.StatusDone, header: "## Done"},
	{status: store.StatusFailed, header: "## Failed"},
	{status: store.StatusSkipped, header: "## Skipped"},
}

// Render returns the Markdown body for tasks. generatedAt becomes the
// "_Generated: ..._" timestamp so a viewer can tell at a glance how stale
// the view is; callers pass time.Now() in production and a fixed clock in
// tests.
func Render(tasks []store.Task, generatedAt time.Time) string {
	var b strings.Builder
	b.WriteString("# marunage view\n\n")
	b.WriteString("_Generated: ")
	b.WriteString(generatedAt.UTC().Format(time.RFC3339))
	b.WriteString("_\n\n")

	if len(tasks) == 0 {
		b.WriteString("_No tasks._\n")
		return b.String()
	}

	grouped := groupByStatus(tasks)
	for _, s := range sectionOrder {
		rows := grouped[s.status]
		if len(rows) == 0 {
			continue
		}
		sortRows(rows)
		b.WriteString(s.header)
		b.WriteString("\n\n")
		for _, t := range rows {
			writeRow(&b, t)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// groupByStatus partitions tasks into a status-keyed map. Rows with an
// unknown status (not in sectionOrder) are silently dropped from the
// rendered body — the store CHECK constraint prevents writing one in the
// first place, but defensive routing keeps a future migration glitch from
// crashing the renderer.
func groupByStatus(tasks []store.Task) map[string][]store.Task {
	out := make(map[string][]store.Task, len(sectionOrder))
	for _, t := range tasks {
		out[t.Status] = append(out[t.Status], t)
	}
	return out
}

// sortRows imposes the same order store.List uses (priority DESC,
// created_at ASC, id ASC). Render is normally fed `repo.List` output
// which is already in this order, but a caller could pass a hand-built
// slice (tests, future composite sources) and the file we write should
// stay deterministic regardless.
func sortRows(rows []store.Task) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

// writeRow emits one checklist line for t. Done / failed rows show as
// "[x]" so the viewer renders a checked box; everything else shows
// "[ ]". Source is appended after an em-dash so the eye can scan
// "what is this and where did it come from" in one glance.
func writeRow(b *strings.Builder, t store.Task) {
	box := "[ ]"
	if t.Status == store.StatusDone || t.Status == store.StatusFailed {
		box = "[x]"
	}
	fmt.Fprintf(b, "- %s #%d %s — %s\n", box, t.ID, sanitizeInline(t.Title), t.Source)
	if t.Status == store.StatusWaitingHuman && t.JudgmentReason != "" {
		fmt.Fprintf(b, "  > Reason: %s\n", sanitizeInline(t.JudgmentReason))
	}
}

// sanitizeInline collapses any line break in s to a single space. The
// store does not forbid newlines in Title (a checklist source could
// legitimately carry a wrapped sentence), but a literal "\n" in the
// rendered row would float the tail outside the checklist and break
// the per-task scan. CR + LF + form-feed all map to space; consecutive
// breaks collapse so we do not leave runs of whitespace behind.
func sanitizeInline(s string) string {
	if !strings.ContainsAny(s, "\n\r\f") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\f' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = r == ' '
	}
	return strings.TrimSpace(b.String())
}
