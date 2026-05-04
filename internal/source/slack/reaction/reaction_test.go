package reaction

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// jsonUnmarshal is a thin alias for json.Unmarshal used in Notes-format tests
// so the assertion reads naturally.
func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// fakeClient is a programmable ReactionClient for tests.
type fakeClient struct {
	events       []ReactionEvent
	eventsErr    error
	postErr      error
	openDMErr    error
	auth         source.AuthStatus
	authErr      error
	setupErr     error
	gotFetchArgs []fetchArgs
	gotPostCh    []string
	gotPostText  []string
	gotOpenDM    []string
	dmChannel    string // returned by OpenDM
}

type fetchArgs struct {
	reactions []string
	sinceTS   string
}

func (f *fakeClient) FetchReactionEvents(_ context.Context, reactions []string, sinceTS string) ([]ReactionEvent, error) {
	f.gotFetchArgs = append(f.gotFetchArgs, fetchArgs{reactions: reactions, sinceTS: sinceTS})
	return f.events, f.eventsErr
}

func (f *fakeClient) PostDM(_ context.Context, channelID, text string) error {
	f.gotPostCh = append(f.gotPostCh, channelID)
	f.gotPostText = append(f.gotPostText, text)
	return f.postErr
}

func (f *fakeClient) OpenDM(_ context.Context, userID string) (string, error) {
	f.gotOpenDM = append(f.gotOpenDM, userID)
	if f.openDMErr != nil {
		return "", f.openDMErr
	}
	if f.dmChannel != "" {
		return f.dmChannel, nil
	}
	return "D-" + userID, nil
}

func (f *fakeClient) AuthStatus(_ context.Context) (source.AuthStatus, error) {
	return f.auth, f.authErr
}

func (f *fakeClient) Setup(_ context.Context, _ bool) error {
	return f.setupErr
}

// memCheckpointer is the in-memory Checkpointer fake.
type memCheckpointer struct {
	store map[string]string
	sets  map[string]string
}

func newMemCheckpointer() *memCheckpointer {
	return &memCheckpointer{store: map[string]string{}, sets: map[string]string{}}
}

func (m *memCheckpointer) Get(_ context.Context, key string) (string, error) {
	return m.store[key], nil
}

func (m *memCheckpointer) Set(_ context.Context, key, value string) error {
	m.store[key] = value
	m.sets[key] = value
	return nil
}

// ── Test list 1: reaction message becomes a task ──────────────────────────────

func TestListReturnsTaskForMatchingReaction(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		events: []ReactionEvent{
			{
				Reaction:  "todo",
				UserID:    "U1",
				ChannelID: "C1",
				TS:        "1700000000.000100",
				Text:      "fix the pipeline",
				Permalink: "https://slack.example/archives/C1/p1700000000000100",
			},
		},
	}
	p := New(WithClient(c), WithReactions([]string{"todo", "inbox_tray"}))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1; %+v", len(got), got)
	}
	tk := got[0]
	if tk.Source != "slack:reaction" {
		t.Errorf("Source = %q, want slack:reaction", tk.Source)
	}
	if tk.Title != "fix the pipeline" {
		t.Errorf("Title = %q, want 'fix the pipeline'", tk.Title)
	}
}

// ── Test list 2: non-configured reactions are ignored ─────────────────────────

func TestClientFiltersEventsByConfiguredReactions(t *testing.T) {
	t.Parallel()
	// The plugin passes its reactions list to the client; the client is
	// responsible for filtering. We verify the plugin forwards the correct
	// reactions list to FetchReactionEvents.
	c := &fakeClient{events: nil}
	p := New(WithClient(c), WithReactions([]string{"todo"}))
	if _, err := p.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(c.gotFetchArgs) != 1 {
		t.Fatalf("FetchReactionEvents calls = %d, want 1", len(c.gotFetchArgs))
	}
	got := c.gotFetchArgs[0].reactions
	if len(got) != 1 || got[0] != "todo" {
		t.Errorf("reactions forwarded = %v, want [todo]", got)
	}
}

func TestListWithNoReactionsConfiguredReturnsEmpty(t *testing.T) {
	t.Parallel()
	c := &fakeClient{events: []ReactionEvent{{Reaction: "todo", UserID: "U1", TS: "1.0"}}}
	p := New(WithClient(c)) // no reactions configured
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
	if len(c.gotFetchArgs) != 0 {
		t.Errorf("FetchReactionEvents called when no reactions configured")
	}
}

// ── Test list 3: permalink and message body are recorded in notes ─────────────

