package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// staticTaskDetailProvider is a fixed provider for handler tests.
type staticTaskDetailProvider struct {
	task store.Task
	err  error
}

func (s staticTaskDetailProvider) TaskDetail(_ context.Context, _ int64) (store.Task, error) {
	return s.task, s.err
}

// staticAuditReader is a fixed audit reader for handler tests.
type staticAuditReader struct {
	entries []AuditEntry
	err     error
}

func (s staticAuditReader) EntriesForTask(_ context.Context, _ int64) ([]AuditEntry, error) {
	return s.entries, s.err
}

func newTaskDetailServer(t *testing.T, prov TaskDetailProvider, reader AuditReader) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		TaskDetail:        prov,
		AuditLog:          reader,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func sampleTask() store.Task {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	return store.Task{
		ID:             42,
		Source:         "markdown",
		ExternalID:     "ext-001",
		ExternalURL:    "https://example.com/task/1",
		Title:          "Sample Task Title",
		Body:           "Task body content here.",
		Notes:          `{"hint": "Some notes about the task."}`,
		Status:         store.StatusDone,
		JudgmentReason: "looked good",
		Priority:       5,
		LockKey:        "lock-001",
		CWD:            "/tmp/work",
		WS:             "workspace:101",
		ResultSummary:  "Completed successfully.",
		Reflection:     "Good approach used.",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-30 * time.Minute),
		StartedAt:      now.Add(-time.Hour),
		CompletedAt:    now.Add(-10 * time.Minute),
	}
}

// TestTaskDetailHandler_Found: task ID exists -> 200 with task data rendered.
func TestTaskDetailHandler_Found(t *testing.T) {
	task := sampleTask()
	prov := staticTaskDetailProvider{task: task}
	reader := staticAuditReader{
		entries: []AuditEntry{
			{Time: "2026-05-04T12:00:00Z", Action: "dispatch.start", TaskID: 42},
		},
	}
	srv := newTaskDetailServer(t, prov, reader)

	req := httptest.NewRequest(http.MethodGet, "/tasks/42", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Sample Task Title",
		"Task body content here.",
		"Some notes about the task.",
		"markdown",
		"ext-001",
		"https://example.com/task/1",
		"done",
		"looked good",
		"workspace:101",
		"Completed successfully.",
		"Good approach used.",
		"dispatch.start",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("task detail body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestTaskDetailHandler_NotFound: non-existent ID -> 404.
func TestTaskDetailHandler_NotFound(t *testing.T) {
	prov := staticTaskDetailProvider{err: store.ErrNotFound}
	reader := staticAuditReader{}
	srv := newTaskDetailServer(t, prov, reader)

	req := httptest.NewRequest(http.MethodGet, "/tasks/999", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

// TestTaskDetailHandler_InvalidID: non-numeric ID -> 400.
func TestTaskDetailHandler_InvalidID(t *testing.T) {
	prov := staticTaskDetailProvider{}
	reader := staticAuditReader{}
	srv := newTaskDetailServer(t, prov, reader)

	req := httptest.NewRequest(http.MethodGet, "/tasks/notanumber", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

// TestTaskDetailHandler_ProviderError: provider returns non-404 error -> 500.
func TestTaskDetailHandler_ProviderError(t *testing.T) {
	prov := staticTaskDetailProvider{err: errors.New("db connection lost")}
	reader := staticAuditReader{}
	srv := newTaskDetailServer(t, prov, reader)

	req := httptest.NewRequest(http.MethodGet, "/tasks/1", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rec.Code)
	}
	// Must not leak raw error detail.
	if strings.Contains(rec.Body.String(), "db connection lost") {
		t.Errorf("response body leaks raw error: %s", rec.Body.String())
	}
}
