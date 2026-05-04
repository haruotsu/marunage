package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/haruotsu/marunage/internal/store"
)

// taskDetailLoadFailedMessage is the generic error message surfaced when
// the provider returns an unexpected error. It mirrors the dashboard
// convention: internal details go to daemon.log, not the HTTP body.
const taskDetailLoadFailedMessage = "Task detail unavailable. See daemon.log for details."

// newTaskDetailHandler returns the GET /tasks/{id} handler. It parses the
// path value, delegates the load to provider, reads related audit entries,
// flattens the result into a view, and renders task_detail.html. Errors
// from the provider map to 404 (store.ErrNotFound) or 500 (everything else).
func newTaskDetailHandler(renderer Renderer, provider TaskDetailProvider, audits AuditReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid task id", http.StatusBadRequest)
			return
		}

		task, err := provider.TaskDetail(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "task not found", http.StatusNotFound)
				return
			}
			http.Error(w, taskDetailLoadFailedMessage, http.StatusInternalServerError)
			return
		}

		entries, err := audits.EntriesForTask(r.Context(), id)
		if err != nil {
			// Audit read failure is non-fatal: the task data is still
			// valid and the page should load. Log the failure implicitly
			// (handler.go level) and render with empty entries.
			entries = nil
		}

		view := newTaskDetailView(task, entries)
		if renderErr := renderer.Render(w, "task_detail.html", view); renderErr != nil {
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	})
}
