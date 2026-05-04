package gmail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// fakeClient is the in-memory Gmail client used across the package tests.
// It records every call so an assertion can verify "Complete issued
// exactly one ModifyLabels with the right add/remove sets" without having
// to mock a real API surface.
type fakeClient struct {
	mu sync.Mutex

	// Programmable state.
	messages       []Message
	listErr        error
	modifyErr      map[string]error // per-id override; zero value means "succeed".
	authErr        error
	authStatusFn   func(ctx context.Context) (source.AuthStatus, error)
	authenticateFn func(ctx context.Context, opts source.SetupOptions) error

	// Captures so tests can assert on call shape.
	listQueries []string
	modifyCalls []modifyCall
	authCalls   []source.SetupOptions
}

type modifyCall struct {
	ID  string
	Req ModifyLabelsRequest
}

func (f *fakeClient) List(_ context.Context, query string) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listQueries = append(f.listQueries, query)
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Message, len(f.messages))
	copy(out, f.messages)
	return out, nil
}

func (f *fakeClient) ModifyLabels(_ context.Context, id string, req ModifyLabelsRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modifyCalls = append(f.modifyCalls, modifyCall{ID: id, Req: req})
	if err, ok := f.modifyErr[id]; ok {
		return err
	}
	// Also fail when the id is not in the message list, so tests can
	// drive the "not found" path without manual modifyErr setup.
	for _, m := range f.messages {
		if m.ID == id {
			return nil
		}
	}
	return ErrClientMessageNotFound
}

func (f *fakeClient) Authenticate(ctx context.Context, opts source.SetupOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authCalls = append(f.authCalls, opts)
	if f.authenticateFn != nil {
		return f.authenticateFn(ctx, opts)
	}
	return f.authErr
}

func (f *fakeClient) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authStatusFn != nil {
		return f.authStatusFn(ctx)
	}
	return source.AuthAuthenticated, nil
}

// fakeCheckpointer is the in-memory KV used for Since tests.
type fakeCheckpointer struct {
	mu     sync.Mutex
	values map[string]string
	getErr error
	setErr error
}

func newFakeCheckpointer() *fakeCheckpointer {
	return &fakeCheckpointer{values: map[string]string{}}
}

func (c *fakeCheckpointer) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return "", c.getErr
	}
	return c.values[key], nil
}

func (c *fakeCheckpointer) Set(_ context.Context, key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErr != nil {
		return c.setErr
	}
	c.values[key] = value
	return nil
}

// ----- A. Plugin construction -------------------------------------------------

// TestNewDefaultsMatchPR80Brief locks down the documented defaults so a
// future config refactor cannot silently change the discovery query or
// archive label, which would invalidate every existing user's mail
// rules.
func TestNewDefaultsMatchPR80Brief(t *testing.T) {
	t.Parallel()

	p := New()
	if p.Query() != DefaultQuery {
		t.Errorf("Query() = %q, want %q", p.Query(), DefaultQuery)
	}
	if p.Query() != "is:unread to:me -label:auto-archived" {
		t.Errorf("DefaultQuery drifted from PR-80 brief: %q", p.Query())
	}
	if p.CompleteLabel() != DefaultCompleteLabel {
		t.Errorf("CompleteLabel() = %q, want %q", p.CompleteLabel(), DefaultCompleteLabel)
	}
	if p.CheckpointKey() != DefaultCheckpointKey {
		t.Errorf("CheckpointKey() = %q, want %q", p.CheckpointKey(), DefaultCheckpointKey)
	}
	if p.CheckpointKey() != "gmail_last_id" {
		t.Errorf("DefaultCheckpointKey drifted from requirement.md: %q", p.CheckpointKey())
	}
}

func TestWithQueryOverridesDefault(t *testing.T) {
	t.Parallel()

	p := New(WithQuery("from:boss is:starred"))
	if p.Query() != "from:boss is:starred" {
		t.Errorf("Query() = %q", p.Query())
	}
}

