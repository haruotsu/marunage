package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeTasks implements TaskOpsStore for unit tests.
// Each method records the call and returns the pre-configured result.
type fakeTasks struct {
	dispatchFn       func(ctx context.Context, id int64) error
	promoteFn        func(ctx context.Context, id int64) error
	reopenFn         func(ctx context.Context, id int64) error
	addFn            func(ctx context.Context, title, body, cwd string, priority int) (int64, error)
	updatePriorityFn func(ctx context.Context, id int64, priority int) error
	deleteFn         func(ctx context.Context, id int64) error
}

func (f *fakeTasks) Dispatch(ctx context.Context, id int64) error {
	if f.dispatchFn != nil {
		return f.dispatchFn(ctx, id)
	}
	return nil
}

func (f *fakeTasks) Promote(ctx context.Context, id int64) error {
	if f.promoteFn != nil {
		return f.promoteFn(ctx, id)
	}
	return nil
}

func (f *fakeTasks) Reopen(ctx context.Context, id int64) error {
	if f.reopenFn != nil {
		return f.reopenFn(ctx, id)
	}
	return nil
}

func (f *fakeTasks) Add(ctx context.Context, title, body, cwd string, priority int) (int64, error) {
	if f.addFn != nil {
		return f.addFn(ctx, title, body, cwd, priority)
	}
	return 1, nil
}

func (f *fakeTasks) UpdatePriority(ctx context.Context, id int64, priority int) error {
	if f.updatePriorityFn != nil {
		return f.updatePriorityFn(ctx, id, priority)
	}
	return nil
}

func (f *fakeTasks) Delete(ctx context.Context, id int64) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id)
	}
	return nil
}

// ErrTaskNotFound and errTaskOpsInvalidStatus are sentinel errors used in
// task_ops.go for distinguishing 404 vs 409 in handler responses.
// Declare them here for test access (they will be defined in task_ops.go).

// doOpsRequest sends a request to the given handler with CSRF credentials.
// It sets the "id" path value from the path so handlers that use r.PathValue("id")
// receive the correct value without going through a real mux.
func doOpsRequest(t *testing.T, h http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	const token = "fixed-test-token"
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	req.Header.Set(CSRFHeaderName, token)
	// Inject path value so handlers using r.PathValue("id") work without a mux.
	// Extract the segment after "tasks/" and before the next "/" (if any).
	if id := extractIDFromTestPath(path); id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// extractIDFromTestPath extracts the id segment from test paths like
// /api/tasks/42/dispatch or /api/tasks/42.
func extractIDFromTestPath(path string) string {
	const prefix = "/api/tasks/"
	rest := strings.TrimPrefix(path, prefix)
	if rest == path {
		return ""
	}
	// Take only the first segment (before any "/")
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// TestTaskOpsHandler_Dispatch_OK: dispatch pending task -> 200 JSON {"status":"ok"}
func TestTaskOpsHandler_Dispatch_OK(t *testing.T) {
	store := &fakeTasks{}
	h := newDispatchTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks/1/dispatch", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("response status = %q; want ok", resp["status"])
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q; want application/json prefix", ct)
	}
}

// TestTaskOpsHandler_Dispatch_NotFound: non-existent ID -> 404
func TestTaskOpsHandler_Dispatch_NotFound(t *testing.T) {
	store := &fakeTasks{
		dispatchFn: func(_ context.Context, _ int64) error {
			return ErrTaskNotFound
		},
	}
	h := newDispatchTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks/999/dispatch", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

// TestTaskOpsHandler_Dispatch_InvalidStatus: task not pending -> 409
func TestTaskOpsHandler_Dispatch_InvalidStatus(t *testing.T) {
	store := &fakeTasks{
		dispatchFn: func(_ context.Context, _ int64) error {
			return ErrTaskInvalidTransition
		},
	}
	h := newDispatchTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks/1/dispatch", nil)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409", rec.Code)
	}
}

// TestTaskOpsHandler_Promote_OK: skipped -> pending success -> 200
func TestTaskOpsHandler_Promote_OK(t *testing.T) {
	var calledID int64
	store := &fakeTasks{
		promoteFn: func(_ context.Context, id int64) error {
			calledID = id
			return nil
		},
	}
	h := newPromoteTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks/42/promote", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if calledID != 42 {
		t.Errorf("called with id=%d; want 42", calledID)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("response status = %q; want ok", resp["status"])
	}
}

// TestTaskOpsHandler_Reopen_OK: done -> pending success -> 200
func TestTaskOpsHandler_Reopen_OK(t *testing.T) {
	var calledID int64
	store := &fakeTasks{
		reopenFn: func(_ context.Context, id int64) error {
			calledID = id
			return nil
		},
	}
	h := newReopenTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks/7/reopen", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if calledID != 7 {
		t.Errorf("called with id=%d; want 7", calledID)
	}
}