func TestTaskNotesContainPermalinkAndMessageBody(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		events: []ReactionEvent{
			{
				Reaction:  "todo",
				UserID:    "U1",
				ChannelID: "C1",
				TS:        "1700000000.000100",
				Text:      "deploy the new service",
				Permalink: "https://slack.example/archives/C1/p1700000000000100",
			},
		},
	}
	p := New(WithClient(c), WithReactions([]string{"todo"}))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	tk := got[0]
	// Notes must be a JSON object (satisfying the tasks.notes CHECK constraint)
	// containing both permalink and message body.
	if !strings.Contains(tk.Notes, `"permalink"`) || !strings.Contains(tk.Notes, "https://slack.example/archives/C1/p1700000000000100") {
		t.Errorf("Notes does not contain permalink JSON: %q", tk.Notes)
	}
	if !strings.Contains(tk.Notes, `"message"`) || !strings.Contains(tk.Notes, "deploy the new service") {
		t.Errorf("Notes does not contain message JSON: %q", tk.Notes)
	}
	if tk.SourcePath != "https://slack.example/archives/C1/p1700000000000100" {
		t.Errorf("SourcePath = %q", tk.SourcePath)
	}
}

func TestTaskRawMetadataContainsReactionAndUserID(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		events: []ReactionEvent{
			{Reaction: "inbox_tray", UserID: "U99", ChannelID: "C2", TS: "1.0", Text: "x", Permalink: "https://p"},
		},
	}
	p := New(WithClient(c), WithReactions([]string{"inbox_tray"}))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	tk := got[0]
	checks := map[string]string{
		"reaction":   "inbox_tray",
		"user_id":    "U99",
		"channel_id": "C2",
		"ts":         "1.0",
		"permalink":  "https://p",
	}
	for k, want := range checks {
		v, ok := tk.RawMetadata[k]
		if !ok {
			t.Errorf("RawMetadata[%q] missing", k)
			continue
		}
		if v != want {
			t.Errorf("RawMetadata[%q] = %v, want %q", k, v, want)
		}
	}
}

// ── Test list 4: idempotency via ExternalID ───────────────────────────────────

func TestExternalIDIsUniquePerReactionEvent(t *testing.T) {
	t.Parallel()
	// Two events: same channel+ts+reaction but different users → different ExternalIDs
	// because each user's reaction is independent.
	c := &fakeClient{
		events: []ReactionEvent{
			{Reaction: "todo", UserID: "U1", ChannelID: "C1", TS: "1700000000.000100"},
			{Reaction: "todo", UserID: "U2", ChannelID: "C1", TS: "1700000000.000100"},
		},
	}
	p := New(WithClient(c), WithReactions([]string{"todo"}))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ExternalID == got[1].ExternalID {
		t.Errorf("ExternalIDs must be unique: both are %q", got[0].ExternalID)
	}
}

func TestExternalIDContainsChannelTSReactionUser(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		events: []ReactionEvent{
			{Reaction: "todo", UserID: "U1", ChannelID: "C1", TS: "1700000000.000100"},
		},
	}
	p := New(WithClient(c), WithReactions([]string{"todo"}))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	id := got[0].ExternalID
	for _, part := range []string{"C1", "1700000000.000100", "todo", "U1"} {
		if !strings.Contains(id, part) {
			t.Errorf("ExternalID %q missing part %q", id, part)
		}
	}
}

func TestSameSameReactionEventProducesSameExternalID(t *testing.T) {
	t.Parallel()
	ev := ReactionEvent{Reaction: "todo", UserID: "U1", ChannelID: "C1", TS: "1.0"}
	id1 := eventToTask(ev).ExternalID
	id2 := eventToTask(ev).ExternalID
	if id1 != id2 {
		t.Errorf("ExternalID not deterministic: %q vs %q", id1, id2)
	}
}

// ── Test list 5: DM notification on task completion ──────────────────────────

func TestCompleteSendsDMToReactor(t *testing.T) {
	t.Parallel()
	c := &fakeClient{dmChannel: "D-NOTIFY"}
	// ExternalID encodes user_id so Complete can route the DM.
	p := New(WithClient(c), WithReactions([]string{"todo"}), WithDMOnComplete(true))
	externalID := "C1:1700000000.000100:todo:U1"
	if err := p.Complete(context.Background(), externalID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(c.gotOpenDM) != 1 || c.gotOpenDM[0] != "U1" {
		t.Errorf("OpenDM called with %v, want [U1]", c.gotOpenDM)
	}
	if len(c.gotPostCh) != 1 || c.gotPostCh[0] != "D-NOTIFY" {
		t.Errorf("PostDM channel = %v, want [D-NOTIFY]", c.gotPostCh)
	}
	if len(c.gotPostText) != 1 ||
		!strings.Contains(c.gotPostText[0], "done") ||
		!strings.Contains(c.gotPostText[0], "todo") {
		t.Errorf("PostDM text = %v, want message containing 'done' and reaction name 'todo'", c.gotPostText)
	}
}

func TestCompleteNoopWhenDMOnCompleteDisabled(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c), WithReactions([]string{"todo"}), WithDMOnComplete(false))
	if err := p.Complete(context.Background(), "C1:1.0:todo:U1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(c.gotPostCh) != 0 {
		t.Errorf("PostDM called when dm_on_complete=false: %v", c.gotPostCh)
	}
}

