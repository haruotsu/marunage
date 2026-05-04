package web

import (
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// TestTaskDetailView_AllFields: newTaskDetailView() maps all Task fields correctly.
func TestTaskDetailView_AllFields(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	task := store.Task{
		ID:             42,
		Source:         "markdown",
		ExternalID:     "ext-001",
		ExternalURL:    "https://example.com/task/1",
		Title:          "Sample Task Title",
		Body:           "Task body content.",
		Notes:          `{"hint": "Some notes."}`,
		Status:         store.StatusDone,
		JudgmentReason: "looked good",
		Priority:       5,
		LockKey:        "lock-001",
		CWD:            "/tmp/work",
		WS:             "workspace:101",
		ResultSummary:  "Completed successfully.",
		Reflection:     "Good approach.",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-30 * time.Minute),
		StartedAt:      now.Add(-time.Hour),
		CompletedAt:    now.Add(-10 * time.Minute),
	}
	entries := []AuditEntry{
		{Time: "2026-05-04T11:00:00Z", Action: "dispatch.start", TaskID: 42, Value: "workspace:101"},
	}

	v := newTaskDetailView(task, entries)

	// All plain string / int fields must be set
	if v.ID != 42 {
		t.Errorf("ID = %d; want 42", v.ID)
	}
	if v.Source != "markdown" {
		t.Errorf("Source = %q; want markdown", v.Source)
	}
	if v.ExternalID != "ext-001" {
		t.Errorf("ExternalID = %q; want ext-001", v.ExternalID)
	}
	if v.ExternalURL != "https://example.com/task/1" {
		t.Errorf("ExternalURL = %q; want https://example.com/task/1", v.ExternalURL)
	}
	if v.Title != "Sample Task Title" {
		t.Errorf("Title = %q; want Sample Task Title", v.Title)
	}
	if v.Body != "Task body content." {
		t.Errorf("Body = %q; want Task body content.", v.Body)
	}
	if v.Notes != `{"hint": "Some notes."}` {
		t.Errorf("Notes = %q; want JSON notes string", v.Notes)
	}
	if v.Status != "done" {
		t.Errorf("Status = %q; want done", v.Status)
	}
	if v.JudgmentReason != "looked good" {
		t.Errorf("JudgmentReason = %q; want looked good", v.JudgmentReason)
	}
	if v.Priority != 5 {
		t.Errorf("Priority = %d; want 5", v.Priority)
	}
	if v.LockKey != "lock-001" {
		t.Errorf("LockKey = %q; want lock-001", v.LockKey)
	}
	if v.CWD != "/tmp/work" {
		t.Errorf("CWD = %q; want /tmp/work", v.CWD)
	}
	if v.WS != "workspace:101" {
		t.Errorf("WS = %q; want workspace:101", v.WS)
	}
	if v.ResultSummary != "Completed successfully." {
		t.Errorf("ResultSummary = %q; want Completed successfully.", v.ResultSummary)
	}
	if v.Reflection != "Good approach." {
		t.Errorf("Reflection = %q; want Good approach.", v.Reflection)
	}

	// Timestamps must be formatted as strings, not zero
	if v.CreatedAt == "" {
		t.Errorf("CreatedAt is empty; want formatted timestamp")
	}
	if v.UpdatedAt == "" {
		t.Errorf("UpdatedAt is empty; want formatted timestamp")
	}
	if v.StartedAt == "" {
		t.Errorf("StartedAt is empty; want formatted timestamp")
	}
	if v.CompletedAt == "" {
		t.Errorf("CompletedAt is empty; want formatted timestamp")
	}

	// Audit entries must be mapped
	if len(v.AuditEntries) != 1 {
		t.Fatalf("AuditEntries len = %d; want 1", len(v.AuditEntries))
	}
	if v.AuditEntries[0].Action != "dispatch.start" {
		t.Errorf("AuditEntries[0].Action = %q; want dispatch.start", v.AuditEntries[0].Action)
	}
}

// TestTaskDetailView_ZeroTimestamps: zero-value timestamps render as "--".
func TestTaskDetailView_ZeroTimestamps(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	task := store.Task{
		ID:        1,
		Source:    "markdown",
		Title:     "No timestamps",
		Status:    store.StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
		// StartedAt and CompletedAt are zero
	}
	v := newTaskDetailView(task, nil)

	if v.StartedAt != "--" {
		t.Errorf("StartedAt = %q; want -- for zero time", v.StartedAt)
	}
	if v.CompletedAt != "--" {
		t.Errorf("CompletedAt = %q; want -- for zero time", v.CompletedAt)
	}
}