// TestTaskOpsHandler_Add_OK: POST /api/tasks with title -> 201 JSON with id
func TestTaskOpsHandler_Add_OK(t *testing.T) {
	store := &fakeTasks{
		addFn: func(_ context.Context, title, body, cwd string, priority int) (int64, error) {
			return 99, nil
		},
	}
	h := newAddTaskHandler(store, nil)

	payload, _ := json.Marshal(map[string]any{"title": "my task", "body": "details", "priority": 5})
	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks", payload)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201", rec.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("response status = %q; want ok", resp["status"])
	}
	// id comes back as a JSON number (float64 in Go's default decode)
	if id, ok := resp["id"].(float64); !ok || id != 99 {
		t.Errorf("response id = %v; want 99", resp["id"])
	}
}

// TestTaskOpsHandler_Add_WithCWD: POST /api/tasks with cwd -> cwd is passed to store
func TestTaskOpsHandler_Add_WithCWD(t *testing.T) {
	var capturedCWD string
	store := &fakeTasks{
		addFn: func(_ context.Context, title, body, cwd string, priority int) (int64, error) {
			capturedCWD = cwd
			return 42, nil
		},
	}
	h := newAddTaskHandler(store, nil)

	payload, _ := json.Marshal(map[string]any{"title": "task", "cwd": "/my/project"})
	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks", payload)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201", rec.Code)
	}
	if capturedCWD != "/my/project" {
		t.Errorf("cwd = %q; want /my/project", capturedCWD)
	}
}

// TestTaskOpsHandler_Add_MissingTitle: missing title -> 400
func TestTaskOpsHandler_Add_MissingTitle(t *testing.T) {
	store := &fakeTasks{}
	h := newAddTaskHandler(store, nil)

	payload, _ := json.Marshal(map[string]any{"body": "no title"})
	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks", payload)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

// TestTaskOpsHandler_Add_CWDNotAllowed: cwd outside allowlist -> 400
func TestTaskOpsHandler_Add_CWDNotAllowed(t *testing.T) {
	store := &fakeTasks{}
	h := newAddTaskHandler(store, []string{"/home/user/works", "/home/user/src"})

	payload, _ := json.Marshal(map[string]any{"title": "task", "cwd": "/tmp/hack"})
	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks", payload)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (cwd not allowed)", rec.Code)
	}
}

// TestTaskOpsHandler_Add_EmptyCWDWithAllowlist: empty cwd + non-empty allowlist -> 400
func TestTaskOpsHandler_Add_EmptyCWDWithAllowlist(t *testing.T) {
	store := &fakeTasks{}
	h := newAddTaskHandler(store, []string{"/home/user/works"})

	payload, _ := json.Marshal(map[string]any{"title": "task", "cwd": ""})
	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks", payload)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (empty cwd with allowlist)", rec.Code)
	}
}

// TestTaskOpsHandler_Add_EmptyAllowlist_AllowsAnyCWD: empty allowlist accepts any cwd
func TestTaskOpsHandler_Add_EmptyAllowlist_AllowsAnyCWD(t *testing.T) {
	store := &fakeTasks{
		addFn: func(_ context.Context, _, _, _ string, _ int) (int64, error) { return 1, nil },
	}
	h := newAddTaskHandler(store, nil)

	payload, _ := json.Marshal(map[string]any{"title": "task", "cwd": "/anything/goes"})
	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks", payload)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 (no allowlist = allow all)", rec.Code)
	}
}

// TestTaskOpsHandler_Add_CWDMatchesAllowlist: matching prefix is accepted
func TestTaskOpsHandler_Add_CWDMatchesAllowlist(t *testing.T) {
	store := &fakeTasks{
		addFn: func(_ context.Context, _, _, _ string, _ int) (int64, error) { return 7, nil },
	}
	h := newAddTaskHandler(store, []string{"/home/user/works", "/home/user/src"})

	payload, _ := json.Marshal(map[string]any{"title": "task", "cwd": "/home/user/src/myrepo"})
	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks", payload)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 (cwd matches allowlist)", rec.Code)
	}
}

// TestTaskOpsHandler_Priority_OK: PATCH priority -> 200
func TestTaskOpsHandler_Priority_OK(t *testing.T) {
	var calledPriority int
	store := &fakeTasks{
		updatePriorityFn: func(_ context.Context, id int64, priority int) error {
			calledPriority = priority
			return nil
		},
	}
	h := newUpdatePriorityHandler(store)

	payload, _ := json.Marshal(map[string]any{"priority": 10})
	rec := doOpsRequest(t, h, http.MethodPatch, "/api/tasks/3/priority", payload)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if calledPriority != 10 {
		t.Errorf("called with priority=%d; want 10", calledPriority)
	}
}

