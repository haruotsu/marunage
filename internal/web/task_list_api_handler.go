package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// TaskListFilter carries query parameters for the task list endpoint.
type TaskListFilter struct {
	Statuses []string
	Source   string
	Limit    int
}

// TaskListProvider is the seam the task list handler consumes.
type TaskListProvider interface {
	ListTasks(ctx context.Context, filter TaskListFilter) ([]store.Task, int, error)
}

// TaskListProviderFunc adapts a function to TaskListProvider.
type TaskListProviderFunc func(ctx context.Context, filter TaskListFilter) ([]store.Task, int, error)

func (f TaskListProviderFunc) ListTasks(ctx context.Context, filter TaskListFilter) ([]store.Task, int, error) {
	return f(ctx, filter)
}

type taskAPITask struct {
	ID             int64      `json:"id"`
	Source         string     `json:"source"`
	ExternalID     string     `json:"external_id"`
	ExternalURL    string     `json:"external_url"`
	Title          string     `json:"title"`
	Status         string     `json:"status"`
	Priority       int        `json:"priority"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	StartedAt      *time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
	WS             string     `json:"ws"`
	JudgmentReason string     `json:"judgment_reason"`
}

type taskListAPIResponse struct {
	Tasks []taskAPITask `json:"tasks"`
	Total int           `json:"total"`
}

func toTaskAPITask(t store.Task) taskAPITask {
	task := taskAPITask{
		ID:             t.ID,
		Source:         t.Source,
		ExternalID:     t.ExternalID,
		ExternalURL:    t.ExternalURL,
		Title:          t.Title,
		Status:         t.Status,
		Priority:       t.Priority,
		CreatedAt:      t.CreatedAt,
		UpdatedAt:      t.UpdatedAt,
		WS:             t.WS,
		JudgmentReason: t.JudgmentReason,
	}
	if !t.StartedAt.IsZero() {
		task.StartedAt = &t.StartedAt
	}
	if !t.CompletedAt.IsZero() {
		task.CompletedAt = &t.CompletedAt
	}
	return task
}

const defaultTaskListLimit = 100

func newTaskListAPIHandler(provider TaskListProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		filter := TaskListFilter{Limit: defaultTaskListLimit}

		if s := r.URL.Query().Get("status"); s != "" {
			filter.Statuses = strings.Split(s, ",")
		}
		if s := r.URL.Query().Get("source"); s != "" {
			filter.Source = s
		}
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				filter.Limit = n
			}
		}

		tasks, total, err := provider.ListTasks(r.Context(), filter)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "task list unavailable")
			return
		}

		out := make([]taskAPITask, len(tasks))
		for i, t := range tasks {
			out[i] = toTaskAPITask(t)
		}
		if out == nil {
			out = []taskAPITask{}
		}

		writeJSON(w, http.StatusOK, taskListAPIResponse{
			Tasks: out,
			Total: total,
		})
	})
}