func TestWithCompleteLabelOverridesDefault(t *testing.T) {
	t.Parallel()

	p := New(WithCompleteLabel("Marunage/Done"))
	if p.CompleteLabel() != "Marunage/Done" {
		t.Errorf("CompleteLabel() = %q", p.CompleteLabel())
	}
}

func TestWithCheckpointKeyOverridesDefault(t *testing.T) {
	t.Parallel()

	p := New(WithCheckpointKey("gmail_account_b_last_id"))
	if p.CheckpointKey() != "gmail_account_b_last_id" {
		t.Errorf("CheckpointKey() = %q", p.CheckpointKey())
	}
}

// ----- B. List ----------------------------------------------------------------

// TestListWithoutClientReturnsErrClientNotSet defends the contract that
// the plugin never silently no-ops when wired without a client; a daemon
// that loads a half-configured plugin must surface the misconfiguration
// loudly instead of returning empty result sets forever.
func TestListWithoutClientReturnsErrClientNotSet(t *testing.T) {
	t.Parallel()

	p := New()
	_, err := p.List(context.Background())
	if !errors.Is(err, ErrClientNotSet) {
		t.Fatalf("List: want ErrClientNotSet, got %v", err)
	}
}

func TestListEmptyClientReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{}
	p := New(WithClient(fc))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestListConvertsMessagesToTasks(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{
		messages: []Message{
			{
				ID:       "m1",
				ThreadID: "t1",
				Subject:  "first",
				Snippet:  "preview body",
				Labels:   []string{"INBOX", "UNREAD"},
				From:     "alice@example.com",
			},
			{
				ID:       "m2",
				ThreadID: "t2",
				Subject:  "second",
				Snippet:  "another preview",
				Labels:   []string{"INBOX"},
			},
		},
	}
	p := New(WithClient(fc))

	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, %+v", len(got), got)
	}
	if got[0].Source != "gmail" {
		t.Errorf("task[0].Source = %q", got[0].Source)
	}
	if got[0].ExternalID != "m1" {
		t.Errorf("task[0].ExternalID = %q", got[0].ExternalID)
	}
	if got[0].Title != "first" {
		t.Errorf("task[0].Title = %q", got[0].Title)
	}
	if got[0].Body != "preview body" {
		t.Errorf("task[0].Body = %q", got[0].Body)
	}
	if got[0].Done {
		t.Errorf("task[0].Done = true; UNREAD label, no archive label, want Done=false")
	}
	if !strings.Contains(got[0].SourcePath, "m1") {
		t.Errorf("task[0].SourcePath = %q (should reference message id)", got[0].SourcePath)
	}
	if got[0].RawMetadata["thread_id"] != "t1" {
		t.Errorf("task[0].RawMetadata[thread_id] = %v", got[0].RawMetadata["thread_id"])
	}
	if got[0].RawMetadata["from"] != "alice@example.com" {
		t.Errorf("task[0].RawMetadata[from] = %v", got[0].RawMetadata["from"])
	}
	gotLabels, _ := got[0].RawMetadata["labels"].([]string)
	if len(gotLabels) != 2 || gotLabels[0] != "INBOX" || gotLabels[1] != "UNREAD" {
		t.Errorf("task[0].RawMetadata[labels] = %v", gotLabels)
	}
}

// TestListOmitsEmptyRawMetadataFrom keeps the Task.RawMetadata bag tidy
// for downstream consumers (web UI, audit log): a sender-less message
// should not produce a `"from": ""` entry that has to be filtered out
// by every reader. The brief equates "no sender" with "key not present"
// and other Phase 1 sources (markdown) follow the same omit-on-empty
// rule for optional fields.
func TestListOmitsEmptyRawMetadataFrom(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{
		{ID: "m1", Subject: "no-sender"},
	}}
	p := New(WithClient(fc))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, present := got[0].RawMetadata["from"]; present {
		t.Errorf("RawMetadata[\"from\"] should be omitted when empty; got %#v", got[0].RawMetadata)
	}
}

