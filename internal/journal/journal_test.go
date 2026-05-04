package journal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCollector is a test double for Collector.
type fakeCollector struct {
	mu    sync.Mutex
	name  string
	items []Item
	err   error
	calls int
}

func (f *fakeCollector) Name() string { return f.name }

func (f *fakeCollector) Collect(_ context.Context, _ time.Time) ([]Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.items, f.err
}

// --- Journal.Tick tests ---

func TestJournalTickWritesEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	now := time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)
	fc := &fakeCollector{
		name:  "git",
		items: []Item{{Text: "feat: add thing"}},
	}
	j := New(w, WithCollectors(fc), WithClock(func() time.Time { return now }))

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "2026-05-04.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "feat: add thing") {
		t.Errorf("journal file missing expected item, got:\n%s", string(data))
	}
}

func TestJournalTickUpdatesCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	now := time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)
	j := New(w, WithCollectors(&fakeCollector{name: "git"}), WithClock(func() time.Time { return now }))

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := w.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if !got.Equal(now) {
		t.Errorf("checkpoint = %v, want %v", got, now)
	}
}

func TestJournalTickDedupSameTimestamp(t *testing.T) {
	// Two Ticks at the same instant must not produce duplicate entries.
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	now := time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)
	fc := &fakeCollector{
		name:  "git",
		items: []Item{{Text: "feat: add thing"}},
	}
	j := New(w, WithCollectors(fc), WithClock(func() time.Time { return now }))

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "2026-05-04.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Count occurrences of the header to detect duplicates.
	count := strings.Count(string(data), "## 2026-05-04 14:30")
	if count != 1 {
		t.Errorf("expected exactly 1 entry, found %d:\n%s", count, string(data))
	}
}

func TestJournalTickAdvancingClock(t *testing.T) {
	// Two Ticks with advancing clock must both write.
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	clocks := []time.Time{
		time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC),
	}
	idx := 0
	mu := sync.Mutex{}
	nowFn := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := clocks[idx]
		if idx < len(clocks)-1 {
			idx++
		}
		return t
	}

	fc := &fakeCollector{name: "git", items: []Item{{Text: "feat: thing"}}}
	j := New(w, WithCollectors(fc), WithClock(nowFn))

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "2026-05-04.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "## 2026-05-04 14:00") {
		t.Errorf("missing first entry")
	}
	if !strings.Contains(content, "## 2026-05-04 14:30") {
		t.Errorf("missing second entry")
	}
}

func TestJournalTickCollectorErrorContinues(t *testing.T) {
	// A failing collector should not prevent others from running.
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	now := time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)
	failing := &fakeCollector{name: "fail", err: errors.New("broken")}
	ok := &fakeCollector{name: "git", items: []Item{{Text: "feat: ok"}}}

	j := New(w, WithCollectors(failing, ok), WithClock(func() time.Time { return now }))

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "2026-05-04.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "feat: ok") {
		t.Errorf("ok collector output missing from journal")
	}
}

// --- Journal.Run tests ---

func TestJournalRunCallsTickMultipleTimes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	var mu sync.Mutex
	tick := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	nowFn := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := tick
		tick = tick.Add(time.Hour)
		return t
	}

	fc := &fakeCollector{name: "git", items: []Item{{Text: "x"}}}
	j := New(w, WithCollectors(fc), WithClock(nowFn))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = j.Run(ctx, 10*time.Millisecond)

	fc.mu.Lock()
	calls := fc.calls
	fc.mu.Unlock()
	if calls < 2 {
		t.Errorf("expected >= 2 Tick calls, got %d", calls)
	}
}

func TestJournalRunStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	var mu sync.Mutex
	tick := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	nowFn := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := tick
		tick = tick.Add(time.Hour)
		return t
	}

	j := New(w, WithCollectors(&fakeCollector{name: "git"}), WithClock(nowFn))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := j.Run(ctx, time.Hour)
	if err != nil {
		t.Errorf("Run returned error on ctx cancel: %v", err)
	}
}

func TestJournalRunZeroIntervalError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)
	j := New(w)

	err := j.Run(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error for interval=0, got nil")
	}
}

func TestJournalRunNegativeIntervalError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)
	j := New(w)

	err := j.Run(context.Background(), -1*time.Second)
	if err == nil {
		t.Fatal("expected error for negative interval, got nil")
	}
}