func TestCompleteRejectsEmptyExternalID(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c), WithDMOnComplete(true))
	err := p.Complete(context.Background(), "")
	if !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("err = %v, want ErrInvalidTaskID", err)
	}
}

func TestCompleteRejectsMalformedExternalID(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c), WithDMOnComplete(true))
	// Missing user_id segment
	err := p.Complete(context.Background(), "C1:1.0:todo")
	if !errors.Is(err, ErrInvalidExternalID) {
		t.Fatalf("err = %v, want ErrInvalidExternalID", err)
	}
}

func TestCompleteOpenDMError(t *testing.T) {
	t.Parallel()
	boom := errors.New("dm-open-failed")
	c := &fakeClient{openDMErr: boom}
	p := New(WithClient(c), WithDMOnComplete(true))
	err := p.Complete(context.Background(), "C1:1.0:todo:U1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

// ── Checkpoint (Since) tests ──────────────────────────────────────────────────

func TestSinceForwardsStoredCheckpointToClient(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	cp := newMemCheckpointer()
	cp.store[CheckpointKey] = "1699999999.000050"
	p := New(WithClient(c), WithCheckpointer(cp), WithReactions([]string{"todo"}))
	if _, err := p.Since(context.Background(), ""); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(c.gotFetchArgs) != 1 || c.gotFetchArgs[0].sinceTS != "1699999999.000050" {
		t.Errorf("sinceTS = %v", c.gotFetchArgs)
	}
}

func TestSinceAdvancesCheckpointToMaxTS(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		events: []ReactionEvent{
			{Reaction: "todo", UserID: "U1", ChannelID: "C", TS: "1700000000.000100"},
			{Reaction: "todo", UserID: "U2", ChannelID: "C", TS: "1700000000.000300"},
		},
	}
	cp := newMemCheckpointer()
	p := New(WithClient(c), WithCheckpointer(cp), WithReactions([]string{"todo"}))
	if _, err := p.Since(context.Background(), ""); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if cp.sets[CheckpointKey] != "1700000000.000300" {
		t.Errorf("checkpoint = %q, want 1700000000.000300", cp.sets[CheckpointKey])
	}
}

func TestSinceWithNoResultsLeavesCheckpointUnchanged(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	cp := newMemCheckpointer()
	cp.store[CheckpointKey] = "1699999999.000000"
	p := New(WithClient(c), WithCheckpointer(cp), WithReactions([]string{"todo"}))
	if _, err := p.Since(context.Background(), ""); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if _, wrote := cp.sets[CheckpointKey]; wrote {
		t.Errorf("checkpoint written when no results")
	}
}

// ── PostDM error propagation ──────────────────────────────────────────────────

func TestCompletePostDMErrorPropagates(t *testing.T) {
	t.Parallel()
	boom := errors.New("post-failed")
	c := &fakeClient{postErr: boom, dmChannel: "D-X"}
	p := New(WithClient(c), WithDMOnComplete(true))
	err := p.Complete(context.Background(), "C1:1.0:todo:U1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestCompleteEmptyUserIDInExternalIDReturnsError(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c), WithDMOnComplete(true))
	// trailing colon → user_id segment is empty
	err := p.Complete(context.Background(), "C1:1.0:todo:")
	if !errors.Is(err, ErrInvalidExternalID) {
		t.Fatalf("err = %v, want ErrInvalidExternalID", err)
	}
}

// ── Since explicit checkpoint ─────────────────────────────────────────────────

func TestSinceExplicitArgWinsOverStoredCheckpoint(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	cp := newMemCheckpointer()
	cp.store[CheckpointKey] = "1699999999.000050"
	p := New(WithClient(c), WithCheckpointer(cp), WithReactions([]string{"todo"}))
	// Explicit non-empty arg must be forwarded directly, not replaced by stored value.
	if _, err := p.Since(context.Background(), "1700000000.000000"); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(c.gotFetchArgs) != 1 || c.gotFetchArgs[0].sinceTS != "1700000000.000000" {
		t.Errorf("sinceTS = %v, want 1700000000.000000", c.gotFetchArgs)
	}
}

// ── splitTitleBody ────────────────────────────────────────────────────────────

func TestSplitTitleBodyMultiLine(t *testing.T) {
	t.Parallel()
	title, body := splitTitleBody("first line\nsecond line")
	if title != "first line" {
		t.Errorf("title = %q, want 'first line'", title)
	}
	if body != "first line\nsecond line" {
		t.Errorf("body = %q, want full text", body)
	}
}

func TestSplitTitleBodySingleLine(t *testing.T) {
	t.Parallel()
	title, body := splitTitleBody("only one line")
	if title != "only one line" {
		t.Errorf("title = %q", title)
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestSplitTitleBodyEmpty(t *testing.T) {
	t.Parallel()
	title, body := splitTitleBody("")
	if title != "" || body != "" {
		t.Errorf("splitTitleBody('') = %q, %q; want empty, empty", title, body)
	}
}

// ── truncate ──────────────────────────────────────────────────────────────────

func TestTruncateShortStringUnchanged(t *testing.T) {
	t.Parallel()
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate = %q, want hello", got)
	}
}

func TestTruncateAtExactLimit(t *testing.T) {
	t.Parallel()
	if got := truncate("abcde", 5); got != "abcde" {
		t.Errorf("truncate = %q, want abcde", got)
	}
}

func TestTruncateBeyondLimit(t *testing.T) {
	t.Parallel()
	if got := truncate("abcdef", 3); got != "abc" {
		t.Errorf("truncate = %q, want abc", got)
	}
}

func TestTruncateMultibyteRune(t *testing.T) {
	t.Parallel()
	// "日本語" = 3 runes, 9 bytes; truncate at 2 must not split a rune
	got := truncate("日本語テスト", 2)
	if got != "日本" {
		t.Errorf("truncate = %q, want 日本", got)
	}
}

func TestTruncateZeroLimit(t *testing.T) {
	t.Parallel()
	if got := truncate("hello", 0); got != "" {
		t.Errorf("truncate(0) = %q, want empty", got)
	}
}

// ── compareTS ─────────────────────────────────────────────────────────────────

func TestCompareTSTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "1.0", -1},
		{"1.0", "", 1},
		{"1.0", "1.0", 0},
		{"1700000000.000100", "1700000000.000200", -1},
		{"1700000000.000200", "1700000000.000100", 1},
		{"1700000001.000000", "1700000000.999999", 1},
		// fractional right-pad: "1" == "100" as fractions (0.1 == 0.100)
		{"1700000000.1", "1700000000.100", 0},
		// fractional ordering: 0.0001 > 0.00009
		{"1700000000.0001", "1700000000.00009", 1},
	}
	for _, tc := range cases {
		got := compareTS(tc.a, tc.b)
		if (got < 0 && tc.want >= 0) || (got == 0 && tc.want != 0) || (got > 0 && tc.want <= 0) {
			t.Errorf("compareTS(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// ── notes JSON format ─────────────────────────────────────────────────────────

func TestTaskNotesIsValidJSON(t *testing.T) {
	t.Parallel()
	ev := ReactionEvent{
		Reaction:  "todo",
		UserID:    "U1",
		ChannelID: "C1",
		TS:        "1.0",
		Text:      "fix this",
		Permalink: "https://example.com",
	}
	tk := eventToTask(ev)
	if len(tk.Notes) == 0 {
		t.Fatal("Notes is empty")
	}
	// Must be valid JSON (tasks.notes CHECK constraint)
	var m map[string]string
	if err := jsonUnmarshal(tk.Notes, &m); err != nil {
		t.Errorf("Notes is not valid JSON: %v — %q", err, tk.Notes)
	}
	if m["permalink"] != "https://example.com" {
		t.Errorf("Notes.permalink = %q", m["permalink"])
	}
	if m["message"] != "fix this" {
		t.Errorf("Notes.message = %q", m["message"])
	}
}

// ── Error propagation ─────────────────────────────────────────────────────────

func TestListPropagatesClientError(t *testing.T) {
	t.Parallel()
	boom := errors.New("network")
	c := &fakeClient{eventsErr: boom}
	p := New(WithClient(c), WithReactions([]string{"todo"}))
	_, err := p.List(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestListWithoutClientReturnsErrClientNotConfigured(t *testing.T) {
	t.Parallel()
	p := New(WithReactions([]string{"todo"}))
	_, err := p.List(context.Background())
	if !errors.Is(err, ErrClientNotConfigured) {
		t.Fatalf("err = %v, want ErrClientNotConfigured", err)
	}
}
