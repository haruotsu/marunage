package web

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// DashboardSnapshot is the data the dashboard template renders. The
// fields mirror the four panels the Phase-1 brief calls out: running
// list, pending queue, 24-hour summary, and per-source discovery
// status. Carrying GeneratedAt lets the partial reload show how fresh
// the data is and lets tests pin the rendered timestamp.
type DashboardSnapshot struct {
	GeneratedAt  time.Time
	Running      []DashboardRunning
	Pending      []DashboardPending
	PendingCount int
	Recent24h    DashboardRecent
	Sources      []DashboardSource
}

// DashboardRunning is one row of the running list. WS carries the cmux
// workspace reference (e.g. "workspace:101") so the dashboard can show
// it next to the title per the PR-63 brief; OutputPreview is a
// truncated peek at the most recently captured output (PR-91 will
// replace this with a live stream).
type DashboardRunning struct {
	ID            int64
	Source        string
	Title         string
	WS            string
	StartedAt     time.Time
	OutputPreview string
}

// DashboardPending is one row of the pending top-N preview. Priority
// is exposed so the template can render the queue ordering.
type DashboardPending struct {
	ID        int64
	Source    string
	Title     string
	Priority  int
	CreatedAt time.Time
}

// DashboardRecent collects the 24-hour summary the brief asks for:
// totals plus per-source breakdown. BySource is sorted by source name
// so the rendered table is stable across refreshes.
type DashboardRecent struct {
	DoneCount    int
	FailedCount  int
	SkippedCount int
	BySource     []DashboardSourceCount
}

// DashboardSourceCount is one row of the per-source 24-hour breakdown.
type DashboardSourceCount struct {
	Source  string
	Done    int
	Failed  int
	Skipped int
}

// DashboardSource is one row of the source-status panel. AuthStatus
// is rendered as a string ("authenticated", "expired", ...) plus the
// sentinel "unknown" used when the plugin's AuthStatus probe returned
// an error — surfacing the failure on the dashboard rather than
// hiding it.
type DashboardSource struct {
	Name         string
	AuthStatus   string
	LastListedAt time.Time
}

// DashboardProvider is the seam handlers consume. NewDashboardProvider
// wires the production aggregator; tests inject a fake (or use the
// noop) so the handler tests stay focused on rendering.
type DashboardProvider interface {
	Snapshot(ctx context.Context) (DashboardSnapshot, error)
}

// DashboardStore is the narrow store-side surface the provider depends
// on. Production wires it via dashboardSQLStore; tests use a fake.
// previewBytes is passed to Running so the SQL implementation can
// truncate the body column on the database side rather than dragging
// full bodies into memory.
type DashboardStore interface {
	Running(ctx context.Context, limit, previewBytes int) ([]DashboardRunning, error)
	PendingTop(ctx context.Context, limit int) ([]DashboardPending, error)
	PendingCount(ctx context.Context) (int, error)
	Recent(ctx context.Context, since time.Time) (DashboardRecent, error)
	SourceCheckpoints(ctx context.Context) (map[string]time.Time, error)
}

// DashboardSourceLister is the registry surface the provider needs:
// the names it should display and a way to probe each plugin's
// AuthStatus. Mirrors the *source.Registry surface so production
// passes the registry directly via a thin adapter.
type DashboardSourceLister interface {
	Names() []string
	AuthStatus(ctx context.Context, name string) (source.AuthStatus, error)
}

// DashboardOptions configures NewDashboardProvider. Zero values fall
// back to documented defaults so callers only need to set the knobs
// they care about.
type DashboardOptions struct {
	// PendingLimit caps the rows in DashboardSnapshot.Pending. The
	// PendingCount field still reflects the unfiltered total.
	PendingLimit int
	// RunningLimit caps the running list. Defaults to 32 — generous
	// for a single-operator instance and below the SQLite expression
	// stack ceiling that an unbounded LIMIT could trip when combined
	// with later joins.
	RunningLimit int
	// PreviewBytes truncates the running output preview at the
	// database boundary. Zero means defaultPreviewBytes.
	PreviewBytes int
	// Window is the 24-hour summary lookback. Zero defaults to 24h.
	Window time.Duration
	// Now is the clock GeneratedAt and the Recent window cutoff
	// derive from. Zero defaults to time.Now.
	Now func() time.Time
}

const (
	defaultPendingLimit = 20
	defaultRunningLimit = 32
	defaultPreviewBytes = 240
	defaultWindow       = 24 * time.Hour
	authStatusUnknown   = "unknown"
)

// dashboardProvider is the production assembler. It pulls the four
// store-side projections and the registry-side source list, then
// composes them into a DashboardSnapshot.
type dashboardProvider struct {
	store        DashboardStore
	sources      DashboardSourceLister
	now          func() time.Time
	pendingLimit int
	runningLimit int
	previewBytes int
	window       time.Duration
}

