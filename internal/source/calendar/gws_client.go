// gws_client.go is the production-side Client implementation. It shells
// out to the `gws` (Google Workspace CLI) binary that requirement.md
// designates as the delegate-cli for Google sources, parses the standard
// Google Calendar v3 events.list response, and translates the result into
// calendar.Event values the rest of the package consumes.
//
// Design choices:
//
//   - The runner is injectable (Runner type / WithRunner option). Tests
//     swap in a scripted runner so the JSON parsing and command-shape
//     assertions can run offline; production wires `exec.CommandContext`.
//   - We build the request through `--params <JSON>` (the exact arg shape
//     gws documents) rather than through repeated `--params-foo=bar`
//     flags. JSON keeps the Go side typed and mirrors what `gws schema`
//     advertises, which is also what google-api-go-client would accept on
//     a future swap.
//   - All-day events are recognised by the presence of a `date` field on
//     start/end (Google's convention); regular events use `dateTime`.
//   - The self attendee responseStatus is the only attendee field we
//     surface — that is what the Plugin filters declined invites on. The
//     rest of the attendee list does not matter at this layer.
package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// Runner is the function shape the GWS client uses to execute a binary.
// Pulling this out as a named type lets tests inject a recorder while
// production wires exec.CommandContext via DefaultRunner.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultRunner runs the binary via os/exec and returns its stdout. The
// command honours ctx (kill on cancel via exec.CommandContext) so a
// caller can bound the subprocess from outside. Errors are wrapped with
// the binary name; we deliberately do NOT include captured stderr in the
// error message because gws diagnostics can carry OAuth refresh tokens
// or PII (calendar id, attendee email) and the wrapped error frequently
// ends up in slog / audit logs. Callers who need the raw stderr for
// debugging can run gws directly.
func DefaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%s: %w (exit code %d)", name, err, exitErr.ExitCode())
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// GWSClient is the gws-CLI-backed Client. Construct with NewGWSClient and
// pass options to override the binary name (e.g. an absolute path) or the
// runner (tests).
type GWSClient struct {
	binary     string
	calendarID string
	runner     Runner
}

// GWSOption is the functional-option shape NewGWSClient accepts. Mirrors
// the Plugin's Option pattern so callers see one consistent style.
type GWSOption func(*GWSClient)

// WithBinary overrides the path to the gws binary. Defaults to "gws"
// (resolved through PATH). Useful for tests that want to point at a stub
// binary, or for deployments that ship a vendored gws.
func WithBinary(path string) GWSOption {
	return func(c *GWSClient) { c.binary = path }
}

// WithCalendarID overrides the calendar to query. Defaults to "primary"
// — the authenticated user's main calendar — which is what PR-81 needs.
// A future multi-calendar follow-up can pass a calendar id from
// calendarList.list output.
func WithCalendarID(id string) GWSOption {
	return func(c *GWSClient) { c.calendarID = id }
}

// WithRunner overrides the binary executor. Tests pass a scripted runner
// so they can assert on the exact (name, args) tuple the client built and
// inject canned JSON responses without spawning a process.
func WithRunner(r Runner) GWSOption {
	return func(c *GWSClient) { c.runner = r }
}

