package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/haruotsu/marunage/internal/store"
)

type taskDetailAPIResponse struct {
	Task         taskAPITask  `json:"task"`
	AuditEntries []AuditEntry `json:"audit_entries"`
}

func newTaskDetailAPIHandler(provider TaskDetailProvider, audits AuditReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}

		task, err := provider.TaskDetail(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "task not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "task detail unavailable")
			return
		}

		entries, err := audits.EntriesForTask(r.Context(), id)
		if err != nil {
			entries = nil
		}
		if entries == nil {
			entries = []AuditEntry{}
		}

		writeJSON(w, http.StatusOK, taskDetailAPIResponse{
			Task:         toTaskAPITask(task),
			AuditEntries: entries,
		})
	})
}