// TestListOmitsEmptyRawMetadataThreadIDAndLabels mirrors the from-omit
// rule for the other two sparse RawMetadata keys. Without this, a
// future refactor of toTask could quietly start emitting
// `"thread_id": ""` / `"labels": nil` while only the `from` branch
// keeps its regression guard.
func TestListOmitsEmptyRawMetadataThreadIDAndLabels(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{
		{ID: "m1", Subject: "bare"}, // no ThreadID, no Labels, no From
	}}
	p := New(WithClient(fc))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, present := got[0].RawMetadata["thread_id"]; present {
		t.Errorf("thread_id should be omitted when empty; got %#v", got[0].RawMetadata)
	}
	if _, present := got[0].RawMetadata["labels"]; present {
		t.Errorf("labels should be omitted when empty; got %#v", got[0].RawMetadata)
	}
}

// TestListLabelsAreDefensiveCopy pins the defensive-copy contract on
// RawMetadata["labels"]. Without it, a downstream consumer mutating
// the returned slice (e.g. sort, append) could reach back into the
// Client's Message slice and cause a confusing aliasing bug under
// concurrent List calls.
func TestListLabelsAreDefensiveCopy(t *testing.T) {
	t.Parallel()

	original := []string{"INBOX", "UNREAD"}
	fc := &fakeClient{messages: []Message{
		{ID: "m1", Subject: "x", Labels: original},
	}}
	p := New(WithClient(fc))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotLabels, ok := got[0].RawMetadata["labels"].([]string)
	if !ok || len(gotLabels) != 2 {
		t.Fatalf("labels missing/wrong shape: %#v", got[0].RawMetadata["labels"])
	}
	gotLabels[0] = "MUTATED"
	if original[0] != "INBOX" {
		t.Errorf("mutating RawMetadata[\"labels\"] reached back into the client's slice: original = %v", original)
	}
}

// TestListMarksDoneWhenArchiveLabelPresent: the queue's reconciliation
// path needs the upstream Done flag so it can mark a queue row finished
// when the user archived the mail directly in Gmail. Without this, two
// systems would carry conflicting "is this task done?" answers.
func TestListMarksDoneWhenArchiveLabelPresent(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{
		messages: []Message{
			{ID: "m1", Subject: "already archived", Labels: []string{"auto-archived"}},
		},
	}
	p := New(WithClient(fc))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !got[0].Done {
		t.Errorf("task[0].Done = false; archive label should set Done=true")
	}
}

func TestListPassesConfiguredQueryToClient(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{}
	p := New(WithClient(fc), WithQuery("from:boss is:starred"))
	if _, err := p.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(fc.listQueries) != 1 || fc.listQueries[0] != "from:boss is:starred" {
		t.Errorf("client.List queries = %v", fc.listQueries)
	}
}

func TestListWrapsClientError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("upstream 503")
	fc := &fakeClient{listErr: sentinel}
	p := New(WithClient(fc))

	_, err := p.List(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("List err = %v; want wrap of sentinel", err)
	}
}

func TestListHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{}
	p := New(WithClient(fc))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.List(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("List err = %v; want context.Canceled", err)
	}
	if len(fc.listQueries) != 0 {
		t.Errorf("client.List should not have been invoked on cancelled ctx")
	}
}

// ----- C. Since (Sincer) ------------------------------------------------------

func TestSinceWithoutCheckpointerFallsBackToList(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{{ID: "m1", Subject: "alpha"}}}
	p := New(WithClient(fc))
	got, err := p.Since(context.Background(), "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 1 || got[0].Title != "alpha" {
		t.Fatalf("Since fallback returned %+v", got)
	}
}

func TestSinceFirstCallReturnsAllAndPersistsHead(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{
		{ID: "m3", Subject: "newest"},
		{ID: "m2", Subject: "middle"},
		{ID: "m1", Subject: "oldest"},
	}}
	cp := newFakeCheckpointer()
	p := New(WithClient(fc), WithCheckpointer(cp))

	got, err := p.Since(context.Background(), "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if cp.values[DefaultCheckpointKey] != "m3" {
		t.Errorf("checkpoint = %q, want m3", cp.values[DefaultCheckpointKey])
	}
}