// NewGWSClient constructs a GWSClient with sensible defaults. The zero
// value is intentionally not usable: the runner is required, even if it
// is just DefaultRunner, so callers cannot accidentally produce a client
// that nil-panics on first use.
func NewGWSClient(opts ...GWSOption) *GWSClient {
	c := &GWSClient{
		binary:     "gws",
		calendarID: "primary",
		runner:     DefaultRunner,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ListEvents shells out to `gws calendar events list` with timeMin/timeMax
// and the canonical singleEvents=true / orderBy=startTime parameters that
// match Google's recommended query for a daily agenda. The response is the
// standard Calendar v3 events resource; we decode it into Event values.
func (c *GWSClient) ListEvents(ctx context.Context, timeMin, timeMax time.Time) ([]Event, error) {
	params := map[string]any{
		"calendarId":   c.calendarID,
		"singleEvents": true,
		"orderBy":      "startTime",
		"timeMin":      timeMin.Format(time.RFC3339),
		"timeMax":      timeMax.Format(time.RFC3339),
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("calendar gws: encode params: %w", err)
	}
	out, err := c.runner(ctx, c.binary, "calendar", "events", "list", "--params", string(paramsJSON), "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("calendar gws: events.list: %w", err)
	}
	var resp gwsEventsResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("calendar gws: decode events.list: %w", err)
	}
	events := make([]Event, 0, len(resp.Items))
	for _, raw := range resp.Items {
		ev, err := raw.toEvent(c.calendarID)
		if err != nil {
			return nil, fmt.Errorf("calendar gws: parse event %q: %w", raw.ID, err)
		}
		events = append(events, ev)
	}
	return events, nil
}

// Status runs a cheap read against the calendarList endpoint to verify the
// gws auth token works. Failure is interpreted as "not configured" rather
// than as a hard error: AuthStatus callers (`marunage doctor`,
// Plugin.AuthStatus) care about the typed state, not about the
// underlying gws stderr line. Callers that DO need the raw error (Setup
// interactive smoke test) should invoke probe directly.
func (c *GWSClient) Status(ctx context.Context) (source.AuthStatus, error) {
	if err := c.probe(ctx); err != nil {
		return source.AuthNotConfigured, nil
	}
	return source.AuthAuthenticated, nil
}

// probe runs the calendarList smoke test and returns the runner error
// verbatim. Pulled out so Status (which downgrades I/O failures to
// AuthNotConfigured) and Setup (which must surface them) can share one
// definition of "is gws + auth working right now?".
func (c *GWSClient) probe(ctx context.Context) error {
	params := map[string]any{"maxResults": 1}
	paramsJSON, _ := json.Marshal(params) // map[string]any with primitives cannot fail to marshal.
	_, err := c.runner(ctx, c.binary, "calendar", "calendarList", "list", "--params", string(paramsJSON), "--format", "json")
	return err
}

// Setup is intentionally a no-op shell-out today: gws owns its own auth
// flow and `gws auth login` is interactive. Forcing the user through gws
// keeps PR-81 from re-implementing OAuth and matches requirement.md's
// delegate-cli policy. A non-interactive caller gets a typed error that
// names the missing setup step rather than a silent success.
func (c *GWSClient) Setup(ctx context.Context, opts source.SetupOptions) error {
	if opts.NonInteractive {
		return fmt.Errorf("calendar: gws auth must already be configured (run `gws auth login` separately; non-interactive Setup cannot launch a browser flow)")
	}
	// Interactive path: run the calendarList smoke test directly and
	// surface the runner error verbatim. Going through Status would
	// collapse "gws binary missing" and "auth missing" into the same
	// AuthNotConfigured outcome, masking real environment problems
	// behind the "please run gws auth login" hint.
	if err := c.probe(ctx); err != nil {
		return fmt.Errorf("calendar: gws smoke test failed (run `gws auth login` and verify gws is on PATH): %w", err)
	}
	return nil
}

// gwsEventsResponse is the subset of the Google Calendar v3 events.list
// response the plugin cares about. Keeping it private and minimal means a
// future API field addition cannot accidentally break the parser, and a
// reader of this file sees exactly which keys we depend on.
type gwsEventsResponse struct {
	Items []gwsEvent `json:"items"`
}

// gwsEvent mirrors the relevant fields of the Calendar v3 event resource.
// Optional fields are pointers/strings that empty-out on absence; the
// toEvent method translates the on-wire shape into the package-public
// Event struct.
type gwsEvent struct {
	ID          string         `json:"id"`
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	Location    string         `json:"location"`
	HTMLLink    string         `json:"htmlLink"`
	Status      string         `json:"status"`
	Start       gwsTimeValue   `json:"start"`
	End         gwsTimeValue   `json:"end"`
	Attendees   []gwsAttendee  `json:"attendees"`
}

// gwsTimeValue is the start/end shape: `date` for all-day, `dateTime` for
// timed events. Google's docs guarantee at most one of the two is set per
// event (the other is omitted), so the parser uses date != "" as the
// all-day discriminator.
type gwsTimeValue struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// gwsAttendee carries only what the plugin needs: the self flag (so we
// can find the user's own response) and responseStatus.
type gwsAttendee struct {
	Email          string `json:"email"`
	Self           bool   `json:"self"`
	ResponseStatus string `json:"responseStatus"`
}

// toEvent converts the on-wire shape to the package-public Event. The
// calendarID argument lets the caller stamp the source calendar (the API
// response does not include it on each item) so a future multi-calendar
// path can populate Event.CalendarID without losing fidelity.
func (g gwsEvent) toEvent(calendarID string) (Event, error) {
	ev := Event{
		ID:          g.ID,
		Summary:     g.Summary,
		Description: g.Description,
		Location:    g.Location,
		HTMLLink:    g.HTMLLink,
		CalendarID:  calendarID,
		Status:      g.Status,
	}
	switch {
	case g.Start.DateTime != "":
		t, err := time.Parse(time.RFC3339, g.Start.DateTime)
		if err != nil {
			return Event{}, fmt.Errorf("start dateTime: %w", err)
		}
		ev.StartDateTime = t
	case g.Start.Date != "":
		ev.AllDay = true
		ev.AllDayStart = g.Start.Date
	}
	switch {
	case g.End.DateTime != "":
		t, err := time.Parse(time.RFC3339, g.End.DateTime)
		if err != nil {
			return Event{}, fmt.Errorf("end dateTime: %w", err)
		}
		ev.EndDateTime = t
	case g.End.Date != "":
		ev.AllDay = true
		ev.AllDayEnd = g.End.Date
	}
	for _, a := range g.Attendees {
		if a.Self {
			ev.AttendeeStatus = a.ResponseStatus
			break
		}
	}
	return ev, nil
}
