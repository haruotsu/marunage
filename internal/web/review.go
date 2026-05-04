package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// ReviewSnapshot holds the data for the /review page.
type ReviewSnapshot struct {
	Tasks       []store.Task
	GeneratedAt string
}

// ReviewProvider supplies the data for the review page.
type ReviewProvider interface {
	// ReviewSnapshot returns skipped tasks matching the given filter.
	ReviewSnapshot(ctx context.Context, f store.ListFilter) (ReviewSnapshot, error)
}

// ReviewStore is the narrow store surface ReviewProvider reads.
type ReviewStore interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
}

// sqlReviewProvider is the production ReviewProvider backed by a ReviewStore.
type sqlReviewProvider struct {
	store ReviewStore
}

// NewReviewProvider creates a ReviewProvider backed by the given store.
func NewReviewProvider(s ReviewStore) ReviewProvider {
	return &sqlReviewProvider{store: s}
}

func (p *sqlReviewProvider) ReviewSnapshot(ctx context.Context, f store.ListFilter) (ReviewSnapshot, error) {
	tasks, err := p.store.List(ctx, f)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	return ReviewSnapshot{
		Tasks:       tasks,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// reviewReasonCount is one entry in the frequency report.
type reviewReasonCount struct {
	Reason string
	Count  int
}

// reviewPageData is the template payload for review.html.
type reviewPageData struct {
	Tasks       []reviewTaskRow
	FreqReport  []reviewReasonCount
	GeneratedAt string
	LoadError   string
}

// reviewTaskRow is one row in the skipped tasks table.
type reviewTaskRow struct {
	ID             int64
	Source         string
	Title          string
	JudgmentReason string
	CreatedRel     string
}

func newReviewPageData(snap ReviewSnapshot) reviewPageData {
	now := time.Now().UTC()

	rows := make([]reviewTaskRow, 0, len(snap.Tasks))
	freq := make(map[string]int)
	for _, t := range snap.Tasks {
		rows = append(rows, reviewTaskRow{
			ID:             t.ID,
			Source:         t.Source,
			Title:          t.Title,
			JudgmentReason: t.JudgmentReason,
			CreatedRel:     formatRelative(now, t.CreatedAt),
		})
		if t.JudgmentReason != "" {
			freq[t.JudgmentReason]++
		}
	}

	counts := make([]reviewReasonCount, 0, len(freq))
	for reason, n := range freq {
		if n > 1 {
			counts = append(counts, reviewReasonCount{Reason: reason, Count: n})
		}
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Count != counts[j].Count {
			return counts[i].Count > counts[j].Count
		}
		return counts[i].Reason < counts[j].Reason
	})

	return reviewPageData{
		Tasks:       rows,
		FreqReport:  counts,
		GeneratedAt: snap.GeneratedAt,
	}
}

const reviewLoadFailedMessage = "Review data unavailable. See daemon.log for details."

// maxSinceDays caps the ?since= / --since parameter to prevent time.Duration
// overflow (int64 wraps around at ~292 years) and to reject unreasonable inputs.
const maxSinceDays = 36500 // 100 years

// ParseSinceWindow parses a human-friendly duration string (e.g. "7d", "30d",
// "24h") and returns the earliest time that falls within the window relative
// to now. Returns zero time when s is empty (no lower-bound filter). Supports
// "Nd" (days, up to maxSinceDays) and anything time.ParseDuration accepts.
//
// Used by both the CLI (task_review.go) and the Web handler (review.go) to
// avoid duplicated parsing logic.
func ParseSinceWindow(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		n := 0
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &n); err != nil {
			return time.Time{}, fmt.Errorf("invalid day count %q", s)
		}
		if n <= 0 {
			return time.Time{}, fmt.Errorf("day count must be positive, got %q", s)
		}
		if n > maxSinceDays {
			return time.Time{}, fmt.Errorf("day count %d exceeds maximum %d", n, maxSinceDays)
		}
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, err
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("duration must be positive, got %q", s)
	}
	return now.Add(-d), nil
}

// buildReviewFilter builds a ListFilter from request query parameters.
// Supported: ?since=7d (period), no other params yet.
func buildReviewFilter(r *http.Request) store.ListFilter {
	f := store.ListFilter{Statuses: []string{store.StatusSkipped}}
	if since := r.URL.Query().Get("since"); since != "" {
		t, err := ParseSinceWindow(since, time.Now())
		if err == nil && !t.IsZero() {
			f.CreatedAfter = t
		}
	}
	return f
}

// newReviewHandler returns the GET /review handler.
func newReviewHandler(renderer Renderer, provider ReviewProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		snap, err := provider.ReviewSnapshot(r.Context(), buildReviewFilter(r))
		if err != nil {
			http.Error(w, reviewLoadFailedMessage, http.StatusInternalServerError)
			return
		}
		page := newReviewPageData(snap)

		if renderErr := renderer.Render(w, "review.html", page); renderErr != nil {
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	})
}

// newReviewAPIHandler returns GET /api/review/skipped: a JSON array of
// skipped tasks. Supports the same ?since= query parameter as GET /review.
func newReviewAPIHandler(provider ReviewProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		snap, err := provider.ReviewSnapshot(r.Context(), buildReviewFilter(r))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "review data unavailable")
			return
		}

		type apiTask struct {
			ID             int64  `json:"id"`
			Source         string `json:"source"`
			Title          string `json:"title"`
			JudgmentReason string `json:"judgment_reason,omitempty"`
			Status         string `json:"status"`
			CreatedAt      string `json:"created_at,omitempty"`
		}
		out := make([]apiTask, 0, len(snap.Tasks))
		for _, t := range snap.Tasks {
			at := ""
			if !t.CreatedAt.IsZero() {
				at = t.CreatedAt.UTC().Format(time.RFC3339)
			}
			out = append(out, apiTask{
				ID:             t.ID,
				Source:         t.Source,
				Title:          t.Title,
				JudgmentReason: t.JudgmentReason,
				Status:         t.Status,
				CreatedAt:      at,
			})
		}
		writeJSON(w, http.StatusOK, out)
	})
}
