package slack

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// fakeClient is a programmable Client used by every test in this file. The
// recorded fields let assertions confirm the plugin forwarded the right
// arguments without having to capture them through closures.
type fakeClient struct {
	mentions      []Message
	dms           []Message
	mentionsErr   error
	dmsErr        error
	postErr       error
	auth          source.AuthStatus
	authErr       error
	setupErr      error
	setupOpts     []bool
	gotMentionsTS []string
	gotDMsTS      []string
	gotPostCh     []string
	gotPostText   []string
	authCalls     int
}

func (f *fakeClient) FetchMentions(_ context.Context, sinceTS string) ([]Message, error) {
	f.gotMentionsTS = append(f.gotMentionsTS, sinceTS)
	return f.mentions, f.mentionsErr
}

func (f *fakeClient) FetchDMs(_ context.Context, sinceTS string) ([]Message, error) {
	f.gotDMsTS = append(f.gotDMsTS, sinceTS)
	return f.dms, f.dmsErr
}

func (f *fakeClient) PostDM(_ context.Context, channelID, text string) error {
	f.gotPostCh = append(f.gotPostCh, channelID)
	f.gotPostText = append(f.gotPostText, text)
	return f.postErr
}

func (f *fakeClient) AuthStatus(_ context.Context) (source.AuthStatus, error) {
	f.authCalls++
	return f.auth, f.authErr
}

func (f *fakeClient) Setup(_ context.Context, nonInteractive bool) error {
	f.setupOpts = append(f.setupOpts, nonInteractive)
	return f.setupErr
}

// memoryCheckpointer is the in-memory Checkpointer fake used by Since tests.
// Mirrors the markdown package's pattern so tests in both packages read alike.
type memoryCheckpointer struct {
	store map[string]string
	gets  []string
	sets  map[string]string
}

func newMemoryCheckpointer() *memoryCheckpointer {
	return &memoryCheckpointer{store: map[string]string{}, sets: map[string]string{}}
}

func (m *memoryCheckpointer) Get(_ context.Context, key string) (string, error) {
	m.gets = append(m.gets, key)
	return m.store[key], nil
}

func (m *memoryCheckpointer) Set(_ context.Context, key, value string) error {
	m.store[key] = value
	m.sets[key] = value
	return nil
}

// B1.
func TestPluginNameIsSlack(t *testing.T) {
	t.Parallel()
	p := New()
	if p.Name() != "slack" {
		t.Fatalf("Name() = %q, want slack", p.Name())
	}
}

// B2.
func TestNewWithOptionsRoundTrip(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	cp := newMemoryCheckpointer()
	p := New(
		WithClient(c),
		WithCheckpointer(cp),
		WithIncludeMentions(true),
		WithIncludeDM(true),
		WithNotifyChannelID("D123"),
	)
	if p.client != c {
		t.Errorf("client not stored")
	}
	if p.checkpointer != cp {
		t.Errorf("checkpointer not stored")
	}
	if !p.includeMentions || !p.includeDM {
		t.Errorf("flags not stored: mentions=%v dm=%v", p.includeMentions, p.includeDM)
	}
	if p.notifyChannelID != "D123" {
		t.Errorf("notifyChannelID = %q", p.notifyChannelID)
	}
}

// B3.
func TestNewDefaultClientErrorsWithoutConfig(t *testing.T) {
	t.Parallel()
	p := New()
	_, err := p.client.FetchMentions(context.Background(), "")
	if !errors.Is(err, ErrClientNotConfigured) {
		t.Fatalf("default FetchMentions error = %v, want ErrClientNotConfigured", err)
	}
	_, err = p.client.FetchDMs(context.Background(), "")
	if !errors.Is(err, ErrClientNotConfigured) {
		t.Fatalf("default FetchDMs error = %v, want ErrClientNotConfigured", err)
	}
	if err := p.client.PostDM(context.Background(), "C", "x"); !errors.Is(err, ErrClientNotConfigured) {
		t.Fatalf("default PostDM error = %v, want ErrClientNotConfigured", err)
	}
}

