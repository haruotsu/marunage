package web

import (
	"context"
	"fmt"
	"net/http"
)

// MetricsDailyCount is one day's task completion counts.
type MetricsDailyCount struct {
	Date   string `json:"date"`
	Done   int    `json:"done"`
	Failed int    `json:"failed"`
}

// MetricsSnapshot holds aggregated task metrics for the dashboard.
type MetricsSnapshot struct {
	TotalTasks  int
	ByStatus    map[string]int
	BySource    map[string]int
	SuccessRate float64
	AvgDuration float64
	DailyCounts []MetricsDailyCount
}

// MetricsProvider is the seam the metrics handlers consume.
type MetricsProvider interface {
	Snapshot(ctx context.Context) (MetricsSnapshot, error)
}

type noopMetricsProvider struct{}

func (noopMetricsProvider) Snapshot(_ context.Context) (MetricsSnapshot, error) {
	return MetricsSnapshot{
		ByStatus:    map[string]int{},
		BySource:    map[string]int{},
		DailyCounts: []MetricsDailyCount{},
	}, nil
}

const metricsLoadFailedMessage = "Metrics data unavailable. See daemon.log for details."

type metricsAPIResponse struct {
	TotalTasks         int                 `json:"total_tasks"`
	ByStatus           map[string]int      `json:"by_status"`
	BySource           map[string]int      `json:"by_source"`
	SuccessRate        float64             `json:"success_rate"`
	AvgDurationSeconds float64             `json:"avg_duration_seconds"`
	DailyCounts        []MetricsDailyCount `json:"daily_counts"`
}

// metricsPageData is the template payload for metrics.html.
type metricsPageData struct {
	TotalTasks         int
	ByStatus           map[string]int
	BySource           map[string]int
	SuccessRatePct     string // pre-formatted, e.g. "85.7%"
	AvgDurationSeconds string // pre-formatted, e.g. "320s"
	DailyCounts        []MetricsDailyCount
	LoadError          string
}

// newMetricsAPIHandler returns GET /api/metrics.
func newMetricsAPIHandler(provider MetricsProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		snap, err := provider.Snapshot(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "metrics data unavailable")
			return
		}
		byStatus := snap.ByStatus
		if byStatus == nil {
			byStatus = map[string]int{}
		}
		bySource := snap.BySource
		if bySource == nil {
			bySource = map[string]int{}
		}
		daily := snap.DailyCounts
		if daily == nil {
			daily = []MetricsDailyCount{}
		}
		writeJSON(w, http.StatusOK, metricsAPIResponse{
			TotalTasks:         snap.TotalTasks,
			ByStatus:           byStatus,
			BySource:           bySource,
			SuccessRate:        snap.SuccessRate,
			AvgDurationSeconds: snap.AvgDuration,
			DailyCounts:        daily,
		})
	})
}

// newMetricsHandler returns GET /metrics HTML page.
func newMetricsHandler(renderer Renderer, provider MetricsProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		snap, err := provider.Snapshot(r.Context())
		if err != nil {
			page := metricsPageData{
				LoadError: metricsLoadFailedMessage,
				ByStatus:  map[string]int{},
				BySource:  map[string]int{},
			}
			if renderErr := renderer.Render(w, "metrics.html", page); renderErr != nil {
				http.Error(w, "render failed", http.StatusInternalServerError)
			}
			return
		}
		byStatus := snap.ByStatus
		if byStatus == nil {
			byStatus = map[string]int{}
		}
		bySource := snap.BySource
		if bySource == nil {
			bySource = map[string]int{}
		}
		page := metricsPageData{
			TotalTasks:         snap.TotalTasks,
			ByStatus:           byStatus,
			BySource:           bySource,
			SuccessRatePct:     fmt.Sprintf("%.1f%%", snap.SuccessRate*100),
			AvgDurationSeconds: fmt.Sprintf("%.0fs", snap.AvgDuration),
			DailyCounts:        snap.DailyCounts,
		}
		if renderErr := renderer.Render(w, "metrics.html", page); renderErr != nil {
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	})
}
