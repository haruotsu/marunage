package web

import (
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// taskDetailView is the template-facing flattening of a store.Task plus
// its related audit entries. All fields are plain Go types (string, int,
// bool, slices of plain types) so the html/template engine does not call
// any methods and the view layer owns all formatting decisions.
type taskDetailView struct {
	// Core identity
	ID         int64
	Source     string
	ExternalID string
	// ExternalURL is kept as a plain string; the template decides
	// whether to render it as an anchor tag or plain text.
	ExternalURL string
	Title       string

	// Content
	Body  string
	Notes string

	// Status / triage
	Status         string
	JudgmentReason string
	Priority       int

	// Execution context
	LockKey string
	CWD     string
	// WS carries the cmux workspace reference (e.g. "workspace:101").
	// It is rendered as plain text — a real deeplink would require the
	// cmux CLI to be running locally, so we surface the reference string
	// and let the operator copy-paste it.
	WS string

	// Outcome
	ResultSummary string
	Reflection    string

	// Timestamps as formatted strings. Zero-value timestamps render as "--".
	CreatedAt   string
	UpdatedAt   string
	StartedAt   string
	CompletedAt string

	// AuditEntries are the audit.log lines that reference this task, in
	// chronological order. Each entry is already a plain struct so the
	// template can range over it without method calls.
	AuditEntries []AuditEntry
}

// taskDetailDisplayLayout is the human-readable display format for
// timestamps in the task detail page. Matching dashboardDisplayLayout
// keeps the two pages visually consistent.
const taskDetailDisplayLayout = dashboardDisplayLayout

// zeroTimeDisplay is the sentinel rendered when a timestamp is zero
// (i.e. the event has not happened yet — pending task has no StartedAt,
// running task has no CompletedAt, etc.).
const zeroTimeDisplay = "--"

// newTaskDetailView converts a store.Task and its audit entries into the
// template-facing view. All time.Time values are formatted to strings
// here so the template stays free of method calls.
func newTaskDetailView(task store.Task, entries []AuditEntry) taskDetailView {
	v := taskDetailView{
		ID:             task.ID,
		Source:         task.Source,
		ExternalID:     task.ExternalID,
		ExternalURL:    task.ExternalURL,
		Title:          task.Title,
		Body:           task.Body,
		Notes:          task.Notes,
		Status:         task.Status,
		JudgmentReason: task.JudgmentReason,
		Priority:       task.Priority,
		LockKey:        task.LockKey,
		CWD:            task.CWD,
		WS:             task.WS,
		ResultSummary:  task.ResultSummary,
		Reflection:     task.Reflection,
		CreatedAt:      formatDetailTime(task.CreatedAt),
		UpdatedAt:      formatDetailTime(task.UpdatedAt),
		StartedAt:      formatDetailTimeOrDash(task.StartedAt),
		CompletedAt:    formatDetailTimeOrDash(task.CompletedAt),
		AuditEntries:   entries,
	}
	return v
}

// formatDetailTime formats a time.Time using the task detail display
// layout. Zero values return an empty string (guaranteed non-zero for
// CreatedAt / UpdatedAt by the store invariant, but kept consistent).
func formatDetailTime(t time.Time) string {
	return formatDisplayTime(t)
}

// formatDetailTimeOrDash is like formatDetailTime but returns "--" for
// zero values. Used for optional timestamps (StartedAt / CompletedAt)
// whose absence carries meaning (not yet dispatched / not yet done).
func formatDetailTimeOrDash(t time.Time) string {
	if t.IsZero() {
		return zeroTimeDisplay
	}
	return formatDisplayTime(t)
}
