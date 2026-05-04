// adapter.go bridges the Plugin Go API to the generic source.Plugin
// contract. The adapter is intentionally thin: it forwards List / Setup /
// AuthStatus to the inner Plugin and only translates Event into
// source.Task. Crucially it does NOT add Add / Complete / Delete / Since
// methods so the runtime type assertions inside the Discovery dispatcher
// (PR-71) cannot accidentally route a mutating call to a read-only source.
package calendar

import (
	"context"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// Adapter wraps a *Plugin and exposes it as a source.Plugin. The struct
// holds a pointer so the adapter and any direct caller of the inner Plugin
// share the same Client and clock.
type Adapter struct {
	inner *Plugin
}

// NewAdapter wraps p. p MUST be a fully-configured *Plugin (typically from
// New(WithClient(...))); we deliberately do not accept Option values here
// so the configuration knobs stay on the inner type and the adapter cannot
// drift out of sync with them.
func NewAdapter(p *Plugin) *Adapter {
	return &Adapter{inner: p}
}

// Name reports the canonical plugin identifier.
func (a *Adapter) Name() string { return pluginName }

// List forwards to the inner Plugin and converts every surviving Event
// (declined entries already filtered) into a source.Task.
func (a *Adapter) List(ctx context.Context) ([]source.Task, error) {
	events, err := a.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]source.Task, len(events))
	for i, ev := range events {
		out[i] = convertEvent(ev)
	}
	return out, nil
}

// Setup forwards opts to the inner Plugin's Setup. The adapter does not
// translate opts because source.SetupOptions is the same type the inner
// Plugin accepts.
func (a *Adapter) Setup(ctx context.Context, opts source.SetupOptions) error {
	return a.inner.Setup(ctx, opts)
}

// AuthStatus forwards to the inner Plugin.
func (a *Adapter) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return a.inner.AuthStatus(ctx)
}

// convertEvent is the calendar.Event -> source.Task adapter. Mapping:
//
//	Source           = "calendar"
//	ExternalID       <- Event.ID
//	Title            <- Event.Summary
//	Body             <- Event.Description
//	SourcePath       <- Event.HTMLLink
//	Done             = false (calendar is read-only)
//	RawMetadata      = {all_day, attendee_status, calendar_id?, location?,
//	                    + start/end OR start_date/end_date depending on AllDay}
//
// Empty optional fields (location, calendar_id) are pruned so RawMetadata
// stays diffable in `marunage discover` JSON output. Required fields are
// always present even when zero so a downstream consumer can rely on the
// keyset without nil-check ceremony.
func convertEvent(ev Event) source.Task {
	meta := map[string]any{
		"all_day":         ev.AllDay,
		"attendee_status": ev.AttendeeStatus,
	}
	if ev.AllDay {
		meta["start_date"] = ev.AllDayStart
		meta["end_date"] = ev.AllDayEnd
	} else {
		// Skip zero-value times rather than emitting
		// "0001-01-01T00:00:00Z" — a malformed upstream payload that
		// reaches the adapter without dateTime should not look like a
		// year-1 event in the queue.
		if !ev.StartDateTime.IsZero() {
			meta["start"] = ev.StartDateTime.Format(time.RFC3339)
		}
		if !ev.EndDateTime.IsZero() {
			meta["end"] = ev.EndDateTime.Format(time.RFC3339)
		}
	}
	if ev.Location != "" {
		meta["location"] = ev.Location
	}
	if ev.CalendarID != "" {
		meta["calendar_id"] = ev.CalendarID
	}
	return source.Task{
		Source:      pluginName,
		ExternalID:  ev.ID,
		Title:       ev.Summary,
		Body:        ev.Description,
		SourcePath:  ev.HTMLLink,
		Done:        false,
		RawMetadata: meta,
	}
}
