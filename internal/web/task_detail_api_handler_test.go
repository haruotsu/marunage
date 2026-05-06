package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

type staticTaskDetailAPIProvider struct {
	task store.Task
	err  error
}

func (s staticTaskDetailAPIProvider) TaskDetail(_ context.Context, _ int64) (store.Task, error) {
	return s.task, s.err
}

type staticAuditAPIReader struct {
	entries []AuditEntry
	err     error
}

func (s staticAuditAPIReader) EntriesForTask(_ context.Context, _ int64) ([]AuditEntry, error) {
	return s.entries, s.err
}

var errTaskDetailAPITestFailed = errors.New("task detail api provider test failure")

func sampleDetailTask() store.Task {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	started := time.Date(2024, 1, 1, 11, 0, 0, 0, time.UTC)
	return store.Task{
		ID:          42,
		Source:      "github",
		ExternalID:  "issue-99",
		ExternalURL: "https://github.com/owner/repo/issues/99",
		Title:       "Implement feature X",
		Status:      store.StatusRunning,
		Priority:    7,
		CreatedAt:   now,
		UpdatedAt:   now,
		StartedAt:   started,
		WS:          "workspace:3",
	}
}

func newTaskDetailAPIServer(t *testing.T, prov TaskDetailProvider, audits AuditReader) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		TaskDetail:        prov,
		AuditLog:          audits,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func TestTaskDetailAPIHandler_ReturnsJSON(t *testing.T) {
	prov := staticTaskDetailAPIProvider{task: sampleDetailTask()}
	srv := newTaskDetailAPIServer(t, prov, noopAuditReader{})

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/42", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q; want application/json", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	task, ok := got["task"].(map[string]any)
	if !ok {
		t.Fatalf("task missing or wrong type: %T", got["task"])
	}
	if task["id"] != float64(42) {
		t.Errorf("task.id=%v; want 42", task["id"])
	}
	if task["source"] != "github" {
		t.Errorf("task.source=%v; want github", task["source"])
	}
	if task["ws"] != "workspace:3" {
		t.Errorf("task.ws=%v; want workspace:3", task["ws"])
	}
	if task["started_at"] == nil {
		t.Error("task.started_at should not be null for running task")
	}
	if task["completed_at"] != nil {
		t.Errorf("task.completed_at=%v; want null", task["completed_at"])
	}
}

func TestTaskDetailAPIHandler_NotFound(t *testing.T) {
	prov := staticTaskDetailAPIProvider{err: store.ErrNotFound}
	srv := newTaskDetailAPIServer(t, prov, noopAuditReader{})

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/99", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d; want 404", w.Code)
	}
}

func TestTaskDetailAPIHandler_IncludesAuditEntries(t *testing.T) {
	prov := staticTaskDetailAPIProvider{task: sampleDetailTask()}
	audits := staticAuditAPIReader{
		entries: []AuditEntry{
			{Time: "2024-01-01T11:00:00Z", Action: "dispatch.start", TaskID: 42, Value: "workspace:3"},
		},
	}
	srv := newTaskDetailAPIServer(t, prov, audits)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/42", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	entries, ok := got["audit_entries"].([]any)
	if !ok {
		t.Fatalf("audit_entries missing or wrong type: %T", got["audit_entries"])
	}
	if len(entries) != 1 {
		t.Errorf("audit_entries len=%d; want 1", len(entries))
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("audit_entries[0] wrong type: %T", entries[0])
	}
	if entry["action"] != "dispatch.start" {
		t.Errorf("audit_entries[0].action=%v; want dispatch.start", entry["action"])
	}
}

func TestTaskDetailAPIHandler_InvalidID(t *testing.T) {
	prov := staticTaskDetailAPIProvider{}
	srv := newTaskDetailAPIServer(t, prov, noopAuditReader{})

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/not-a-number", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d; want 400", w.Code)
	}
}

func TestTaskDetailAPIHandler_SetsCacheControlNoStore(t *testing.T) {
	prov := staticTaskDetailAPIProvider{task: sampleDetailTask()}
	srv := newTaskDetailAPIServer(t, prov, noopAuditReader{})

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/42", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

func TestTaskDetailAPIHandler_ZeroIDReturnsBadRequest(t *testing.T) {
	prov := staticTaskDetailAPIProvider{}
	srv := newTaskDetailAPIServer(t, prov, noopAuditReader{})

	for _, path := range []string{"/api/tasks/0", "/api/tasks/-1"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			srv.Routes().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status=%d; want 400 for %s", w.Code, path)
			}
		})
	}
}

func TestTaskDetailAPIHandler_ProviderError(t *testing.T) {
	prov := staticTaskDetailAPIProvider{err: errTaskDetailAPITestFailed}
	srv := newTaskDetailAPIServer(t, prov, noopAuditReader{})

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/42", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), errTaskDetailAPITestFailed.Error()) {
		t.Errorf("response leaks raw error detail: %q", w.Body.String())
	}
}