// TestSinceFiltersAtCheckpoint: the second call should return only
// messages that arrived AFTER the checkpoint. The plugin's contract
// (newest-first ordering) means the cutoff is "stop at the first id
// equal to the checkpoint".
func TestSinceFiltersAtCheckpoint(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{
		{ID: "m5", Subject: "newest"},
		{ID: "m4", Subject: "newer"},
		{ID: "m3", Subject: "boundary"},
		{ID: "m2", Subject: "older"},
	}}
	cp := newFakeCheckpointer()
	cp.values[DefaultCheckpointKey] = "m3"
	p := New(WithClient(fc), WithCheckpointer(cp))

	got, err := p.Since(context.Background(), "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, %+v", len(got), got)
	}
	if got[0].ExternalID != "m5" || got[1].ExternalID != "m4" {
		t.Errorf("got = %+v", got)
	}
	if cp.values[DefaultCheckpointKey] != "m5" {
		t.Errorf("checkpoint = %q, want m5", cp.values[DefaultCheckpointKey])
	}
}

func TestSinceEmptyResultLeavesCheckpointUntouched(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{
		{ID: "m3", Subject: "boundary"},
		{ID: "m2", Subject: "older"},
	}}
	cp := newFakeCheckpointer()
	cp.values[DefaultCheckpointKey] = "m3"
	p := New(WithClient(fc), WithCheckpointer(cp))

	got, err := p.Since(context.Background(), "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d", len(got))
	}
	if cp.values[DefaultCheckpointKey] != "m3" {
		t.Errorf("checkpoint advanced to %q on empty result", cp.values[DefaultCheckpointKey])
	}
}

func TestSinceUsesConfiguredCheckpointKey(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{{ID: "m1"}}}
	cp := newFakeCheckpointer()
	p := New(WithClient(fc), WithCheckpointer(cp), WithCheckpointKey("custom_key"))

	if _, err := p.Since(context.Background(), ""); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if cp.values["custom_key"] != "m1" {
		t.Errorf("custom_key = %q", cp.values["custom_key"])
	}
	if _, ok := cp.values[DefaultCheckpointKey]; ok {
		t.Errorf("default key should not have been written: %v", cp.values)
	}
}

func TestSincePropagatesClientErrorAndKeepsCheckpoint(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("upstream down")
	fc := &fakeClient{listErr: sentinel}
	cp := newFakeCheckpointer()
	cp.values[DefaultCheckpointKey] = "m9"
	p := New(WithClient(fc), WithCheckpointer(cp))

	if _, err := p.Since(context.Background(), ""); !errors.Is(err, sentinel) {
		t.Errorf("err = %v; want wrap of sentinel", err)
	}
	if cp.values[DefaultCheckpointKey] != "m9" {
		t.Errorf("checkpoint mutated despite client error: %q", cp.values[DefaultCheckpointKey])
	}
}

func TestSinceWithoutClientReturnsErrClientNotSet(t *testing.T) {
	t.Parallel()

	cp := newFakeCheckpointer()
	p := New(WithCheckpointer(cp))
	if _, err := p.Since(context.Background(), ""); !errors.Is(err, ErrClientNotSet) {
		t.Errorf("err = %v; want ErrClientNotSet", err)
	}
}

// TestSinceHonoursContextCancellation closes a coverage hole the first
// review pass spotted: List has a cancellation test but Since's
// equivalent guard (after the Checkpointer fallback branch) was
// untested. A future refactor that drops the ctx.Err() check would now
// flip this red.
func TestSinceHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{{ID: "m1"}}}
	cp := newFakeCheckpointer()
	p := New(WithClient(fc), WithCheckpointer(cp))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Since(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if len(fc.listQueries) != 0 {
		t.Errorf("client.List should not have been invoked on cancelled ctx")
	}
}

