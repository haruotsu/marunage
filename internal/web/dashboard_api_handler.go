package web

import (
	"net/http"
	"time"
)

type dashboardAPIRunning struct {
	ID            int64     `json:"id"`
	Source        string    `json:"source"`
	Title         string    `json:"title"`
	WS            string    `json:"ws"`
	StartedAt     time.Time `json:"started_at"`
	OutputPreview string    `json:"output_preview"`
}

type dashboardAPIPending struct {
	ID        int64     `json:"id"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	Priority  int       `json:"priority"`
	CreatedAt time.Time `json:"created_at"`
}

type dashboardAPIRecent struct {
	DoneCount    int `json:"done_count"`
	FailedCount  int `json:"failed_count"`
	SkippedCount int `json:"skipped_count"`
}

type dashboardAPISource struct {
	Name         string    `json:"name"`
	AuthStatus   string    `json:"auth_status"`
	LastListedAt time.Time `json:"last_listed_at"`
}

type dashboardAPIResponse struct {
	GeneratedAt  time.Time             `json:"generated_at"`
	Running      []dashboardAPIRunning `json:"running"`
	Pending      []dashboardAPIPending `json:"pending"`
	PendingCount int                   `json:"pending_count"`
	Recent24h    dashboardAPIRecent    `json:"recent_24h"`
	Sources      []dashboardAPISource  `json:"sources"`
}

func newDashboardAPIHandler(provider DashboardProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		snap, err := provider.Snapshot(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "dashboard data unavailable")
			return
		}

		running := make([]dashboardAPIRunning, len(snap.Running))
		for i, r := range snap.Running {
			running[i] = dashboardAPIRunning{
				ID:            r.ID,
				Source:        r.Source,
				Title:         r.Title,
				WS:            r.WS,
				StartedAt:     r.StartedAt,
				OutputPreview: r.OutputPreview,
			}
		}

		pending := make([]dashboardAPIPending, len(snap.Pending))
		for i, p := range snap.Pending {
			pending[i] = dashboardAPIPending{
				ID:        p.ID,
				Source:    p.Source,
				Title:     p.Title,
				Priority:  p.Priority,
				CreatedAt: p.CreatedAt,
			}
		}

		sources := make([]dashboardAPISource, len(snap.Sources))
		for i, s := range snap.Sources {
			sources[i] = dashboardAPISource{
				Name:         s.Name,
				AuthStatus:   s.AuthStatus,
				LastListedAt: s.LastListedAt,
			}
		}

		writeJSON(w, http.StatusOK, dashboardAPIResponse{
			GeneratedAt:  snap.GeneratedAt,
			Running:      running,
			Pending:      pending,
			PendingCount: snap.PendingCount,
			Recent24h: dashboardAPIRecent{
				DoneCount:    snap.Recent24h.DoneCount,
				FailedCount:  snap.Recent24h.FailedCount,
				SkippedCount: snap.Recent24h.SkippedCount,
			},
			Sources: sources,
		})
	})
}
