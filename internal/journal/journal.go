package journal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrInvalidInterval is returned by Run when interval is non-positive.
var ErrInvalidInterval = errors.New("journal: interval must be > 0")

// Journal orchestrates periodic activity collection and journal writing.
type Journal struct {
	collectors []Collector
	writer     *Writer
	now        func() time.Time
	interval   time.Duration
}

// Option configures a Journal.
type Option func(*Journal)

// WithCollectors appends collectors to the Journal.
func WithCollectors(cs ...Collector) Option {
	return func(j *Journal) { j.collectors = append(j.collectors, cs...) }
}

// WithClock injects a deterministic clock for testing.
func WithClock(now func() time.Time) Option { return func(j *Journal) { j.now = now } }

// WithInterval sets the default collection interval (used for the initial
// "since" window when no checkpoint exists).
func WithInterval(d time.Duration) Option { return func(j *Journal) { j.interval = d } }

// New constructs a Journal backed by w. Panics when w is nil.
func New(w *Writer, opts ...Option) *Journal {
	if w == nil {
		panic("journal.New: writer must not be nil")
	}
	j := &Journal{
		writer:   w,
		now:      time.Now,
		interval: 30 * time.Minute,
	}
	for _, o := range opts {
		o(j)
	}
	return j
}

// Tick collects activity since the last checkpoint and appends an entry.
// When the last checkpoint is not before now, Tick is a no-op (dedup guard).
func (j *Journal) Tick(ctx context.Context) error {
	now := j.now()

	since, err := j.writer.LastCheckpoint()
	switch {
	case err == nil:
		if !now.After(since) {
			return nil // dedup: checkpoint is current or future
		}
	case errors.Is(err, ErrNoCheckpoint):
		since = now.Add(-j.interval)
	default:
		return fmt.Errorf("read checkpoint: %w", err)
	}

	var sections []Section
	for _, c := range j.collectors {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		items, cErr := c.Collect(ctx, since)
		if cErr != nil {
			slog.Warn("journal: collector error", "source", c.Name(), "err", cErr)
			continue
		}
		if len(items) > 0 {
			sections = append(sections, Section{
				Title: sectionTitle(c.Name()),
				Items: items,
			})
		}
	}

	if err := j.writer.Append(Entry{At: now, Sections: sections}); err != nil {
		return err
	}
	return j.writer.UpdateCheckpoint(now)
}

// Run fires Tick immediately and then on every interval until ctx is done.
// Returns ErrInvalidInterval when interval is non-positive.
// Per-tick errors are logged and swallowed so the loop keeps running.
func (j *Journal) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("%w: got %v", ErrInvalidInterval, interval)
	}
	if err := j.Tick(ctx); err != nil && ctx.Err() == nil {
		slog.Error("journal: tick failed", "err", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := j.Tick(ctx); err != nil && ctx.Err() == nil {
				slog.Error("journal: tick failed", "err", err)
			}
		}
	}
}

// sectionTitle maps a known collector name to a human-readable Markdown heading.
// Unknown names are returned as-is so third-party collectors work out of the box.
func sectionTitle(name string) string {
	switch name {
	case "git":
		return "Git Activity"
	case "github":
		return "GitHub Activity"
	case "marunage":
		return "Completed Tasks"
	default:
		return name
	}
}
