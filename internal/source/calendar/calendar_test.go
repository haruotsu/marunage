package calendar

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// fakeClient is the test double behind the Client interface. It records the
// last (timeMin, timeMax) handed to ListEvents so the day-boundary tests can
// assert on the window the Plugin built. Each method has an injectable
// behaviour so a single fake covers happy-path and error-path tests.
type fakeClient struct {
	lastTimeMin time.Time
	lastTimeMax time.Time
	listCalls   int

	listEvents []Event
	listErr    error

	statusOut source.AuthStatus
	statusErr error

	setupOpts source.SetupOptions
	setupErr  error
	setupCall int
}

func (f *fakeClient) ListEvents(_ context.Context, timeMin, timeMax time.Time) ([]Event, error) {
	f.listCalls++
	f.lastTimeMin = timeMin
	f.lastTimeMax = timeMax
	return f.listEvents, f.listErr
}

func (f *fakeClient) Status(context.Context) (source.AuthStatus, error) {
	return f.statusOut, f.statusErr
}

func (f *fakeClient) Setup(_ context.Context, opts source.SetupOptions) error {
	f.setupCall++
	f.setupOpts = opts
	return f.setupErr
}

// fixedClock returns a clock function pinned to t. Tests use it to make the
// day boundary deterministic without sleeping or relying on the wall clock.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// TestPluginNameIsCalendar — C1.
func TestPluginNameIsCalendar(t *testing.T) {
	t.Parallel()

	if got := New().Name(); got != "calendar" {
		t.Fatalf("Name = %q, want calendar", got)
	}
}

// TestPluginListWithoutClientReturnsClientRequired — C2.
func TestPluginListWithoutClientReturnsClientRequired(t *testing.T) {
	t.Parallel()

	_, err := New().List(context.Background())
	if !errors.Is(err, ErrClientRequired) {
		t.Fatalf("List err = %v, want ErrClientRequired", err)
	}
}

// TestPluginListPassesTodaysWindowToClient — C3 + C4.
func TestPluginListPassesTodaysWindowToClient(t *testing.T) {
	t.Parallel()

	jst := time.FixedZone("JST", 9*60*60)
	now := time.Date(2026, 5, 4, 14, 30, 0, 0, jst)
	wantMin := time.Date(2026, 5, 4, 0, 0, 0, 0, jst)
	wantMax := wantMin.Add(24 * time.Hour)

	fake := &fakeClient{}
	p := New(WithClient(fake), WithClock(fixedClock(now)))

	if _, err := p.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !fake.lastTimeMin.Equal(wantMin) {
		t.Errorf("timeMin = %s, want %s", fake.lastTimeMin, wantMin)
	}
	if !fake.lastTimeMax.Equal(wantMax) {
		t.Errorf("timeMax = %s, want %s", fake.lastTimeMax, wantMax)
	}
	if loc := fake.lastTimeMin.Location().String(); loc != "JST" {
		t.Errorf("timeMin Location = %q, want JST (boundary must be local)", loc)
	}
}

// TestPluginListRollsOverAtMidnight — C5.
func TestPluginListRollsOverAtMidnight(t *testing.T) {
	t.Parallel()

	jst := time.FixedZone("JST", 9*60*60)
	cases := []struct {
		name    string
		now     time.Time
		wantMin time.Time
	}{
		{
			name:    "just before midnight stays on today",
			now:     time.Date(2026, 5, 4, 23, 59, 59, 0, jst),
			wantMin: time.Date(2026, 5, 4, 0, 0, 0, 0, jst),
		},
		{
			name:    "midnight exactly is on next day",
			now:     time.Date(2026, 5, 5, 0, 0, 0, 0, jst),
			wantMin: time.Date(2026, 5, 5, 0, 0, 0, 0, jst),
		},
		{
			name:    "just after midnight is on next day",
			now:     time.Date(2026, 5, 5, 0, 0, 1, 0, jst),
			wantMin: time.Date(2026, 5, 5, 0, 0, 0, 0, jst),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeClient{}
			p := New(WithClient(fake), WithClock(fixedClock(tc.now)))
			if _, err := p.List(context.Background()); err != nil {
				t.Fatalf("List: %v", err)
			}
			if !fake.lastTimeMin.Equal(tc.wantMin) {
				t.Errorf("timeMin = %s, want %s", fake.lastTimeMin, tc.wantMin)
			}
			if !fake.lastTimeMax.Equal(tc.wantMin.Add(24 * time.Hour)) {
				t.Errorf("timeMax = %s, want %s", fake.lastTimeMax, tc.wantMin.Add(24*time.Hour))
			}
		})
	}
}

