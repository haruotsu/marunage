package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Sentinel errors TaskOpsStore implementations return so handlers can map
// them to the right HTTP status code without parsing error strings.
var (
	// errTaskOpsNotFound is returned when the target row does not exist.
	errTaskOpsNotFound = errors.New("task ops: not found")
	// errTaskOpsInvalidTransition is returned when a state transition is
	// not allowed from the current status (e.g. dispatch on a non-pending
	// row). Maps to 409 Conflict.
	errTaskOpsInvalidTransition = errors.New("task ops: invalid status transition")
)

// TaskOpsStore is the write-side surface the task operation handlers need.
// Production wires sqlTaskOpsStore; tests inject a fake via fakeTasks.
type TaskOpsStore interface {
	// Dispatch transitions a task from pending -> running and stamps
	// started_at. Returns errTaskOpsNotFound or errTaskOpsInvalidTransition
	// on the documented failure paths.
	Dispatch(ctx context.Context, id int64) error
	// Promote transitions a task from skipped -> pending.
	Promote(ctx context.Context, id int64) error
	// Reopen transitions a task from done or failed -> pending.
	Reopen(ctx context.Context, id int64) error
	// Add inserts a new manual task and returns its assigned id.
	Add(ctx context.Context, title, body string, priority int) (int64, error)
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
	case errors.Is(err, errTaskOpsNotFound):
		writeJSONError(w, http.StatusNotFound, "not found")
	case errors.Is(err, errTaskOpsInvalidTransition):
		writeJSONError(w, http.StatusConflict, "invalid status transition")
	default:
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	}
	return true
}

// newDispatchTaskHandler returns POST /api/tasks/{id}/dispatch.
// Transitions pending -> running and stamps started_at.
func newDispatchTaskHandler(store TaskOpsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := parseIDFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}
		if mapOpsError(w, store.Dispatch(r.Context(), id)) {
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
	Priority int    `json:"priority"`
}

// newAddTaskHandler returns POST /api/tasks.
// Creates a new manual task. title is required; body and priority are optional.
func newAddTaskHandler(store TaskOpsStore) http.Handler {
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
		id, err := store.Add(r.Context(), req.Title, req.Body, req.Priority)
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
