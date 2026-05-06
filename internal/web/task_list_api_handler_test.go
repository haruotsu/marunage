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

type staticTaskListProvider struct {
	tasks     []store.Task
	total     int
	err       error
	gotFilter TaskListFilter
}

func (s *staticTaskListProvider) ListTasks(_ context.Context, f TaskListFilter) ([]store.Task, int, error) {
	s.gotFilter = f
	return s.tasks, s.total, s.err
}

var errTaskListTestFailed = errors.New("task list provider test failure")

func sampleTaskListTasks() []store.Task {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	return []store.Task{
		{
			ID:          1,
			Source:      "github",
			ExternalID:  "issue-123",
			ExternalURL: "https://github.com/owner/repo/issues/123",
			Title:       "Fix bug",
			Status:      store.StatusPending,
			Priority:    5,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:        2,
			Source:    "slack",
			Title:     "Review PR",
			Status:    store.StatusRunning,
			Priority:  3,
			CreatedAt: now,
			UpdatedAt: now,
			StartedAt: now,
			WS:        "workspace:7",
		},
	}
}

func newTaskListAPIServer(t *testing.T, prov TaskListProvider) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		TaskList:          prov,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func TestTaskListAPIHandler_ReturnsJSON(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks(), total: 2}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
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
	tasks, ok := got["tasks"].([]any)
	if !ok {
		t.Fatalf("tasks missing or wrong type: %T", got["tasks"])
	}
	if len(tasks) != 2 {
		t.Errorf("tasks len=%d; want 2", len(tasks))
	}
	if got["total"] != float64(2) {
		t.Errorf("total=%v; want 2", got["total"])
	}
	row, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("tasks[0] wrong type: %T", tasks[0])
	}
	if row["id"] != float64(1) {
		t.Errorf("tasks[0].id=%v; want 1", row["id"])
	}
	if row["source"] != "github" {
		t.Errorf("tasks[0].source=%v; want github", row["source"])
	}
	if row["external_id"] != "issue-123" {
		t.Errorf("tasks[0].external_id=%v; want issue-123", row["external_id"])
	}
	if row["status"] != "pending" {
		t.Errorf("tasks[0].status=%v; want pending", row["status"])
	}
}

func TestTaskListAPIHandler_StatusFilter(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks()[:1], total: 1}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks?status=pending,running", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	if len(prov.gotFilter.Statuses) != 2 {
		t.Errorf("Statuses=%v; want [pending running]", prov.gotFilter.Statuses)
	}
	if prov.gotFilter.Statuses[0] != "pending" || prov.gotFilter.Statuses[1] != "running" {
		t.Errorf("Statuses=%v; want [pending running]", prov.gotFilter.Statuses)
	}
}

func TestTaskListAPIHandler_SourceFilter(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks()[:1], total: 1}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks?source=github", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	if prov.gotFilter.Source != "github" {
		t.Errorf("Source=%q; want github", prov.gotFilter.Source)
	}
}

func TestTaskListAPIHandler_DefaultLimit(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks(), total: 2}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if prov.gotFilter.Limit != 100 {
		t.Errorf("Limit=%d; want 100 (default)", prov.gotFilter.Limit)
	}
}

func TestTaskListAPIHandler_NullableTimestamps(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks(), total: 2}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	tasks := got["tasks"].([]any)
	// tasks[0] has no started_at so it should be null
	row0 := tasks[0].(map[string]any)
	if row0["started_at"] != nil {
		t.Errorf("tasks[0].started_at=%v; want null for pending task", row0["started_at"])
	}
	// tasks[1] has started_at set
	row1 := tasks[1].(map[string]any)
	if row1["started_at"] == nil {
		t.Error("tasks[1].started_at should not be null for running task")
	}
}

func TestTaskListAPIHandler_SetsCacheControlNoStore(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks(), total: 2}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

func TestTaskListAPIHandler_ProviderError(t *testing.T) {
	prov := &staticTaskListProvider{err: errTaskListTestFailed}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), errTaskListTestFailed.Error()) {
		t.Errorf("response leaks raw error detail: %q", w.Body.String())
	}
}

func TestTaskListAPIHandler_LimitCap(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks(), total: 2}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks?limit=9999999", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	if prov.gotFilter.Limit != maxTaskListLimit {
		t.Errorf("Limit=%d; want %d (capped)", prov.gotFilter.Limit, maxTaskListLimit)
	}
}

func TestTaskListAPIHandler_InvalidLimitUsesDefault(t *testing.T) {
	for _, q := range []string{"limit=0", "limit=-5", "limit=abc"} {
		t.Run(q, func(t *testing.T) {
			prov := &staticTaskListProvider{tasks: sampleTaskListTasks(), total: 2}
			srv := newTaskListAPIServer(t, prov)

			req := httptest.NewRequest(http.MethodGet, "/api/tasks?"+q, nil)
			w := httptest.NewRecorder()
			srv.Routes().ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status=%d; want 200", w.Code)
			}
			if prov.gotFilter.Limit != defaultTaskListLimit {
				t.Errorf("Limit=%d; want %d (default) for query %q", prov.gotFilter.Limit, defaultTaskListLimit, q)
			}
		})
	}
}

func TestTaskListAPIHandler_InvalidStatusReturns400(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks(), total: 2}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks?status=invalid_status", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d; want 400 for invalid status", w.Code)
	}
}

func TestTaskListAPIHandler_EmptyTasksReturnsArray(t *testing.T) {
	prov := &staticTaskListProvider{tasks: nil, total: 0}
	srv := newTaskListAPIServer(t, prov)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if string(raw["tasks"]) != "[]" {
		t.Errorf("tasks=%s; want [] (not null) for empty list", string(raw["tasks"]))
	}
	if string(raw["total"]) != "0" {
		t.Errorf("total=%s; want 0", string(raw["total"]))
	}
}

func TestTaskListAPIHandler_StatusTrimmingPassesTrimmedValues(t *testing.T) {
	prov := &staticTaskListProvider{tasks: sampleTaskListTasks()[:1], total: 1}
	srv := newTaskListAPIServer(t, prov)

	// Spaces around status values should be trimmed before passing to provider.
	req := httptest.NewRequest(http.MethodGet, "/api/tasks?status=pending%2C+running", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	for _, s := range prov.gotFilter.Statuses {
		if s != strings.TrimSpace(s) {
			t.Errorf("filter.Statuses contains untrimmed value %q", s)
		}
	}
}