// C1.
func TestListWithBothFlagsOffReturnsEmptyAndDoesNotCallClient(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		mentions: []Message{{ChannelID: "C1", TS: "1.0", Text: "x"}},
		dms:      []Message{{ChannelID: "D1", ChannelType: "im", TS: "2.0", Text: "y"}},
	}
	p := New(WithClient(c))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List = %+v, want empty", got)
	}
	if len(c.gotMentionsTS) != 0 || len(c.gotDMsTS) != 0 {
		t.Fatalf("client called: mentions=%v dms=%v", c.gotMentionsTS, c.gotDMsTS)
	}
}

// C2 / C3.
func TestListMentionsOnlyAndDMsOnly(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		mentions: []Message{{ChannelID: "C1", TS: "1.0", Text: "hi"}},
		dms:      []Message{{ChannelID: "D1", ChannelType: "im", TS: "2.0", Text: "ho"}},
	}
	pMen := New(WithClient(c), WithIncludeMentions(true))
	if _, err := pMen.List(context.Background()); err != nil {
		t.Fatalf("mentions list: %v", err)
	}
	if len(c.gotMentionsTS) != 1 || len(c.gotDMsTS) != 0 {
		t.Fatalf("mentions-only call shape: mentions=%v dms=%v", c.gotMentionsTS, c.gotDMsTS)
	}

	c2 := &fakeClient{
		mentions: []Message{{ChannelID: "C1", TS: "1.0", Text: "hi"}},
		dms:      []Message{{ChannelID: "D1", ChannelType: "im", TS: "2.0", Text: "ho"}},
	}
	pDM := New(WithClient(c2), WithIncludeDM(true))
	if _, err := pDM.List(context.Background()); err != nil {
		t.Fatalf("dm list: %v", err)
	}
	if len(c2.gotMentionsTS) != 0 || len(c2.gotDMsTS) != 1 {
		t.Fatalf("dm-only call shape: mentions=%v dms=%v", c2.gotMentionsTS, c2.gotDMsTS)
	}
}

// C4.
func TestListBothFlagsMergesResults(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		mentions: []Message{{ChannelID: "C1", TS: "1.0", Text: "alpha"}},
		dms:      []Message{{ChannelID: "D1", ChannelType: "im", TS: "2.0", Text: "beta"}},
	}
	p := New(WithClient(c), WithIncludeMentions(true), WithIncludeDM(true))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2; %+v", len(got), got)
	}
	titles := []string{got[0].Title, got[1].Title}
	want := map[string]bool{"alpha": false, "beta": false}
	for _, ti := range titles {
		if _, ok := want[ti]; !ok {
			t.Fatalf("unexpected title %q", ti)
		}
		want[ti] = true
	}
	for k, seen := range want {
		if !seen {
			t.Fatalf("missing title %q in %v", k, titles)
		}
	}
}

// C5 / C6.
func TestListProducesTaskWithMetadataAndExternalID(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		dms: []Message{{
			ChannelID:   "D5",
			ChannelType: "im",
			TS:          "1700000000.000100",
			ThreadTS:    "1699999999.000050",
			UserID:      "U7",
			Text:        "please review the patch",
			Permalink:   "https://slack.example/archives/D5/p1700000000000100",
		}},
	}
	p := New(WithClient(c), WithIncludeDM(true))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	tk := got[0]
	if tk.Source != "slack" {
		t.Errorf("Source = %q", tk.Source)
	}
	if tk.ExternalID != "D5:1700000000.000100" {
		t.Errorf("ExternalID = %q", tk.ExternalID)
	}
	if tk.Title != "please review the patch" {
		t.Errorf("Title = %q", tk.Title)
	}
	if tk.SourcePath != "https://slack.example/archives/D5/p1700000000000100" {
		t.Errorf("SourcePath = %q", tk.SourcePath)
	}
	checks := map[string]string{
		"channel_id":   "D5",
		"thread_ts":    "1699999999.000050",
		"ts":           "1700000000.000100",
		"user_id":      "U7",
		"channel_type": "im",
		"dm_id":        "D5",
	}
	for k, v := range checks {
		if got, ok := tk.RawMetadata[k]; !ok || got != v {
			t.Errorf("RawMetadata[%q] = %v (ok=%v), want %q", k, got, ok, v)
		}
	}
}

