package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/haruotsu/marunage/internal/policy"
)

// Sentinel errors returned by TaskOpsStore / TaskDispatcher implementations
// so handlers can map them to the right HTTP status code.
var (
	// ErrTaskNotFound is returned when the target row does not exist.
	ErrTaskNotFound = errors.New("task ops: not found")
	// ErrTaskInvalidTransition is returned when a state transition is not
	// allowed from the current status (e.g. dispatch on a non-pending row).
	// Maps to 409 Conflict.
	ErrTaskInvalidTransition = errors.New("task ops: invalid status transition")
	// ErrNoActiveSession is returned by TaskDispatcher when the web server is
	// not running inside a live cmux terminal session and therefore cannot
	// dispatch tasks. Orphaned-process dispatch is intentionally unsupported;
	// the operator must restart marunage web from an active terminal.
	// Maps to 503 Service Unavailable.
	ErrNoActiveSession = errors.New("task ops: no active cmux session")
)

// TaskDispatcher triggers real cmux-backed dispatch for a single task.
// Production wires a webDispatchAdapter (in internal/cli) that delegates
// to dispatch.Dispatcher.Run; tests inject a fake via fakeTasks.
type TaskDispatcher interface {
	Dispatch(ctx context.Context, id int64) error
}

// TaskOpsStore is the write-side surface the task operation handlers need.
// Production wires sqlTaskOpsStore; tests inject a fake via fakeTasks.
type TaskOpsStore interface {
	// Dispatch transitions a task from pending -> running and stamps
	// started_at. Returns ErrTaskNotFound or ErrTaskInvalidTransition
	// on the documented failure paths.
	Dispatch(ctx context.Context, id int64) error
	// Promote transitions a task from skipped -> pending.
	Promote(ctx context.Context, id int64) error
	// Reopen transitions a task from done or failed -> pending.
	Reopen(ctx context.Context, id int64) error
	// Add inserts a new manual task and returns its assigned id.
	// cwd is the working directory for dispatch; empty means "unset".
	Add(ctx context.Context, title, body, cwd string, priority int) (int64, error)
	// UpdatePriority changes the priority of an existing task.
	UpdatePriority(ctx context.Context, id int64, priority int) error
	// Delete removes a task row entirely.
	Delete(ctx context.Context, id int64) error
}

// parseIDFromRequest extracts the {id} path wildcard from the request using
// r.PathValue("id"), which is populated by Go 1.22's net/http ServeMux when
// the route pattern contains {id}. Returns errTaskOpsNotFound-compatible 400
// if the value is absent or not a valid int64.
func parseIDFromRequest(r *http.Request) (int64, error) {
	raw := r.PathValue("id")
	if raw == "" {
		return 0, errors.New("task ops: missing id path value")
	}
	return strconv.ParseInt(raw, 10, 64)
}

// writeJSONError writes a JSON error response with the given status and message.
// It delegates to writeJSON (defined in skills.go, shared across the web package).
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// mapOpsError translates TaskOpsStore errors to HTTP status codes.
// Returns 0 if the error is nil, otherwise the appropriate HTTP status.
func mapOpsError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrTaskNotFound):
		writeJSONError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrTaskInvalidTransition):
		writeJSONError(w, http.StatusConflict, "invalid status transition")
	case errors.Is(err, ErrNoActiveSession):
		writeJSONError(w, http.StatusServiceUnavailable, "no active session: restart marunage web from a terminal")
	default:
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	}
	return true
}

// newDispatchTaskHandler returns POST /api/tasks/{id}/dispatch.
// Calls disp.Dispatch which triggers real cmux-backed dispatch in production.
func newDispatchTaskHandler(disp TaskDispatcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := parseIDFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}
		if mapOpsError(w, disp.Dispatch(r.Context(), id)) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
	})
}

// newPromoteTaskHandler returns POST /api/tasks/{id}/promote.
// Transitions skipped -> pending.
func newPromoteTaskHandler(store TaskOpsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := parseIDFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}
		if mapOpsError(w, store.Promote(r.Context(), id)) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
	})
}

// newReopenTaskHandler returns POST /api/tasks/{id}/reopen.
// Transitions done or failed -> pending.
func newReopenTaskHandler(store TaskOpsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := parseIDFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}
		if mapOpsError(w, store.Reopen(r.Context(), id)) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
	})
}

// addTaskRequest is the JSON body for POST /api/tasks.
type addTaskRequest struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	CWD      string `json:"cwd"`
	Priority int    `json:"priority"`
}

// newAddTaskHandler returns POST /api/tasks.
// Creates a new manual task. title is required; body and priority are optional.
// allowedCwdPrefixes mirrors execution.allowed_cwd_prefixes: when non-empty,
// the task's cwd must start with one of the prefixes (same rule the dispatcher
// enforces at dispatch time, surfaced here for immediate feedback).
func newAddTaskHandler(store TaskOpsStore, allowedCwdPrefixes []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req addTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if strings.TrimSpace(req.Title) == "" {
			writeJSONError(w, http.StatusBadRequest, "title is required")
			return
		}
		if !policy.CwdAllowed(req.CWD, allowedCwdPrefixes) {
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("cwd %q is not in allowed_cwd_prefixes; set execution.allowed_cwd_prefixes or use an empty allowlist to allow all paths", req.CWD))
			return
		}
		id, err := store.Add(r.Context(), req.Title, req.Body, req.CWD, req.Priority)
		if mapOpsError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "id": id})
	})
}

// updatePriorityRequest is the JSON body for PATCH /api/tasks/{id}/priority.
type updatePriorityRequest struct {
	Priority int `json:"priority"`
}

// newUpdatePriorityHandler returns PATCH /api/tasks/{id}/priority.
// Updates the priority of an existing task.
func newUpdatePriorityHandler(store TaskOpsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := parseIDFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}
		var req updatePriorityRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if mapOpsError(w, store.UpdatePriority(r.Context(), id, req.Priority)) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
	})
}

// newDeleteTaskHandler returns DELETE /api/tasks/{id}.
// Removes a task regardless of its current status.
func newDeleteTaskHandler(store TaskOpsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := parseIDFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}
		if mapOpsError(w, store.Delete(r.Context(), id)) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
	})
}
