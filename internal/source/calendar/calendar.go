// Package calendar implements the Google Calendar Discovery source promised
// in docs/requirement.md row #3 of the standard sources table and detailed
// in PR-81 of pr_split_plan.md. The plugin is read-only: it surfaces today's
// events through the source.Plugin contract (List / Setup / AuthStatus) and
// intentionally does not implement Adder / Completer / Deleter so callers
// cannot accidentally mutate calendar state through the Discovery layer.
//
// External API isolation: the plugin talks to Google Calendar through the
// Client interface, not the SDK directly. PR-81 ships a gws-CLI-backed
// Client (gws_client.go) for production and lets unit tests inject a fake.
// Keeping the seam this narrow means the day-boundary, declined-event, and
// all-day-vs-regular logic can be exercised offline without touching the
// network or shelling out to gws.
//
// Day boundary: requirement.md PR-81 mandates that the next-day rollover
// happens at midnight in `time.Now().Local()`. The plugin computes
// `[startOfDay(now), startOfDay(now)+24h)` on every List call, so a long-
// running daemon naturally rolls over at midnight without needing to be
// restarted.
package calendar

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// pluginName is the canonical identifier the registry dispatches under and
// the value Adapter stamps onto every emitted source.Task. Kept unexported
// — the CLI integration (PR-71+) goes through Plugin.Name() / Manifest()
// rather than this literal, mirroring the markdown source's pattern.
const pluginName = "calendar"

// AttendeeDeclined is the Google Calendar v3 responseStatus value the plugin
// filters out before returning events. requirement.md treats declined
// invites as "the user actively said no" and would not surface them as
// today's tasks. Other statuses (accepted, tentative, needsAction, "") are
// kept — the plugin does not pre-judge them.
const AttendeeDeclined = "declined"

// EventCancelled is the Google Calendar v3 event.status value the plugin
// filters out. With singleEvents=true the upstream surfaces removed
// exceptions of recurring events as Status="cancelled" items; passing
// them through would leave the queue with rows the user already deleted
// from the calendar and no good way to mark them done.
const EventCancelled = "cancelled"

// ErrClientRequired is returned by List / Setup when the Plugin was
// constructed without a Client. AuthStatus does not return this error — it
// downgrades to AuthNotConfigured instead, which is the more useful signal
// for `marunage doctor` style status checks.
var ErrClientRequired = errors.New("calendar: client required")

// Event is the Discovery-side neutral view of one calendar entry. It is a
// superset of what the source.Task layer needs so the Adapter can choose
// which fields to expose in RawMetadata without forcing the Client to
// pre-format them. The struct is plain data — no methods — so the gws
// JSON parser, the fake test client, and any future SDK-backed Client can
// all populate the same shape.
type Event struct {
	// ID is the Google Calendar event id. Becomes source.Task.ExternalID
	// verbatim — the (source, external_id) UNIQUE index requirement.md
	// describes assumes ids are stable across List calls.
	ID string

	// Summary, Description, Location are the user-visible strings. Empty
	// fields are not pruned here; the Adapter decides what to drop.
	Summary     string
	Description string
	Location    string

	// HTMLLink is the calendar.google.com URL for the event. Becomes
	// source.Task.SourcePath so `marunage show` can deep-link.
	HTMLLink string

	// CalendarID is the calendar this event belongs to ("primary" for the
	// authenticated user's calendar). Phase 1 only reads from "primary";
	// the field exists so a future multi-calendar follow-up does not need
	// a struct edit.
	CalendarID string

	// AllDay is true when the event is date-only (no specific time of
	// day). Regular events use StartDateTime/EndDateTime; all-day events
	// use AllDayStart/AllDayEnd. The two pairs are mutually exclusive.
	AllDay bool

	// StartDateTime / EndDateTime carry timed events. Zero when AllDay.
	StartDateTime time.Time
	EndDateTime   time.Time

	// AllDayStart / AllDayEnd carry the YYYY-MM-DD strings the Google API
	// emits for all-day events. End is exclusive (Google's convention)
	// and we preserve that semantics rather than subtracting a day, so
	// downstream display code can decide on its own representation.
	AllDayStart string
	AllDayEnd   string

	// AttendeeStatus is the responseStatus of the attendee whose `self`
	// field is true ("accepted" / "tentative" / "declined" /
	// "needsAction"). Empty when the user has no attendee record (e.g.
	// they own the event but were not invited as an attendee).
	AttendeeStatus string

	// Status mirrors the Google Calendar v3 event.status field
	// ("confirmed" / "tentative" / "cancelled"). Cancelled instances of
	// recurring events surface here when the upstream is queried with
	// singleEvents=true; the Plugin filters them out alongside declined
	// invites. Non-cancelled values are surfaced verbatim so a future
	// downstream consumer can distinguish "tentative event" from
	// "tentative attendee response".
	Status string
}

