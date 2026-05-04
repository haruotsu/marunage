package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestAuditReader_FilterByTaskID: FileAuditReader filters JSON lines by task_id field.
func TestAuditReader_FilterByTaskID(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.log")

	// Write lines in the format the dispatch writes (key: "task:NNN")
	lines := `{"time":"2026-05-04T10:00:00Z","action":"dispatch.start","key":"task:42","value":"workspace:101"}
{"time":"2026-05-04T10:01:00Z","action":"dispatch.start","key":"task:99","value":"workspace:200"}
{"time":"2026-05-04T10:02:00Z","action":"dispatch.fail","key":"task:42","value":"some error"}
{"time":"2026-05-04T10:03:00Z","action":"config.save","path":"/etc/conf"}
{"time":"2026-05-04T10:04:00Z","action":"reaper.warn","key":"task:42","value":"stuck-warn"}
`
	if err := os.WriteFile(auditPath, []byte(lines), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reader := NewFileAuditReader(auditPath)
	entries, err := reader.EntriesForTask(context.Background(), 42)
	if err != nil {
		t.Fatalf("EntriesForTask: %v", err)
	}

	// Should get 3 entries: dispatch.start for task:42, dispatch.fail for task:42, reaper.warn for task:42
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d; want 3\nentries: %+v", len(entries), entries)
	}

	// First entry: dispatch.start
	if entries[0].Action != "dispatch.start" {
		t.Errorf("entries[0].Action = %q; want dispatch.start", entries[0].Action)
	}
	if entries[0].TaskID != 42 {
		t.Errorf("entries[0].TaskID = %d; want 42", entries[0].TaskID)
	}
	if entries[0].Time != "2026-05-04T10:00:00Z" {
		t.Errorf("entries[0].Time = %q; want 2026-05-04T10:00:00Z", entries[0].Time)
	}
	if entries[0].Value != "workspace:101" {
		t.Errorf("entries[0].Value = %q; want workspace:101", entries[0].Value)
	}

	// Second entry: dispatch.fail
	if entries[1].Action != "dispatch.fail" {
		t.Errorf("entries[1].Action = %q; want dispatch.fail", entries[1].Action)
	}

	// Third entry: reaper.warn
	if entries[2].Action != "reaper.warn" {
		t.Errorf("entries[2].Action = %q; want reaper.warn", entries[2].Action)
	}
}

// TestAuditReader_EmptyFile: empty log file returns empty slice (no error).
func TestAuditReader_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(auditPath, []byte(""), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reader := NewFileAuditReader(auditPath)
	entries, err := reader.EntriesForTask(context.Background(), 1)
	if err != nil {
		t.Fatalf("EntriesForTask: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v; want empty", entries)
	}
}

// TestAuditReader_MissingFile: missing file returns empty slice (no error).
// The audit log may not exist on a fresh install.
func TestAuditReader_MissingFile(t *testing.T) {
	reader := NewFileAuditReader("/nonexistent/audit.log")
	entries, err := reader.EntriesForTask(context.Background(), 1)
	if err != nil {
		t.Fatalf("EntriesForTask on missing file should not return error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v; want empty", entries)
	}
}

// TestAuditReader_NoMatchingTaskID: log has entries but none for requested id.
func TestAuditReader_NoMatchingTaskID(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.log")
	lines := `{"time":"2026-05-04T10:00:00Z","action":"dispatch.start","key":"task:99","value":"workspace:200"}
`
	if err := os.WriteFile(auditPath, []byte(lines), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reader := NewFileAuditReader(auditPath)
	entries, err := reader.EntriesForTask(context.Background(), 42)
	if err != nil {
		t.Fatalf("EntriesForTask: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v; want empty for non-matching task", entries)
	}
}
