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
	Body           string     `json:"body"`
	Notes          string     `json:"notes"`
	Status         string     `json:"status"`
	Priority       int        `json:"priority"`
	LockKey        string     `json:"lock_key"`
	CWD            string     `json:"cwd"`
	WS             string     `json:"ws"`
	JudgmentReason string     `json:"judgment_reason"`
	ResultSummary  string     `json:"result_summary"`
	Reflection     string     `json:"reflection"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	StartedAt      *time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
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
		Body:           t.Body,
		Notes:          t.Notes,
		Status:         t.Status,
		Priority:       t.Priority,
		LockKey:        t.LockKey,
		CWD:            t.CWD,
		WS:             t.WS,
		JudgmentReason: t.JudgmentReason,
		ResultSummary:  t.ResultSummary,
		Reflection:     t.Reflection,
		CreatedAt:      t.CreatedAt,
		UpdatedAt:      t.UpdatedAt,
	}
	if !t.StartedAt.IsZero() {
		task.StartedAt = &t.StartedAt
	}
	if !t.CompletedAt.IsZero() {
		task.CompletedAt = &t.CompletedAt
	}
	return task
}

const (
	defaultTaskListLimit = 100
	maxTaskListLimit     = 1000
)

// validTaskStatuses mirrors store.validStatuses for handler-level input validation.
var validTaskStatuses = map[string]struct{}{
	store.StatusPending:      {},
	store.StatusRunning:      {},
	store.StatusDone:         {},
	store.StatusFailed:       {},
	store.StatusSkipped:      {},
	store.StatusWaitingHuman: {},
}

func newTaskListAPIHandler(provider TaskListProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		filter := TaskListFilter{Limit: defaultTaskListLimit}

		if s := r.URL.Query().Get("status"); s != "" {
			statuses := strings.Split(s, ",")
			for i, sv := range statuses {
				statuses[i] = strings.TrimSpace(sv)
				if _, ok := validTaskStatuses[statuses[i]]; !ok {
					writeJSONError(w, http.StatusBadRequest, "invalid status value: "+statuses[i])
					return
				}
			}
			filter.Statuses = statuses
		}
		if s := r.URL.Query().Get("source"); s != "" {
			filter.Source = s
		}
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				filter.Limit = min(n, maxTaskListLimit)
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

		writeJSON(w, http.StatusOK, taskListAPIResponse{
			Tasks: out,
			Total: total,
		})
	})
}