// NewDashboardProvider wires a provider against a store + a source
// lister. Either argument may be nil at the type level; the
// production wiring always supplies both. A nil now is patched to
// time.Now so callers can leave it unset.
func NewDashboardProvider(store DashboardStore, sources DashboardSourceLister, opts DashboardOptions) DashboardProvider {
	if opts.PendingLimit <= 0 {
		opts.PendingLimit = defaultPendingLimit
	}
	if opts.RunningLimit <= 0 {
		opts.RunningLimit = defaultRunningLimit
	}
	if opts.PreviewBytes <= 0 {
		opts.PreviewBytes = defaultPreviewBytes
	}
	if opts.Window <= 0 {
		opts.Window = defaultWindow
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &dashboardProvider{
		store:        store,
		sources:      sources,
		now:          opts.Now,
		pendingLimit: opts.PendingLimit,
		runningLimit: opts.RunningLimit,
		previewBytes: opts.PreviewBytes,
		window:       opts.Window,
	}
}

func (p *dashboardProvider) Snapshot(ctx context.Context) (DashboardSnapshot, error) {
	now := p.now()
	snap := DashboardSnapshot{GeneratedAt: now}

	running, err := p.store.Running(ctx, p.runningLimit, p.previewBytes)
	if err != nil {
		return DashboardSnapshot{}, fmt.Errorf("dashboard: running: %w", err)
	}
	snap.Running = running

	pending, err := p.store.PendingTop(ctx, p.pendingLimit)
	if err != nil {
		return DashboardSnapshot{}, fmt.Errorf("dashboard: pending top: %w", err)
	}
	snap.Pending = pending

	count, err := p.store.PendingCount(ctx)
	if err != nil {
		return DashboardSnapshot{}, fmt.Errorf("dashboard: pending count: %w", err)
	}
	snap.PendingCount = count

	since := now.Add(-p.window)
	recent, err := p.store.Recent(ctx, since)
	if err != nil {
		return DashboardSnapshot{}, fmt.Errorf("dashboard: recent: %w", err)
	}
	snap.Recent24h = recent

	checkpoints, err := p.store.SourceCheckpoints(ctx)
	if err != nil {
		return DashboardSnapshot{}, fmt.Errorf("dashboard: source checkpoints: %w", err)
	}

	snap.Sources = p.buildSources(ctx, checkpoints)
	return snap, nil
}

func (p *dashboardProvider) buildSources(ctx context.Context, checkpoints map[string]time.Time) []DashboardSource {
	if p.sources == nil {
		return nil
	}
	names := append([]string(nil), p.sources.Names()...)
	sort.Strings(names)
	out := make([]DashboardSource, 0, len(names))
	for _, name := range names {
		row := DashboardSource{Name: name, LastListedAt: checkpoints[name]}
		status, err := p.sources.AuthStatus(ctx, name)
		if err != nil {
			row.AuthStatus = authStatusUnknown
		} else {
			row.AuthStatus = string(status)
		}
		out = append(out, row)
	}
	return out
}

// noopDashboardProvider is the fallback the server uses when a real
// provider has not been wired in. Returning an empty snapshot keeps
// existing handler tests passing without forcing them to mock a full
// data source.
type noopDashboardProvider struct {
	now func() time.Time
}

func (p noopDashboardProvider) Snapshot(_ context.Context) (DashboardSnapshot, error) {
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	return DashboardSnapshot{GeneratedAt: now}, nil
}

// RegistrySourceLister adapts a *source.Registry to
// DashboardSourceLister. Lives in the web package because it is the
// only consumer of the dashboard surface; keeping the adapter here
// avoids dragging registry-shaped types into the source package.
type RegistrySourceLister struct {
	Registry *source.Registry
}

func (l RegistrySourceLister) Names() []string {
	if l.Registry == nil {
		return nil
	}
	return l.Registry.Names()
}

func (l RegistrySourceLister) AuthStatus(ctx context.Context, name string) (source.AuthStatus, error) {
	if l.Registry == nil {
		return "", fmt.Errorf("dashboard: nil source registry")
	}
	plugin, err := l.Registry.Get(name)
	if err != nil {
		return "", err
	}
	return plugin.AuthStatus(ctx)
}

// FormatRelative renders a duration in a human-readable form for the
// dashboard (e.g. "2m ago", "3h ago"). Stays in the web package since
// it is only used by the template helpers.
func FormatRelative(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// truncatePreview cuts s to at most max bytes, falling on a UTF-8
// boundary so a multi-byte rune is not split across a render. Exposed
// for the SQL store implementation; the body column may already be
// shorter than max, in which case we return it unchanged.
func truncatePreview(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	out := strings.TrimRightFunc(s[:cut], func(r rune) bool { return r == '\n' || r == '\r' })
	return out
}