// TestTaskOpsHandler_Delete_OK: DELETE -> 200
func TestTaskOpsHandler_Delete_OK(t *testing.T) {
	var calledID int64
	store := &fakeTasks{
		deleteFn: func(_ context.Context, id int64) error {
			calledID = id
			return nil
		},
	}
	h := newDeleteTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodDelete, "/api/tasks/5", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if calledID != 5 {
		t.Errorf("called with id=%d; want 5", calledID)
	}
}

// TestCSRF_TaskOps_MissingToken: POST without CSRF token -> 403
func TestCSRF_TaskOps_MissingToken(t *testing.T) {
	srv := newTestServer(t)

	// We hit a real route that's POST-protected; /test-post is convenient
	// and is enabled for test servers.
	req := httptest.NewRequest(http.MethodPost, "/test-post", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403 (CSRF should block tokenless POST)", rec.Code)
	}
}

// TestTaskOpsHandler_Dispatch_BadID: non-numeric id -> 400
func TestTaskOpsHandler_Dispatch_BadID(t *testing.T) {
	store := &fakeTasks{}
	h := newDispatchTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks/abc/dispatch", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

// TestTaskOpsHandler_Promote_NotFound: non-existent id -> 404
func TestTaskOpsHandler_Promote_NotFound(t *testing.T) {
	store := &fakeTasks{
		promoteFn: func(_ context.Context, _ int64) error {
			return ErrTaskNotFound
		},
	}
	h := newPromoteTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks/999/promote", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

// TestTaskOpsHandler_Delete_NotFound: deleting non-existent task -> 404
func TestTaskOpsHandler_Delete_NotFound(t *testing.T) {
	store := &fakeTasks{
		deleteFn: func(_ context.Context, _ int64) error {
			return ErrTaskNotFound
		},
	}
	h := newDeleteTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodDelete, "/api/tasks/999", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

// TestTaskOpsHandler_UpdatePriority_NotFound: updating priority for non-existent task -> 404
func TestTaskOpsHandler_UpdatePriority_NotFound(t *testing.T) {
	store := &fakeTasks{
		updatePriorityFn: func(_ context.Context, _ int64, _ int) error {
			return ErrTaskNotFound
		},
	}
	h := newUpdatePriorityHandler(store)

	payload, _ := json.Marshal(map[string]any{"priority": 5})
	rec := doOpsRequest(t, h, http.MethodPatch, "/api/tasks/999/priority", payload)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

// TestTaskOpsRoutes_Wired: confirm all task ops routes are registered in server Routes
func TestTaskOpsRoutes_Wired(t *testing.T) {
	store := &fakeTasks{}
	srv, err := NewServer(Options{
		TokenSource:      testTokenSource,
		EnableTestRoutes: true,
		TaskOps:          store,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	const token = "fixed-test-token"
	addCsrf := func(req *http.Request) *http.Request {
		req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
		req.Header.Set(CSRFHeaderName, token)
		return req
	}

	type testCase struct {
		method string
		path   string
		body   []byte
	}
	cases := []testCase{
		{http.MethodPost, "/api/tasks/1/dispatch", nil},
		{http.MethodPost, "/api/tasks/1/promote", nil},
		{http.MethodPost, "/api/tasks/1/reopen", nil},
		{http.MethodPost, "/api/tasks", mustMarshal(t, map[string]any{"title": "x"})},
		{http.MethodPatch, "/api/tasks/1/priority", mustMarshal(t, map[string]any{"priority": 1})},
		{http.MethodDelete, "/api/tasks/1", nil},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			var req *http.Request
			if c.body != nil {
				req = httptest.NewRequest(c.method, c.path, bytes.NewReader(c.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(c.method, c.path, nil)
			}
			req = addCsrf(req)
			rec := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Errorf("route %s %s returned 404; route is not wired", c.method, c.path)
			}
		})
	}
}

// errSentinel used to test that internal errors map to 500.
var errSentinel = errors.New("internal error")

// TestTaskOpsHandler_Dispatch_InternalError: internal store error -> 500
func TestTaskOpsHandler_Dispatch_InternalError(t *testing.T) {
	store := &fakeTasks{
		dispatchFn: func(_ context.Context, _ int64) error {
			return errSentinel
		},
	}
	h := newDispatchTaskHandler(store)

	rec := doOpsRequest(t, h, http.MethodPost, "/api/tasks/1/dispatch", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rec.Code)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