// TestPluginListSkipsDeclinedEvents — C6.
func TestPluginListSkipsDeclinedEvents(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{
		listEvents: []Event{
			{ID: "a", Summary: "join me", AttendeeStatus: "accepted"},
			{ID: "b", Summary: "skip me", AttendeeStatus: "declined"},
			{ID: "c", Summary: "tentative ok", AttendeeStatus: "tentative"},
			{ID: "d", Summary: "no rsvp"},
		},
	}
	p := New(WithClient(fake))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantIDs := []string{"a", "c", "d"}
	gotIDs := make([]string, len(got))
	for i, ev := range got {
		gotIDs[i] = ev.ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("ids = %v, want %v", gotIDs, wantIDs)
	}
}

// TestPluginListSkipsCancelledEvents — C14. With singleEvents=true the
// upstream API surfaces cancelled exceptions of recurring events as
// items with Status="cancelled". Materialising those into the queue
// would leave dangling rows the user already removed from the calendar,
// so the Plugin filters them out alongside declined invites.
func TestPluginListSkipsCancelledEvents(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{
		listEvents: []Event{
			{ID: "live", Summary: "happens", Status: "confirmed", AttendeeStatus: "accepted"},
			{ID: "dead", Summary: "should be skipped", Status: "cancelled", AttendeeStatus: "accepted"},
		},
	}
	p := New(WithClient(fake))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "live" {
		t.Errorf("got = %v, want only [live]", got)
	}
}

// TestPluginListBoundarySurvivesSpringForwardDST — C15. On spring-forward
// days local midnight to next local midnight is 23 wall-clock hours, not
// 24. The plugin must still hand the upstream a window that ends on the
// next local midnight, otherwise the daily agenda silently grows by an
// hour and pulls in tomorrow's first event.
func TestPluginListBoundarySurvivesSpringForwardDST(t *testing.T) {
	t.Parallel()

	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York not available: %v", err)
	}
	// 2026-03-08 is a US spring-forward day (02:00 EST -> 03:00 EDT).
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, ny)
	wantMin := time.Date(2026, 3, 8, 0, 0, 0, 0, ny)
	wantMax := time.Date(2026, 3, 9, 0, 0, 0, 0, ny)

	fake := &fakeClient{}
	p := New(WithClient(fake), WithClock(fixedClock(now)))
	if _, err := p.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !fake.lastTimeMin.Equal(wantMin) {
		t.Errorf("timeMin = %s, want %s", fake.lastTimeMin, wantMin)
	}
	if !fake.lastTimeMax.Equal(wantMax) {
		t.Errorf("timeMax = %s, want %s (DST: 23h after timeMin, NOT 24h)", fake.lastTimeMax, wantMax)
	}
	if delta := fake.lastTimeMax.Sub(fake.lastTimeMin); delta != 23*time.Hour {
		t.Errorf("timeMax - timeMin = %s, want 23h on spring-forward day", delta)
	}
}

