package web

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// fakeDashboardStore is the in-memory test double that lets us pin the
// provider's contract without standing up SQLite. The fields are
// pre-filled fixtures the test cases assemble.
type fakeDashboardStore struct {
	running          []DashboardRunning
	pendingTop       []DashboardPending
	pendingTotal     int
	recent           DashboardRecent
	sourceCheckpoint map[string]time.Time
	gotPreviewBytes  int
	gotPendingLimit  int
	gotRecentSince   time.Time
	err              error
}

func (f *fakeDashboardStore) Running(_ context.Context, _ int, previewBytes int) ([]DashboardRunning, error) {
	f.gotPreviewBytes = previewBytes
	return f.running, f.err
}

func (f *fakeDashboardStore) PendingTop(_ context.Context, limit int) ([]DashboardPending, error) {
	f.gotPendingLimit = limit
	return f.pendingTop, f.err
}

func (f *fakeDashboardStore) PendingCount(_ context.Context) (int, error) {
	return f.pendingTotal, f.err
}

func (f *fakeDashboardStore) Recent(_ context.Context, since time.Time) (DashboardRecent, error) {
	f.gotRecentSince = since
	return f.recent, f.err
}

func (f *fakeDashboardStore) SourceCheckpoints(_ context.Context) (map[string]time.Time, error) {
	return f.sourceCheckpoint, f.err
}

// fakeSourceLister implements DashboardSourceLister so we can pin the
// provider's mapping of registry → source rows without registering real
// plugins.
type fakeSourceLister struct {
	names    []string
	statuses map[string]source.AuthStatus
	errs     map[string]error
}

func (f *fakeSourceLister) Names() []string { return f.names }

func (f *fakeSourceLister) AuthStatus(_ context.Context, name string) (source.AuthStatus, error) {
	if err := f.errs[name]; err != nil {
		return "", err
	}
	return f.statuses[name], nil
}

func TestDashboardProvider_RunningCarriesOutputBudgetAndOrdering(t *testing.T) {
	startedFirst := time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC)
	startedSecond := startedFirst.Add(2 * time.Minute)

	store := &fakeDashboardStore{
		running: []DashboardRunning{
			{ID: 1, Source: "markdown", Title: "first", WS: "workspace:101", StartedAt: startedFirst, OutputPreview: "hello"},
			{ID: 2, Source: "markdown", Title: "second", WS: "workspace:102", StartedAt: startedSecond, OutputPreview: "world"},
		},
	}
	prov := NewDashboardProvider(store, &fakeSourceLister{}, DashboardOptions{
		PreviewBytes: 64,
		Now:          func() time.Time { return startedSecond.Add(time.Minute) },
	})

	snap, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got, want := len(snap.Running), 2; got != want {
		t.Fatalf("running len = %d; want %d", got, want)
	}
	if store.gotPreviewBytes != 64 {
		t.Errorf("preview bytes pass-through = %d; want 64", store.gotPreviewBytes)
	}
	if snap.Running[0].WS != "workspace:101" || snap.Running[1].WS != "workspace:102" {
		t.Errorf("running WS not preserved in order: %#v", snap.Running)
	}
}

func TestDashboardProvider_PendingSplitsTopAndCount(t *testing.T) {
	store := &fakeDashboardStore{
		pendingTop: []DashboardPending{
			{ID: 11, Source: "markdown", Title: "high", Priority: 5, CreatedAt: time.Date(2026, 5, 4, 7, 0, 0, 0, time.UTC)},
		},
		pendingTotal: 42,
	}
	prov := NewDashboardProvider(store, &fakeSourceLister{}, DashboardOptions{
		PendingLimit: 10,
		Now:          func() time.Time { return time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC) },
	})

	snap, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if store.gotPendingLimit != 10 {
		t.Errorf("pending limit pass-through = %d; want 10", store.gotPendingLimit)
	}
	if snap.PendingCount != 42 {
		t.Errorf("pending count = %d; want 42", snap.PendingCount)
	}
	if len(snap.Pending) != 1 || snap.Pending[0].ID != 11 {
		t.Errorf("pending top = %#v; want one row id=11", snap.Pending)
	}
}

func TestDashboardProvider_RecentUsesWindowFromClock(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	store := &fakeDashboardStore{
		recent: DashboardRecent{
			DoneCount:    3,
			FailedCount:  1,
			SkippedCount: 2,
			BySource: []DashboardSourceCount{
				{Source: "markdown", Done: 3, Failed: 1, Skipped: 2},
			},
		},
	}
	prov := NewDashboardProvider(store, &fakeSourceLister{}, DashboardOptions{
		Window: 24 * time.Hour,
		Now:    func() time.Time { return now },
	})

	snap, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	wantSince := now.Add(-24 * time.Hour)
	if !store.gotRecentSince.Equal(wantSince) {
		t.Errorf("recent since = %v; want %v", store.gotRecentSince, wantSince)
	}
	if snap.Recent24h.DoneCount != 3 || snap.Recent24h.FailedCount != 1 || snap.Recent24h.SkippedCount != 2 {
		t.Errorf("recent counts = %+v; want done=3 failed=1 skipped=2", snap.Recent24h)
	}
	if len(snap.Recent24h.BySource) != 1 || snap.Recent24h.BySource[0].Source != "markdown" {
		t.Errorf("recent by-source = %#v; want single markdown row", snap.Recent24h.BySource)
	}
}