// TestSinceWrapsCheckpointerGetError pins the documented contract:
// Checkpointer.Get errors must surface to the caller (wrapped) rather
// than be silently treated as "no checkpoint yet". Without this, a KV
// outage would look like a fresh run and re-process every upstream
// message.
func TestSinceWrapsCheckpointerGetError(t *testing.T) {
	t.Parallel()

	getErr := errors.New("kv unavailable")
	fc := &fakeClient{messages: []Message{{ID: "m1"}}}
	cp := newFakeCheckpointer()
	cp.getErr = getErr
	p := New(WithClient(fc), WithCheckpointer(cp))

	if _, err := p.Since(context.Background(), ""); !errors.Is(err, getErr) {
		t.Errorf("err = %v; want wrap of getErr", err)
	}
	if len(fc.listQueries) != 0 {
		t.Errorf("client.List should not have run after a checkpoint Get error")
	}
}

// TestSinceWrapsCheckpointerSetError covers the symmetric write path:
// after a successful upstream fetch, a Set failure must surface to the
// caller. The plugin discards the in-flight task slice on this error
// (returns nil + wrap) — the next Since call will refetch from upstream
// rather than risk handing the caller tasks that may not be replayable
// if the checkpoint never reaches durable storage.
func TestSinceWrapsCheckpointerSetError(t *testing.T) {
	t.Parallel()

	setErr := errors.New("kv write failed")
	fc := &fakeClient{messages: []Message{{ID: "m1"}}}
	cp := newFakeCheckpointer()
	cp.setErr = setErr
	p := New(WithClient(fc), WithCheckpointer(cp))

	tasks, err := p.Since(context.Background(), "")
	if !errors.Is(err, setErr) {
		t.Errorf("err = %v; want wrap of setErr", err)
	}
	if tasks != nil {
		t.Errorf("tasks = %+v; want nil on Set failure (caller must refetch)", tasks)
	}
}

// ----- D. Complete (Completer) ------------------------------------------------

func TestCompleteAddsArchiveAndRemovesUnreadInOneCall(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{{ID: "m1"}}}
	p := New(WithClient(fc))

	if err := p.Complete(context.Background(), "m1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(fc.modifyCalls) != 1 {
		t.Fatalf("ModifyLabels calls = %d, want 1", len(fc.modifyCalls))
	}
	call := fc.modifyCalls[0]
	if call.ID != "m1" {
		t.Errorf("call.ID = %q", call.ID)
	}
	if len(call.Req.AddLabels) != 1 || call.Req.AddLabels[0] != DefaultCompleteLabel {
		t.Errorf("AddLabels = %v", call.Req.AddLabels)
	}
	if len(call.Req.RemoveLabels) != 1 || call.Req.RemoveLabels[0] != "UNREAD" {
		t.Errorf("RemoveLabels = %v", call.Req.RemoveLabels)
	}
}

func TestCompleteUsesConfiguredLabel(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{{ID: "m1"}}}
	p := New(WithClient(fc), WithCompleteLabel("Marunage/Done"))

	if err := p.Complete(context.Background(), "m1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if fc.modifyCalls[0].Req.AddLabels[0] != "Marunage/Done" {
		t.Errorf("AddLabels = %v", fc.modifyCalls[0].Req.AddLabels)
	}
}

func TestCompleteUnknownIDReturnsErrTaskNotFound(t *testing.T) {
	t.Parallel()

	// Explicitly programme the fake to return ErrClientMessageNotFound for
	// the id we will probe. Relying on the fake's "any unknown id ->
	// not-found" convenience is honest behaviour for adapter tests but
	// hides the exact mapping the plugin promises (client-not-found ->
	// ErrTaskNotFound), so this test pins the contract directly.
	fc := &fakeClient{
		modifyErr: map[string]error{"ghost": ErrClientMessageNotFound},
	}
	p := New(WithClient(fc))

	err := p.Complete(context.Background(), "ghost")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("err = %v; want ErrTaskNotFound", err)
	}
}

func TestCompletePropagatesOtherClientErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("rate limited")
	fc := &fakeClient{
		messages:  []Message{{ID: "m1"}},
		modifyErr: map[string]error{"m1": sentinel},
	}
	p := New(WithClient(fc))

	err := p.Complete(context.Background(), "m1")
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v; want wrap of sentinel", err)
	}
}

