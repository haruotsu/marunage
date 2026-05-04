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

	fmt.Fprintf(&b, "# HELP marunage_tasks Total number of tasks (current snapshot)\n")
	fmt.Fprintf(&b, "# TYPE marunage_tasks gauge\n")
	fmt.Fprintf(&b, "marunage_tasks %d\n", snap.TotalTasks)

	fmt.Fprintf(&b, "# HELP marunage_tasks_by_status Number of tasks by status\n")
	fmt.Fprintf(&b, "# TYPE marunage_tasks_by_status gauge\n")
	for _, status := range sortedKeys(snap.ByStatus) {
		fmt.Fprintf(&b, "marunage_tasks_by_status{status=\"%s\"} %d\n", prometheusLabelEscape(status), snap.ByStatus[status])
	}

	fmt.Fprintf(&b, "# HELP marunage_tasks_by_source Number of tasks by source\n")
	fmt.Fprintf(&b, "# TYPE marunage_tasks_by_source gauge\n")
	for _, src := range sortedKeys(snap.BySource) {
		fmt.Fprintf(&b, "marunage_tasks_by_source{source=\"%s\"} %d\n", prometheusLabelEscape(src), snap.BySource[src])
	}

	fmt.Fprintf(&b, "# HELP marunage_task_success_ratio Ratio of tasks completed successfully (0–1)\n")
	fmt.Fprintf(&b, "# TYPE marunage_task_success_ratio gauge\n")
	fmt.Fprintf(&b, "marunage_task_success_ratio %g\n", snap.SuccessRate)

	fmt.Fprintf(&b, "# HELP marunage_task_avg_duration_seconds Average task duration in seconds\n")
	fmt.Fprintf(&b, "# TYPE marunage_task_avg_duration_seconds gauge\n")
	fmt.Fprintf(&b, "marunage_task_avg_duration_seconds %g\n", snap.AvgDuration)

	return b.String()
}

// prometheusLabelEscape escapes a label value per Prometheus exposition format 0.0.4.
// Only \, " and newline are escaped; all other bytes pass through unchanged.
func prometheusLabelEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
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
		w.Header().Set("Content-Type", prometheusContentType)
		snap, err := provider.Snapshot(r.Context())
		if err != nil {
			http.Error(w, "metrics data unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, formatPrometheus(snap))
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
