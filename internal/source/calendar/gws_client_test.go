package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// recordedCall captures one runner invocation so the GWS-shape tests can
// assert on the exact (name, args) tuple the client built. Storing both
// stdout and an error lets the same recorder drive happy-path and
// failure-path tests.
type recordedCall struct {
	name string
	args []string
}

// scriptedRunner is a fakeRunner that returns a queue of pre-baked outputs
// so a single GWSClient method can be exercised across multiple scenarios
// without the test having to know which call matches which response.
type scriptedRunner struct {
	calls   []recordedCall
	outputs [][]byte
	outErrs []error
	callIdx int
}

func (r *scriptedRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedCall{name: name, args: append([]string(nil), args...)})
	if r.callIdx >= len(r.outputs) {
		return nil, errors.New("scripted runner: ran out of canned responses")
	}
	out, err := r.outputs[r.callIdx], r.outErrs[r.callIdx]
	r.callIdx++
	return out, err
}

// helper: encode a Google Calendar v3 events.list response with the given
// items so each test reads top-to-bottom without indirection.
func eventsListJSON(t *testing.T, items []map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"kind":  "calendar#events",
		"items": items,
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

// findArg returns the argument that follows flag, or "" if flag is absent.
// `gws` accepts `--params <JSON>` so the test reads the JSON payload that
// way to assert on the request shape without parsing the entire arg list.
func findArg(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestGWSListEventsBuildsCommandShape — G1.
func TestGWSListEventsBuildsCommandShape(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{eventsListJSON(t, nil)},
		outErrs: []error{nil},
	}
	client := NewGWSClient(WithRunner(runner.run))
	timeMin := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	timeMax := timeMin.Add(24 * time.Hour)

	if _, err := client.ListEvents(context.Background(), timeMin, timeMax); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "gws" {
		t.Errorf("name = %q, want gws", call.name)
	}
	wantPrefix := []string{"calendar", "events", "list"}
	if len(call.args) < len(wantPrefix) {
		t.Fatalf("args too short: %v", call.args)
	}
	for i, w := range wantPrefix {
		if call.args[i] != w {
			t.Errorf("args[%d] = %q, want %q", i, call.args[i], w)
		}
	}
	params := findArg(call.args, "--params")
	if params == "" {
		t.Fatalf("--params not present in args: %v", call.args)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(params), &got); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if got["calendarId"] != "primary" {
		t.Errorf("calendarId = %v, want primary", got["calendarId"])
	}
	if got["singleEvents"] != true {
		t.Errorf("singleEvents = %v, want true", got["singleEvents"])
	}
	if got["orderBy"] != "startTime" {
		t.Errorf("orderBy = %v, want startTime", got["orderBy"])
	}
	if got["timeMin"] != timeMin.Format(time.RFC3339) {
		t.Errorf("timeMin = %v, want %s", got["timeMin"], timeMin.Format(time.RFC3339))
	}
	if got["timeMax"] != timeMax.Format(time.RFC3339) {
		t.Errorf("timeMax = %v, want %s", got["timeMax"], timeMax.Format(time.RFC3339))
	}
}

// TestGWSListEventsParsesRegularAndAllDay — G2.
func TestGWSListEventsParsesRegularAndAllDay(t *testing.T) {
	t.Parallel()

	jst := time.FixedZone("JST", 9*60*60)
	regularStart := time.Date(2026, 5, 4, 10, 0, 0, 0, jst)
	regularEnd := regularStart.Add(time.Hour)
	items := []map[string]any{
		{
			"id":          "evt-regular",
			"summary":     "Standup",
			"description": "daily sync",
			"location":    "Room A",
			"htmlLink":    "https://cal.google.com/x",
			"start":       map[string]any{"dateTime": regularStart.Format(time.RFC3339)},
			"end":         map[string]any{"dateTime": regularEnd.Format(time.RFC3339)},
		},
		{
			"id":      "evt-allday",
			"summary": "Holiday",
			"start":   map[string]any{"date": "2026-05-04"},
			"end":     map[string]any{"date": "2026-05-05"},
		},
	}
	runner := &scriptedRunner{
		outputs: [][]byte{eventsListJSON(t, items)},
		outErrs: []error{nil},
	}
	client := NewGWSClient(WithRunner(runner.run))
	got, err := client.ListEvents(context.Background(), time.Now(), time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "evt-regular" || got[0].AllDay {
		t.Errorf("regular event mis-parsed: %+v", got[0])
	}
	if !got[0].StartDateTime.Equal(regularStart) {
		t.Errorf("StartDateTime = %s, want %s", got[0].StartDateTime, regularStart)
	}
	if !got[0].EndDateTime.Equal(regularEnd) {
		t.Errorf("EndDateTime = %s, want %s", got[0].EndDateTime, regularEnd)
	}
	if got[0].Summary != "Standup" || got[0].Description != "daily sync" || got[0].Location != "Room A" || got[0].HTMLLink != "https://cal.google.com/x" {
		t.Errorf("regular fields mis-parsed: %+v", got[0])
	}
	if got[1].ID != "evt-allday" || !got[1].AllDay {
		t.Errorf("all-day mis-parsed: %+v", got[1])
	}
	if got[1].AllDayStart != "2026-05-04" || got[1].AllDayEnd != "2026-05-05" {
		t.Errorf("all-day dates = (%q, %q)", got[1].AllDayStart, got[1].AllDayEnd)
	}
}

// TestGWSListEventsExtractsSelfAttendeeStatus — G3.
func TestGWSListEventsExtractsSelfAttendeeStatus(t *testing.T) {
	t.Parallel()

	items := []map[string]any{
		{
			"id":      "evt-self-declined",
			"summary": "I said no",
			"start":   map[string]any{"dateTime": "2026-05-04T10:00:00+09:00"},
			"end":     map[string]any{"dateTime": "2026-05-04T11:00:00+09:00"},
			"attendees": []map[string]any{
				{"email": "other@example.com", "responseStatus": "accepted"},
				{"email": "me@example.com", "self": true, "responseStatus": "declined"},
			},
		},
		{
			"id":      "evt-no-self",
			"summary": "I own this",
			"start":   map[string]any{"dateTime": "2026-05-04T12:00:00+09:00"},
			"end":     map[string]any{"dateTime": "2026-05-04T13:00:00+09:00"},
			"attendees": []map[string]any{
				{"email": "other@example.com", "responseStatus": "accepted"},
			},
		},
	}
	runner := &scriptedRunner{
		outputs: [][]byte{eventsListJSON(t, items)},
		outErrs: []error{nil},
	}
	client := NewGWSClient(WithRunner(runner.run))
	got, err := client.ListEvents(context.Background(), time.Now(), time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if got[0].AttendeeStatus != "declined" {
		t.Errorf("self-declined event AttendeeStatus = %q, want declined", got[0].AttendeeStatus)
	}
	if got[1].AttendeeStatus != "" {
		t.Errorf("no-self event AttendeeStatus = %q, want empty", got[1].AttendeeStatus)
	}
}

// TestGWSListEventsWrapsRunnerError — G4.
func TestGWSListEventsWrapsRunnerError(t *testing.T) {
	t.Parallel()

	upstream := errors.New("gws: command failed: exit status 1")
	runner := &scriptedRunner{
		outputs: [][]byte{nil},
		outErrs: []error{upstream},
	}
	client := NewGWSClient(WithRunner(runner.run))
	_, err := client.ListEvents(context.Background(), time.Now(), time.Now().Add(24*time.Hour))
	if !errors.Is(err, upstream) {
		t.Fatalf("err = %v, want wrap of %v", err, upstream)
	}
}

// TestGWSStatusReportsAuth — G5.
func TestGWSStatusReportsAuth(t *testing.T) {
	t.Parallel()

	t.Run("authenticated", func(t *testing.T) {
		runner := &scriptedRunner{
			outputs: [][]byte{[]byte(`{"items":[{"id":"primary"}]}`)},
			outErrs: []error{nil},
		}
		client := NewGWSClient(WithRunner(runner.run))
		got, err := client.Status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got != source.AuthAuthenticated {
			t.Errorf("status = %q, want authenticated", got)
		}
		// We deliberately do not pin the exact subcommand here; the
		// G5 contract is "calls something cheap to verify creds and
		// returns the typed status."
		if len(runner.calls) != 1 {
			t.Errorf("expected one runner call, got %d", len(runner.calls))
		}
		if runner.calls[0].name != "gws" {
			t.Errorf("runner called %q, want gws", runner.calls[0].name)
		}
	})

	t.Run("not configured on runner failure", func(t *testing.T) {
		runner := &scriptedRunner{
			outputs: [][]byte{nil},
			outErrs: []error{errors.New("gws auth: token not present")},
		}
		client := NewGWSClient(WithRunner(runner.run))
		got, err := client.Status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got != source.AuthNotConfigured {
			t.Errorf("status = %q, want not_configured", got)
		}
	})
}

// TestGWSListEventsParsesEventStatus — G7. status=cancelled items must
// reach the Plugin layer so Plugin.List can drop them; the parser must
// not pre-filter (test_list C14 owns the cancelled-event filtering at
// the Plugin layer).
func TestGWSListEventsParsesEventStatus(t *testing.T) {
	t.Parallel()

	items := []map[string]any{
		{
			"id":     "evt-cancelled",
			"status": "cancelled",
			"start":  map[string]any{"dateTime": "2026-05-04T10:00:00+09:00"},
			"end":    map[string]any{"dateTime": "2026-05-04T11:00:00+09:00"},
		},
	}
	runner := &scriptedRunner{
		outputs: [][]byte{eventsListJSON(t, items)},
		outErrs: []error{nil},
	}
	client := NewGWSClient(WithRunner(runner.run))
	got, err := client.ListEvents(context.Background(), time.Now(), time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (parser does not filter; that is the Plugin's job)", len(got))
	}
	if got[0].Status != "cancelled" {
		t.Errorf("Status = %q, want cancelled", got[0].Status)
	}
}

// TestGWSSetupInteractiveSurfacesProbeError — G8. Interactive Setup
// runs the calendarList smoke test directly and surfaces the runner
// error (e.g. binary missing, network failure) verbatim — going through
// Status would collapse those into AuthNotConfigured and hide the
// underlying problem behind the "please run gws auth login" hint.
func TestGWSSetupInteractiveSurfacesProbeError(t *testing.T) {
	t.Parallel()

	upstream := errors.New("exec: \"gws\": executable file not found in $PATH")
	runner := &scriptedRunner{
		outputs: [][]byte{nil},
		outErrs: []error{upstream},
	}
	client := NewGWSClient(WithRunner(runner.run))
	err := client.Setup(context.Background(), source.SetupOptions{NonInteractive: false})
	if !errors.Is(err, upstream) {
		t.Fatalf("err = %v, want it to wrap %v", err, upstream)
	}
}

// TestGWSSetupNonInteractiveExplains — G6.
func TestGWSSetupNonInteractiveExplains(t *testing.T) {
	t.Parallel()

	client := NewGWSClient(WithRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatalf("Setup must not invoke the runner in non-interactive mode")
		return nil, nil
	}))
	err := client.Setup(context.Background(), source.SetupOptions{NonInteractive: true})
	if err == nil {
		t.Fatalf("Setup err = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "gws auth") {
		t.Errorf("err = %v, want it to mention gws auth", err)
	}
}