func TestCompleteWithoutClientReturnsErrClientNotSet(t *testing.T) {
	t.Parallel()

	p := New()
	if err := p.Complete(context.Background(), "m1"); !errors.Is(err, ErrClientNotSet) {
		t.Errorf("err = %v", err)
	}
}

func TestCompleteHonoursContext(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{{ID: "m1"}}}
	p := New(WithClient(fc))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Complete(ctx, "m1"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if len(fc.modifyCalls) != 0 {
		t.Errorf("ModifyLabels should not have been invoked")
	}
}

// ----- E. AuthStatus ----------------------------------------------------------

func TestAuthStatusNilClientReportsNotConfigured(t *testing.T) {
	t.Parallel()

	p := New()
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Errorf("got = %q", got)
	}
}

func TestAuthStatusForwardsAuthenticated(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{authStatusFn: func(context.Context) (source.AuthStatus, error) {
		return source.AuthAuthenticated, nil
	}}
	p := New(WithClient(fc))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("got = %q", got)
	}
}

func TestAuthStatusMapsClientSentinels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want source.AuthStatus
	}{
		{"not_configured", ErrClientNotConfigured, source.AuthNotConfigured},
		{"expired", ErrClientCredentialsExpired, source.AuthExpired},
		{"revoked", ErrClientCredentialsRevoked, source.AuthRevoked},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fc := &fakeClient{authStatusFn: func(context.Context) (source.AuthStatus, error) {
				return "", tc.err
			}}
			p := New(WithClient(fc))
			got, err := p.AuthStatus(context.Background())
			if err != nil {
				t.Fatalf("AuthStatus: %v", err)
			}
			if got != tc.want {
				t.Errorf("got = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestAuthStatusWrapsUnclassifiedErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("network down")
	fc := &fakeClient{authStatusFn: func(context.Context) (source.AuthStatus, error) {
		return "", sentinel
	}}
	p := New(WithClient(fc))
	if _, err := p.AuthStatus(context.Background()); !errors.Is(err, sentinel) {
		t.Errorf("err = %v", err)
	}
}

// ----- F. Setup ---------------------------------------------------------------

func TestSetupForwardsOptionsToClient(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{}
	p := New(WithClient(fc))
	if err := p.Setup(context.Background(), source.SetupOptions{NonInteractive: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if len(fc.authCalls) != 1 || !fc.authCalls[0].NonInteractive {
		t.Errorf("authCalls = %+v", fc.authCalls)
	}
}

func TestSetupWithoutClientReturnsErrClientNotSet(t *testing.T) {
	t.Parallel()

	p := New()
	if err := p.Setup(context.Background(), source.SetupOptions{}); !errors.Is(err, ErrClientNotSet) {
		t.Errorf("err = %v", err)
	}
}

func TestSetupWrapsClientError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("auth refused")
	fc := &fakeClient{authErr: sentinel}
	p := New(WithClient(fc))
	if err := p.Setup(context.Background(), source.SetupOptions{}); !errors.Is(err, sentinel) {
		t.Errorf("err = %v", err)
	}
}

// TestSetupHonoursContext mirrors List/Complete: a cancelled ctx must
// not invoke the client. Symmetric coverage so a future refactor that
// drops the ctx pre-check goes red here, not in production.
func TestSetupHonoursContext(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{}
	p := New(WithClient(fc))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Setup(ctx, source.SetupOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if len(fc.authCalls) != 0 {
		t.Errorf("client.Authenticate should not have been invoked on cancelled ctx")
	}
}

// TestAuthStatusHonoursContext mirrors the Setup test for the
// credential probe. Same rationale: cancellation is a hard "do not
// touch the network" signal that the plugin honours up-front.
func TestAuthStatusHonoursContext(t *testing.T) {
	t.Parallel()

	called := false
	fc := &fakeClient{authStatusFn: func(context.Context) (source.AuthStatus, error) {
		called = true
		return source.AuthAuthenticated, nil
	}}
	p := New(WithClient(fc))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.AuthStatus(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if called {
		t.Errorf("client.AuthStatus should not have been invoked on cancelled ctx")
	}
}