// C5 (multiline).
func TestListMultilineTextSplitsTitleAndBody(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		mentions: []Message{{
			ChannelID: "C9",
			TS:        "1.0",
			Text:      "fix the build\ndetails follow on the next line",
		}},
	}
	p := New(WithClient(c), WithIncludeMentions(true))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].Title != "fix the build" {
		t.Errorf("Title = %q", got[0].Title)
	}
	if !strings.Contains(got[0].Body, "details follow") {
		t.Errorf("Body did not include rest: %q", got[0].Body)
	}
}

// C5 (channel-type metadata for mentions has no dm_id).
func TestListMentionDoesNotCarryDMID(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		mentions: []Message{{ChannelID: "C9", ChannelType: "channel", TS: "1.0", Text: "x"}},
	}
	p := New(WithClient(c), WithIncludeMentions(true))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, ok := got[0].RawMetadata["dm_id"]; ok {
		t.Fatalf("mention task should not carry dm_id, RawMetadata=%v", got[0].RawMetadata)
	}
}

// C7.
func TestListPropagatesClientError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	c := &fakeClient{mentionsErr: boom}
	p := New(WithClient(c), WithIncludeMentions(true))
	_, err := p.List(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("List error = %v, want boom", err)
	}
}

// C8.
func TestListPassesEmptySinceTSToClient(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c), WithIncludeMentions(true), WithIncludeDM(true))
	if _, err := p.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(c.gotMentionsTS) != 1 || c.gotMentionsTS[0] != "" {
		t.Errorf("mentions sinceTS = %v", c.gotMentionsTS)
	}
	if len(c.gotDMsTS) != 1 || c.gotDMsTS[0] != "" {
		t.Errorf("dms sinceTS = %v", c.gotDMsTS)
	}
}

// D1.
func TestSinceWithoutCheckpointerActsLikeList(t *testing.T) {
	t.Parallel()
	c := &fakeClient{mentions: []Message{{ChannelID: "C", TS: "1.0", Text: "x"}}}
	p := New(WithClient(c), WithIncludeMentions(true))
	got, err := p.Since(context.Background(), "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if c.gotMentionsTS[0] != "" {
		t.Errorf("expected empty sinceTS, got %q", c.gotMentionsTS[0])
	}
}

// D2.
func TestSinceReadsCheckpointWhenArgIsEmpty(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	cp := newMemoryCheckpointer()
	cp.store["slack:last_ts"] = "1699999999.000050"
	p := New(WithClient(c), WithCheckpointer(cp), WithIncludeMentions(true))
	if _, err := p.Since(context.Background(), ""); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if c.gotMentionsTS[0] != "1699999999.000050" {
		t.Errorf("sinceTS = %q, want stored checkpoint", c.gotMentionsTS[0])
	}
}

// D3.
func TestSinceArgWinsOverStoredCheckpoint(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	cp := newMemoryCheckpointer()
	cp.store["slack:last_ts"] = "1699999999.000050"
	p := New(WithClient(c), WithCheckpointer(cp), WithIncludeDM(true))
	if _, err := p.Since(context.Background(), "1700000000.000000"); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if c.gotDMsTS[0] != "1700000000.000000" {
		t.Errorf("sinceTS = %q, want explicit arg", c.gotDMsTS[0])
	}
}

// D4 / D6.
func TestSincePersistsMaxTSAfterReturning(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		mentions: []Message{
			{ChannelID: "C", TS: "1700000000.000100", Text: "a"},
			{ChannelID: "C", TS: "1700000000.000300", Text: "b"},
		},
		dms: []Message{
			{ChannelID: "D", ChannelType: "im", TS: "1700000000.000200", Text: "c"},
			{ChannelID: "D", ChannelType: "im", TS: "1700000000.000050", Text: "d"},
		},
	}
	cp := newMemoryCheckpointer()
	p := New(WithClient(c), WithCheckpointer(cp), WithIncludeMentions(true), WithIncludeDM(true))
	if _, err := p.Since(context.Background(), ""); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if cp.sets["slack:last_ts"] != "1700000000.000300" {
		t.Errorf("checkpoint stored = %q, want max ts", cp.sets["slack:last_ts"])
	}
}