func TestDashboardProvider_SourcesMergeAuthAndCheckpoint(t *testing.T) {
	now := time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC)
	store := &fakeDashboardStore{
		sourceCheckpoint: map[string]time.Time{
			// Raw kv_state keys; the provider attributes them
			// to source rows via prefix-against-registered-name.
			"markdown:mtime:/tmp/todo.md": now.Add(-30 * time.Minute),
			"gmail_last_id":               now.Add(-2 * time.Hour),
		},
	}
	lister := &fakeSourceLister{
		names: []string{"gmail", "markdown"},
		statuses: map[string]source.AuthStatus{
			"markdown": source.AuthAuthenticated,
			"gmail":    source.AuthExpired,
		},
	}
	prov := NewDashboardProvider(store, lister, DashboardOptions{
		Now: func() time.Time { return now },
	})

	snap, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got, want := len(snap.Sources), 2; got != want {
		t.Fatalf("sources len = %d; want %d", got, want)
	}
	// Sorted by name for stable rendering across refreshes.
	if snap.Sources[0].Name != "gmail" || snap.Sources[1].Name != "markdown" {
		t.Errorf("sources not sorted: %#v", snap.Sources)
	}
	if snap.Sources[0].AuthStatus != string(source.AuthExpired) {
		t.Errorf("gmail auth = %q; want %q", snap.Sources[0].AuthStatus, source.AuthExpired)
	}
	if !snap.Sources[1].LastListedAt.Equal(now.Add(-30 * time.Minute)) {
		t.Errorf("markdown last listed = %v; want %v", snap.Sources[1].LastListedAt, now.Add(-30*time.Minute))
	}
}

func TestDashboardProvider_SourceAuthErrorBecomesUnknown(t *testing.T) {
	store := &fakeDashboardStore{}
	lister := &fakeSourceLister{
		names:    []string{"gmail"},
		statuses: map[string]source.AuthStatus{},
		errs:     map[string]error{"gmail": errors.New("boom")},
	}
	prov := NewDashboardProvider(store, lister, DashboardOptions{
		Now: func() time.Time { return time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC) },
	})

	snap, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot returned %v; auth probe errors should not fail the dashboard", err)
	}
	if snap.Sources[0].AuthStatus != "unknown" {
		t.Errorf("auth status on error = %q; want %q", snap.Sources[0].AuthStatus, "unknown")
	}
}

// TestDashboardProvider_SourcesAttributeMultiUnderscoreNames pins the
// per-source checkpoint attribution against source names that contain
// an underscore (the `google_tasks` adapter is the canonical example).
// A naive split-on-first-`_` would credit "google_tasks_last_id" to a
// non-existent "google" source and leave the real google_tasks row at
// "never".
func TestDashboardProvider_SourcesAttributeMultiUnderscoreNames(t *testing.T) {
	now := time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC)
	store := &fakeDashboardStore{
		sourceCheckpoint: map[string]time.Time{
			"google_tasks_last_id": now.Add(-15 * time.Minute),
		},
	}
	lister := &fakeSourceLister{
		names: []string{"google_tasks"},
		statuses: map[string]source.AuthStatus{
			"google_tasks": source.AuthAuthenticated,
		},
	}
	prov := NewDashboardProvider(store, lister, DashboardOptions{
		Now: func() time.Time { return now },
	})
	snap, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Sources) != 1 {
		t.Fatalf("sources len = %d; want 1", len(snap.Sources))
	}
	if got := snap.Sources[0].LastListedAt; !got.Equal(now.Add(-15 * time.Minute)) {
		t.Errorf("google_tasks LastListedAt = %v; want %v", got, now.Add(-15*time.Minute))
	}
}

func TestDashboardProvider_GeneratedAtFromInjectedClock(t *testing.T) {
	now := time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC)
	prov := NewDashboardProvider(&fakeDashboardStore{}, &fakeSourceLister{}, DashboardOptions{
		Now: func() time.Time { return now },
	})

	snap, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snap.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v; want %v", snap.GeneratedAt, now)
	}
}

// TestCheckpointKeyBelongsTo pins the kv_state attribution helper
// directly so the prefix-match table is regression-locked without
// having to thread every edge case through the provider's six other
// store calls.
func TestCheckpointKeyBelongsTo(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want bool
	}{
		{"markdown", "markdown:mtime:/tmp/foo.md", true},
		{"markdown", "markdown_last_id", true},
		{"markdown", "markdown", true},
		{"markdown", "markdownish", false},
		{"markdown", "other:markdown", false},
		{"google_tasks", "google_tasks_last_id", true},
		{"google_tasks", "google_tasks:cursor", true},
		{"google_tasks", "google_last_id", false},
		{"gmail", "", false},
		{"gmail", "gma", false},
	}
	for _, c := range cases {
		got := checkpointKeyBelongsTo(c.key, c.name)
		if got != c.want {
			t.Errorf("checkpointKeyBelongsTo(%q, %q) = %v; want %v", c.key, c.name, got, c.want)
		}
	}
}

func TestNoopDashboardProviderReturnsEmptySnapshot(t *testing.T) {
	prov := noopDashboardProvider{now: func() time.Time { return time.Unix(42, 0).UTC() }}
	snap, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Running) != 0 || len(snap.Pending) != 0 || snap.PendingCount != 0 {
		t.Errorf("noop snapshot not empty: %#v", snap)
	}
	if !snap.GeneratedAt.Equal(time.Unix(42, 0).UTC()) {
		t.Errorf("noop GeneratedAt = %v; want injected time", snap.GeneratedAt)
	}
}

func TestDashboardProvider_StoreErrorPropagates(t *testing.T) {
	prov := NewDashboardProvider(&fakeDashboardStore{err: errors.New("store down")}, &fakeSourceLister{}, DashboardOptions{
		Now: func() time.Time { return time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC) },
	})
	if _, err := prov.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot returned nil error; want store error to propagate")
	}
}
