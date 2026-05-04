package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// AuditEntry is one parsed line from audit.log that is relevant to a
// task. Only the fields the task detail page needs are surfaced; the
// rest of the JSON object (path, backend, name, ...) is intentionally
// omitted to keep the view layer slim.
type AuditEntry struct {
	// Time is the RFC3339Nano timestamp string from the audit record.
	Time string
	// Action is the audit action verb (e.g. "dispatch.start",
	// "dispatch.fail", "reaper.warn").
	Action string
	// TaskID is the task id extracted from the Key field ("task:NNN").
	// Zero means the entry did not carry a task reference (should not
	// appear in filtered results but is kept for safety).
	TaskID int64
	// Value is the raw value field from the audit record (ws reference,
	// failure reason, etc.). May be empty.
	Value string
}

// AuditReader is the seam the task detail handler consumes.
// Production wires FileAuditReader; tests inject a static fake.
type AuditReader interface {
	// EntriesForTask returns all audit entries in the log that reference
	// the given task id, in file order (oldest first).
	EntriesForTask(ctx context.Context, id int64) ([]AuditEntry, error)
}

// auditLine mirrors the JSON shape that logging.AuditLog.Record writes.
// Fields are decoded with omitempty-like tolerance: missing fields
// unmarshal to their zero values so old log entries do not fail.
type auditLine struct {
	Time   string `json:"time"`
	Action string `json:"action"`
	Key    string `json:"key,omitempty"`
	Value  string `json:"value,omitempty"`
}

// taskKeyPrefix is the prefix that dispatch / reaper stamp onto the Key
// field when recording a task-related event, e.g. "task:42".
const taskKeyPrefix = "task:"

// extractTaskID parses the numeric suffix from a "task:NNN" key.
// Returns (0, false) for any other key format.
func extractTaskID(key string) (int64, bool) {
	if !strings.HasPrefix(key, taskKeyPrefix) {
		return 0, false
	}
	n, err := strconv.ParseInt(key[len(taskKeyPrefix):], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// FileAuditReader reads audit.log from a fixed path and filters by task id.
// It re-opens the file on every call so it always sees the latest entries
// appended since the server started; there is no watch/tail mechanism.
type FileAuditReader struct {
	path string
}

// NewFileAuditReader returns a FileAuditReader for path. The file does
// not need to exist at construction time; a missing file at read time
// returns an empty slice rather than an error, because a fresh install
// may not have written any audit entries yet.
func NewFileAuditReader(path string) *FileAuditReader {
	return &FileAuditReader{path: path}
}

// EntriesForTask reads the audit log from disk and returns every line
// whose Key field matches "task:<id>". Context cancellation is checked
// between lines so a very large log file does not block the server's
// goroutine pool indefinitely.
func (r *FileAuditReader) EntriesForTask(ctx context.Context, id int64) ([]AuditEntry, error) {
	f, err := os.Open(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("audit reader: open %s: %w", r.path, err)
	}
	defer func() { _ = f.Close() }()

	var out []AuditEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec auditLine
		if err := json.Unmarshal(line, &rec); err != nil {
			// Malformed lines are silently skipped: a partial write
			// at the tail of the file (e.g. crash mid-line) must not
			// break the task detail page for already-completed tasks.
			continue
		}
		taskID, ok := extractTaskID(rec.Key)
		if !ok || taskID != id {
			continue
		}
		out = append(out, AuditEntry{
			Time:   rec.Time,
			Action: rec.Action,
			TaskID: taskID,
			Value:  rec.Value,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("audit reader: scan %s: %w", r.path, err)
	}
	return out, nil
}

// noopAuditReader is the fallback used when no AuditLog path is wired in
// Options. It returns an empty slice on every call.
type noopAuditReader struct{}

func (noopAuditReader) EntriesForTask(_ context.Context, _ int64) ([]AuditEntry, error) {
	return nil, nil
}