// D5.
func TestSinceWithZeroResultsLeavesCheckpointAlone(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	cp := newMemoryCheckpointer()
	cp.store["slack:last_ts"] = "1699999999.000000"
	p := New(WithClient(c), WithCheckpointer(cp), WithIncludeMentions(true))
	if _, err := p.Since(context.Background(), ""); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if _, set := cp.sets["slack:last_ts"]; set {
		t.Errorf("checkpoint was rewritten when no results: %q", cp.sets["slack:last_ts"])
	}
	if cp.store["slack:last_ts"] != "1699999999.000000" {
		t.Errorf("stored value mutated: %q", cp.store["slack:last_ts"])
	}
}

// D7.
func TestSinceErrorDoesNotAdvanceCheckpoint(t *testing.T) {
	t.Parallel()
	boom := errors.New("network")
	c := &fakeClient{mentionsErr: boom}
	cp := newMemoryCheckpointer()
	p := New(WithClient(c), WithCheckpointer(cp), WithIncludeMentions(true))
	_, err := p.Since(context.Background(), "")
	if !errors.Is(err, boom) {
		t.Fatalf("Since err = %v, want boom", err)
	}
	if _, set := cp.sets["slack:last_ts"]; set {
		t.Errorf("checkpoint advanced on error: %q", cp.sets["slack:last_ts"])
	}
}

// E1.
func TestCompleteSendsDMNotification(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c), WithNotifyChannelID("D-notify"))
	if err := p.Complete(context.Background(), "42"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(c.gotPostCh) != 1 || c.gotPostCh[0] != "D-notify" {
		t.Errorf("PostDM channel = %v", c.gotPostCh)
	}
	if len(c.gotPostText) != 1 || !strings.Contains(c.gotPostText[0], "#42") || !strings.Contains(c.gotPostText[0], "done") {
		t.Errorf("PostDM text = %v", c.gotPostText)
	}
}

// E2.
func TestCompleteRejectsEmptyExternalID(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c), WithNotifyChannelID("D"))
	err := p.Complete(context.Background(), "")
	if !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("err = %v, want ErrInvalidTaskID", err)
	}
}

// E3.
func TestCompleteRequiresNotifyChannel(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c))
	err := p.Complete(context.Background(), "1")
	if !errors.Is(err, ErrNotifyChannelRequired) {
		t.Fatalf("err = %v, want ErrNotifyChannelRequired", err)
	}
}

// E4.
func TestCompletePropagatesPostError(t *testing.T) {
	t.Parallel()
	boom := errors.New("post-failure")
	c := &fakeClient{postErr: boom}
	p := New(WithClient(c), WithNotifyChannelID("D"))
	err := p.Complete(context.Background(), "1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

// F1.
func TestAuthStatusForwards(t *testing.T) {
	t.Parallel()
	c := &fakeClient{auth: source.AuthExpired}
	p := New(WithClient(c))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthExpired {
		t.Fatalf("AuthStatus = %q, want %q", got, source.AuthExpired)
	}
	if c.authCalls != 1 {
		t.Errorf("authCalls = %d", c.authCalls)
	}
}

// F2.
func TestAuthStatusWithoutClientReturnsNotConfigured(t *testing.T) {
	t.Parallel()
	p := New()
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Fatalf("AuthStatus = %q, want %q", got, source.AuthNotConfigured)
	}
}

// F3.
func TestSetupForwardsNonInteractive(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	p := New(WithClient(c))
	if err := p.Setup(context.Background(), source.SetupOptions{NonInteractive: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if len(c.setupOpts) != 1 || !c.setupOpts[0] {
		t.Fatalf("setupOpts = %v", c.setupOpts)
	}
}

// F4.
func TestSetupWithoutClientReturnsErrClientNotConfigured(t *testing.T) {
	t.Parallel()
	p := New()
	err := p.Setup(context.Background(), source.SetupOptions{})
	if !errors.Is(err, ErrClientNotConfigured) {
		t.Fatalf("err = %v, want ErrClientNotConfigured", err)
	}
}
