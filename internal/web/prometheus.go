package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// formatPrometheus converts a MetricsSnapshot into Prometheus text format.
// Keys in ByStatus and BySource are sorted to produce deterministic output.
func formatPrometheus(snap MetricsSnapshot) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# HELP marunage_tasks_total Total number of tasks\n")
	fmt.Fprintf(&b, "# TYPE marunage_tasks_total gauge\n")
	fmt.Fprintf(&b, "marunage_tasks_total %d\n", snap.TotalTasks)

	fmt.Fprintf(&b, "# HELP marunage_tasks_by_status Number of tasks by status\n")
	fmt.Fprintf(&b, "# TYPE marunage_tasks_by_status gauge\n")
	for _, status := range sortedKeys(snap.ByStatus) {
		fmt.Fprintf(&b, "marunage_tasks_by_status{status=%q} %d\n", status, snap.ByStatus[status])
	}

	fmt.Fprintf(&b, "# HELP marunage_tasks_by_source Number of tasks by source\n")
	fmt.Fprintf(&b, "# TYPE marunage_tasks_by_source gauge\n")
	for _, src := range sortedKeys(snap.BySource) {
		fmt.Fprintf(&b, "marunage_tasks_by_source{source=%q} %d\n", src, snap.BySource[src])
	}

	fmt.Fprintf(&b, "# HELP marunage_task_success_rate Task success rate\n")
	fmt.Fprintf(&b, "# TYPE marunage_task_success_rate gauge\n")
	fmt.Fprintf(&b, "marunage_task_success_rate %g\n", snap.SuccessRate)

	fmt.Fprintf(&b, "# HELP marunage_task_avg_duration_seconds Average task duration in seconds\n")
	fmt.Fprintf(&b, "# TYPE marunage_task_avg_duration_seconds gauge\n")
	fmt.Fprintf(&b, "marunage_task_avg_duration_seconds %g\n", snap.AvgDuration)

	return b.String()
}

// sortedKeys returns the keys of m in sorted order.
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// newPrometheusHandler returns GET /prometheus — Prometheus text format.
func newPrometheusHandler(provider MetricsProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		snap, err := provider.Snapshot(r.Context())
		if err != nil {
			http.Error(w, "metrics data unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", prometheusContentType)
		fmt.Fprint(w, formatPrometheus(snap))
	})
}

// acceptsTextPlain reports whether the Accept header explicitly includes text/plain.
// */* is intentionally excluded so browser requests (which add */* by default) keep
// getting the HTML dashboard.
func acceptsTextPlain(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	for _, part := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if mediaType == "text/plain" {
			return true
		}
	}
	return false
}