// TestPluginListPreservesAllDayAndRegularInOrder — C7 + C8.
func TestPluginListPreservesAllDayAndRegularInOrder(t *testing.T) {
	t.Parallel()

	in := []Event{
		{ID: "1", Summary: "morning", AllDay: false, StartDateTime: time.Now(), AttendeeStatus: "accepted"},
		{ID: "2", Summary: "all day", AllDay: true, AllDayStart: "2026-05-04", AllDayEnd: "2026-05-05"},
		{ID: "3", Summary: "evening", AllDay: false, AttendeeStatus: ""},
	}
	fake := &fakeClient{listEvents: in}
	p := New(WithClient(fake))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i := range got {
		if got[i].ID != in[i].ID || got[i].AllDay != in[i].AllDay {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], in[i])
		}
	}
}

// TestPluginListEmptyClientResultIsNotAnError — C8.
func TestPluginListEmptyClientResultIsNotAnError(t *testing.T) {
	t.Parallel()

	p := New(WithClient(&fakeClient{}))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestPluginListWrapsClientError — C9.
func TestPluginListWrapsClientError(t *testing.T) {
	t.Parallel()

	upstream := errors.New("boom")
	fake := &fakeClient{listErr: upstream}
	p := New(WithClient(fake))
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatalf("List err = nil, want non-nil")
	}
	if !errors.Is(err, upstream) {
		t.Errorf("err = %v, want it to wrap %v", err, upstream)
	}
}

// TestPluginSetupWithoutClient — C10.
func TestPluginSetupWithoutClient(t *testing.T) {
	t.Parallel()

	err := New().Setup(context.Background(), source.SetupOptions{})
	if !errors.Is(err, ErrClientRequired) {
		t.Fatalf("Setup err = %v, want ErrClientRequired", err)
	}
}

// TestPluginSetupForwardsOptionsAndError — C11.
func TestPluginSetupForwardsOptionsAndError(t *testing.T) {
	t.Parallel()

	upstream := errors.New("auth failed")
	fake := &fakeClient{setupErr: upstream}
	p := New(WithClient(fake))
	err := p.Setup(context.Background(), source.SetupOptions{NonInteractive: true})
	if !errors.Is(err, upstream) {
		t.Fatalf("err = %v, want %v", err, upstream)
	}
	if !fake.setupOpts.NonInteractive {
		t.Errorf("setupOpts.NonInteractive = false, want true")
	}
	if fake.setupCall != 1 {
		t.Errorf("setup called %d times, want 1", fake.setupCall)
	}
}

// TestPluginAuthStatusWithoutClientIsNotConfigured — C12.
func TestPluginAuthStatusWithoutClientIsNotConfigured(t *testing.T) {
	t.Parallel()

	got, err := New().AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Errorf("status = %q, want %q", got, source.AuthNotConfigured)
	}
}

// TestPluginAuthStatusPropagatesClientError — C16. AuthStatus must
// surface the underlying client.Status error verbatim so a daemon
// health check can distinguish "we asked and got an answer" from
// "we asked and the I/O failed". The previous test only exercised the
// (status, nil) shape; this one fixes the missing branch.
func TestPluginAuthStatusPropagatesClientError(t *testing.T) {
	t.Parallel()

	upstream := errors.New("status probe boom")
	fake := &fakeClient{statusErr: upstream}
	p := New(WithClient(fake))
	_, err := p.AuthStatus(context.Background())
	if !errors.Is(err, upstream) {
		t.Fatalf("err = %v, want wrap of %v", err, upstream)
	}
}

// TestPluginAuthStatusDelegatesToClient — C13.
func TestPluginAuthStatusDelegatesToClient(t *testing.T) {
	t.Parallel()

	cases := []source.AuthStatus{
		source.AuthAuthenticated,
		source.AuthExpired,
		source.AuthRevoked,
		source.AuthNotConfigured,
	}
	for _, want := range cases {
		want := want
		t.Run(string(want), func(t *testing.T) {
			fake := &fakeClient{statusOut: want}
			p := New(WithClient(fake))
			got, err := p.AuthStatus(context.Background())
			if err != nil {
				t.Fatalf("AuthStatus: %v", err)
			}
			if got != want {
				t.Errorf("status = %q, want %q", got, want)
			}
		})
	}
}
