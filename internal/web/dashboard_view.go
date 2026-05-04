package web

import "time"

// dashboardView is the template-facing flattening of
// DashboardSnapshot. Splitting "what the provider returned" from
// "what the template iterates over" keeps the html/template usage
// cheap (no method calls in the template, every field is a plain
// string / int / slice) and stops the snapshot type from sprouting
// rendering concerns it should not own.
type dashboardView struct {
	GeneratedAt    string
	GeneratedRel   string
	Running        []dashboardRunningView
	Pending        []dashboardPendingView
	PendingCount   int
	Recent24h      DashboardRecent
	Recent24hTotal int
	Sources        []dashboardSourceView
}

type dashboardRunningView struct {
	ID            int64
	Source        string
	Title         string
	WS            string
	StartedAt     string
	StartedRel    string
	OutputPreview string
}

type dashboardPendingView struct {
	ID         int64
	Source     string
	Title      string
	Priority   int
	CreatedAt  string
	CreatedRel string
}

type dashboardSourceView struct {
	Name          string
	AuthStatus    string
	AuthBadge     string
	LastListedAt  string
	LastListedRel string
}

const dashboardDisplayLayout = "2006-01-02 15:04 MST"

func newDashboardView(snap DashboardSnapshot) dashboardView {
	now := snap.GeneratedAt
	if now.IsZero() {
		now = time.Now()
	}
	v := dashboardView{
		GeneratedAt:    now.UTC().Format(dashboardDisplayLayout),
		GeneratedRel:   "just now",
		PendingCount:   snap.PendingCount,
		Recent24h:      snap.Recent24h,
		Recent24hTotal: snap.Recent24h.DoneCount + snap.Recent24h.FailedCount + snap.Recent24h.SkippedCount,
	}
	for _, r := range snap.Running {
		v.Running = append(v.Running, dashboardRunningView{
			ID:            r.ID,
			Source:        r.Source,
			Title:         r.Title,
			WS:            r.WS,
			StartedAt:     formatDisplayTime(r.StartedAt),
			StartedRel:    formatRelative(now, r.StartedAt),
			OutputPreview: r.OutputPreview,
		})
	}
	for _, p := range snap.Pending {
		v.Pending = append(v.Pending, dashboardPendingView{
			ID:         p.ID,
			Source:     p.Source,
			Title:      p.Title,
			Priority:   p.Priority,
			CreatedAt:  formatDisplayTime(p.CreatedAt),
			CreatedRel: formatRelative(now, p.CreatedAt),
		})
	}
	for _, src := range snap.Sources {
		v.Sources = append(v.Sources, dashboardSourceView{
			Name:          src.Name,
			AuthStatus:    src.AuthStatus,
			AuthBadge:     authBadgeClass(src.AuthStatus),
			LastListedAt:  formatDisplayTime(src.LastListedAt),
			LastListedRel: formatRelative(now, src.LastListedAt),
		})
	}
	return v
}

func formatDisplayTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(dashboardDisplayLayout)
}

// authBadgeClass maps the four documented auth states (plus the
// "unknown" sentinel) to a short CSS class name the template uses to
// colour the badge. Unknown statuses fall through to the "neutral"
// class so a future plugin emitting an unforeseen value still
// renders.
func authBadgeClass(status string) string {
	switch status {
	case "authenticated":
		return "ok"
	case "expired":
		return "warn"
	case "revoked":
		return "bad"
	case "not_configured":
		return "neutral"
	default:
		return "neutral"
	}
}
