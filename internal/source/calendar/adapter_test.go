package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// TestAdapterName — A1.
func TestAdapterName(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	if got := a.Name(); got != pluginName {
		t.Fatalf("Name = %q, want %q", got, pluginName)
	}
}

// TestAdapterListConvertsRegularEvent — A2 + A3.
func TestAdapterListConvertsRegularEvent(t *testing.T) {
	t.Parallel()

	jst := time.FixedZone("JST", 9*60*60)
	start := time.Date(2026, 5, 4, 10, 0, 0, 0, jst)
	end := time.Date(2026, 5, 4, 11, 0, 0, 0, jst)
	fake := &fakeClient{listEvents: []Event{{
		ID:             "evt-1",
		Summary:        "Standup",
		Description:    "daily sync",
		HTMLLink:       "https://calendar.google.com/event?eid=evt-1",
		CalendarID:     "primary",
		StartDateTime:  start,
		EndDateTime:    end,
		AttendeeStatus: "accepted",
	}}}
	a := NewAdapter(New(WithClient(fake)))
	tasks, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len = %d, want 1", len(tasks))
	}
	got := tasks[0]
	if got.Source != pluginName {
		t.Errorf("Source = %q, want %q", got.Source, pluginName)
	}
	if got.ExternalID != "evt-1" {
		t.Errorf("ExternalID = %q", got.ExternalID)
	}
	if got.Title != "Standup" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Body != "daily sync" {
		t.Errorf("Body = %q", got.Body)
	}
	if got.SourcePath != "https://calendar.google.com/event?eid=evt-1" {
		t.Errorf("SourcePath = %q", got.SourcePath)
	}
	if got.Done {
		t.Errorf("Done = true, want false (calendar is read-only)")
	}
	allDay, ok := got.RawMetadata["all_day"].(bool)
	if !ok || allDay {
		t.Errorf("RawMetadata.all_day = %v (%T), want bool false", got.RawMetadata["all_day"], got.RawMetadata["all_day"])
	}
	if status, _ := got.RawMetadata["attendee_status"].(string); status != "accepted" {
		t.Errorf("RawMetadata.attendee_status = %q, want accepted", status)
	}
	if s, _ := got.RawMetadata["start"].(string); s != start.Format(time.RFC3339) {
		t.Errorf("RawMetadata.start = %q, want %q", s, start.Format(time.RFC3339))
	}
	if e, _ := got.RawMetadata["end"].(string); e != end.Format(time.RFC3339) {
		t.Errorf("RawMetadata.end = %q, want %q", e, end.Format(time.RFC3339))
	}
	if cal, _ := got.RawMetadata["calendar_id"].(string); cal != "primary" {
		t.Errorf("RawMetadata.calendar_id = %q, want primary", cal)
	}
}

// TestAdapterListConvertsAllDayEvent — A4.
func TestAdapterListConvertsAllDayEvent(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{listEvents: []Event{{
		ID:             "evt-allday",
		Summary:        "Holiday",
		AllDay:         true,
		AllDayStart:    "2026-05-04",
		AllDayEnd:      "2026-05-05",
		AttendeeStatus: "accepted",
	}}}
	a := NewAdapter(New(WithClient(fake)))
	tasks, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len = %d, want 1", len(tasks))
	}
	got := tasks[0].RawMetadata
	if allDay, _ := got["all_day"].(bool); !allDay {
		t.Errorf("all_day = false, want true")
	}
	if d, _ := got["start_date"].(string); d != "2026-05-04" {
		t.Errorf("start_date = %q", d)
	}
	if d, _ := got["end_date"].(string); d != "2026-05-05" {
		t.Errorf("end_date = %q", d)
	}
	if _, ok := got["start"]; ok {
		t.Errorf("all-day event must not carry RawMetadata.start (use start_date)")
	}
	if _, ok := got["end"]; ok {
		t.Errorf("all-day event must not carry RawMetadata.end (use end_date)")
	}
}

// TestAdapterListSkipsZeroValueRegularEventTimes — A3b. A defensive
// guard in convertEvent: if a malformed Client somehow returns a
// non-all-day event with zero-value StartDateTime/EndDateTime, the
// adapter must NOT emit "0001-01-01T00:00:00Z" into RawMetadata. The
// real GWSClient already rejects bad dateTime via G10, so this test
// pins the second line of defence at the layer boundary.
func TestAdapterListSkipsZeroValueRegularEventTimes(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{listEvents: []Event{{
		ID:             "evt-no-time",
		Summary:        "broken",
		AllDay:         false,
		AttendeeStatus: "accepted",
	}}}
	a := NewAdapter(New(WithClient(fake)))
	tasks, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, ok := tasks[0].RawMetadata["start"]; ok {
		t.Errorf("RawMetadata.start present for zero StartDateTime; want omitted")
	}
	if _, ok := tasks[0].RawMetadata["end"]; ok {
		t.Errorf("RawMetadata.end present for zero EndDateTime; want omitted")
	}
}

// TestAdapterListLocationOnlyWhenPresent — A5.
func TestAdapterListLocationOnlyWhenPresent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		ev      Event
		wantKey bool
	}{
		{"with location", Event{ID: "1", Location: "Room A", AttendeeStatus: "accepted"}, true},
		{"without location", Event{ID: "2", AttendeeStatus: "accepted"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeClient{listEvents: []Event{tc.ev}}
			a := NewAdapter(New(WithClient(fake)))
			tasks, err := a.List(context.Background())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			_, ok := tasks[0].RawMetadata["location"]
			if ok != tc.wantKey {
				t.Errorf("RawMetadata.location present = %v, want %v", ok, tc.wantKey)
			}
		})
	}
}

// TestAdapterSetupDelegatesToInner — A6.
func TestAdapterSetupDelegatesToInner(t *testing.T) {
	t.Parallel()

	upstream := errors.New("setup boom")
	fake := &fakeClient{setupErr: upstream}
	a := NewAdapter(New(WithClient(fake)))
	err := a.Setup(context.Background(), source.SetupOptions{NonInteractive: true})
	if !errors.Is(err, upstream) {
		t.Fatalf("err = %v, want wrap of %v", err, upstream)
	}
	if !fake.setupOpts.NonInteractive {
		t.Errorf("NonInteractive opt was not forwarded")
	}
}

// TestAdapterAuthStatusDelegatesToInner — A7.
func TestAdapterAuthStatusDelegatesToInner(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{statusOut: source.AuthExpired}
	a := NewAdapter(New(WithClient(fake)))
	got, err := a.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthExpired {
		t.Errorf("status = %q, want expired", got)
	}
}

// TestAdapterDoesNotImplementOptionalCapabilities — A8. Calendar is
// strictly read-only; the Adapter must not satisfy Adder/Completer/Deleter
// /Sincer so the Discovery dispatcher's type assertions can never reach
// for a mutating method.
func TestAdapterDoesNotImplementOptionalCapabilities(t *testing.T) {
	t.Parallel()

	var p source.Plugin = NewAdapter(New())
	if _, ok := p.(source.Adder); ok {
		t.Error("Adapter satisfies source.Adder, want NOT (calendar is read-only)")
	}
	if _, ok := p.(source.Completer); ok {
		t.Error("Adapter satisfies source.Completer, want NOT (calendar is read-only)")
	}
	if _, ok := p.(source.Deleter); ok {
		t.Error("Adapter satisfies source.Deleter, want NOT (calendar is read-only)")
	}
	if _, ok := p.(source.Sincer); ok {
		t.Error("Adapter satisfies source.Sincer, want NOT (PR-81 only ships List)")
	}
}