// Client is the seam between the Plugin core and the Google Calendar
// upstream. It is intentionally small: three methods, no exported types
// from any SDK. Production wiring (gws_client.go) implements this against
// the gws CLI; tests provide an in-memory fake. Callers that want to swap
// in google-api-go-client can write their own implementation without
// touching the Plugin or Adapter.
type Client interface {
	// ListEvents returns events whose [start, end) overlaps the
	// half-open window [timeMin, timeMax). The Plugin always passes a
	// 24-hour window aligned to local midnight; whether the upstream
	// pages or sorts the result is the implementation's concern.
	ListEvents(ctx context.Context, timeMin, timeMax time.Time) ([]Event, error)

	// Status reports the current credential state without performing any
	// mutating I/O. Returning a typed source.AuthStatus (rather than an
	// error sentinel) keeps the plugin from re-encoding the same four-
	// state vocabulary at every layer.
	Status(ctx context.Context) (source.AuthStatus, error)

	// Setup runs the calendar-side authentication / smoke-test flow. The
	// Client decides what "setup" means concretely (gws auth login, an
	// OAuth-local server, ...); the Plugin merely forwards opts so the
	// non-interactive path remains honour-bound at the boundary.
	Setup(ctx context.Context, opts source.SetupOptions) error
}

// Plugin is the read-only Calendar source. Construct one with New and pass
// at least WithClient before calling List / Setup. The struct is safe for
// concurrent use as long as the underlying Client is.
type Plugin struct {
	client Client
	now    func() time.Time
}

// Option is the functional-option shape New accepts. Mirrors the markdown
// source's pattern so the two stay visually consistent for reviewers
// flipping between them.
type Option func(*Plugin)

// WithClient injects the Calendar Client. Mandatory for any operation that
// hits the upstream; AuthStatus is the only method that survives without
// it (downgrading to AuthNotConfigured).
func WithClient(c Client) Option {
	return func(p *Plugin) { p.client = c }
}

// WithClock overrides the clock the day-boundary computation reads. Tests
// pass a fixed-clock here so midnight rollover can be exercised without
// sleeping; production callers leave it alone and get time.Now.
func WithClock(now func() time.Time) Option {
	return func(p *Plugin) { p.now = now }
}

// New constructs a Plugin with the given options. now defaults to time.Now;
// the Client must be supplied via WithClient for any I/O method to succeed.
func New(opts ...Option) *Plugin {
	p := &Plugin{now: time.Now}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name reports the canonical plugin identifier.
func (p *Plugin) Name() string { return pluginName }

// List returns today's events with declined and cancelled entries
// filtered out. The window is [startOfDay(now), nextMidnight(now))
// computed in the local timezone of `now`. Both bounds are calendar-
// arithmetic results, NOT `timeMin + 24h`: on DST spring-forward days
// the wall-clock distance between two local midnights is 23 hours, and
// adding 24 hours would silently extend the agenda into the next day.
func (p *Plugin) List(ctx context.Context) ([]Event, error) {
	if p.client == nil {
		return nil, ErrClientRequired
	}
	now := p.now()
	timeMin := startOfDay(now)
	timeMax := nextMidnight(now)
	events, err := p.client.ListEvents(ctx, timeMin, timeMax)
	if err != nil {
		return nil, fmt.Errorf("calendar: list events: %w", err)
	}
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if ev.AttendeeStatus == AttendeeDeclined {
			continue
		}
		if ev.Status == EventCancelled {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// Setup forwards opts to the underlying Client. ErrClientRequired surfaces
// when no Client was wired so a misconfigured caller fails loudly instead
// of silently no-oping.
func (p *Plugin) Setup(ctx context.Context, opts source.SetupOptions) error {
	if p.client == nil {
		return ErrClientRequired
	}
	return p.client.Setup(ctx, opts)
}

// AuthStatus reports the credential state. With no Client wired it returns
// AuthNotConfigured (and a nil error) so `marunage doctor` can report
// "calendar source needs setup" without each caller having to special-case
// the missing-client error.
func (p *Plugin) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	if p.client == nil {
		return source.AuthNotConfigured, nil
	}
	return p.client.Status(ctx)
}

// startOfDay rounds t down to local midnight. Pulled out so the boundary
// rule has a single named definition the tests and the production caller
// share. We deliberately use t.Date() + time.Date with t.Location() rather
// than t.Truncate(24h): Truncate quantises in UTC, which would shift the
// boundary off local midnight in any non-zero offset.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// nextMidnight returns the local midnight that begins the day after t.
// `time.Date` normalises overflow (day=32 -> next month) so this works
// across month and year boundaries without an extra calendar dance. We
// use this rather than `startOfDay(t).Add(24 * time.Hour)` because that
// addition is wall-clock arithmetic: on DST spring-forward days it
// overshoots local midnight by an hour, and on fall-back days it
// undershoots. The agenda window must always be [today 00:00, tomorrow
// 00:00) regardless of DST.
func nextMidnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, t.Location())
}
