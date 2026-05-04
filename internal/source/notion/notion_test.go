package notion

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// memCheckpointer is the same in-memory shape the markdown package uses, copied
// here verbatim so the notion test file does not have to import test code from
// a sibling package. Mirrors the (key,string) shape of internal/store.kv_state.
type memCheckpointer struct {
	mu sync.Mutex
	kv map[string]string
}

func newMemCheckpointer() *memCheckpointer {
	return &memCheckpointer{kv: map[string]string{}}
}

func (m *memCheckpointer) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.kv[key], nil
}

func (m *memCheckpointer) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv[key] = value
	return nil
}

// TestListReturnsTaskPerPage locks in the core mapping: one Notion page → one
// source.Task with ExternalID = page.id (UUID), Source unset (Plugin layer
// emits markdown.Task-style internal Task; Adapter layer stamps Source).
func TestListReturnsTaskPerPage(t *testing.T) {
	t.Parallel()

	c := &fakeClient{pages: []Page{
		{ID: "11111111-1111-1111-1111-111111111111", Title: "alpha", URL: "https://notion.so/alpha"},
		{ID: "22222222-2222-2222-2222-222222222222", Title: "beta", URL: "https://notion.so/beta", Archived: true},
	}}
	p := New(WithClient(c), WithDatabaseID("db-1"))

	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, %+v", len(got), got)
	}
	if got[0].ExternalID != "11111111-1111-1111-1111-111111111111" || got[0].Title != "alpha" {
		t.Errorf("task[0] = %+v", got[0])
	}
	if got[0].SourcePath != "https://notion.so/alpha" {
		t.Errorf("task[0].SourcePath = %q", got[0].SourcePath)
	}
	if got[1].Done != true {
		t.Errorf("task[1].Done = %v, want true (archived page)", got[1].Done)
	}
}

// TestListEmptyDatabase exercises the "fresh database, no pages yet" path.
// We must return an empty slice with no error so the caller's len()/range
// loop sees zero rows rather than a typed sentinel.
func TestListEmptyDatabase(t *testing.T) {
	t.Parallel()

	p := New(WithClient(&fakeClient{}), WithDatabaseID("db"))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0, got %+v", got)
	}
}

// TestListPropagatesClientError verifies an upstream HTTP failure surfaces
// to the caller verbatim — no swallowing, no wrap that hides the typed
// sentinel. Discovery scheduler depends on errors.Is(err, ctx.Canceled)
// etc. round-tripping through here.
func TestListPropagatesClientError(t *testing.T) {
	t.Parallel()

	want := errors.New("upstream boom")
	p := New(WithClient(&fakeClient{queryErr: want}), WithDatabaseID("db"))
	_, err := p.List(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// TestListWithoutDatabaseIDReturnsTyped: like markdown's ErrNoFilesConfigured,
// the Plugin must refuse to call upstream when nothing is configured. A
// silent empty result would mask the misconfiguration until the user wonders
// why no Notion tasks ever materialise.
func TestListWithoutDatabaseIDReturnsTyped(t *testing.T) {
	t.Parallel()

	p := New(WithClient(&fakeClient{}))
	_, err := p.List(context.Background())
	if !errors.Is(err, ErrDatabaseIDRequired) {
		t.Fatalf("err = %v, want ErrDatabaseIDRequired", err)
	}
}

// TestListPopulatesProvenanceFields locks in the provenance contract on the
// inner Task: every Task carries last_edited_time and database_id so the
// Adapter can lift them into source.Task.RawMetadata without re-querying
// Notion. The source.Task-level assertion lives in adapter_test.go.
func TestListPopulatesProvenanceFields(t *testing.T) {
	t.Parallel()

	c := &fakeClient{pages: []Page{
		{ID: "abc", Title: "x", LastEditedTime: "2025-01-01T00:00:00.000Z"},
	}}
	p := New(WithClient(c), WithDatabaseID("db-1"))

	got, err := p.List(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("List: %v / %+v", err, got)
	}
	if got[0].LastEditedTime != "2025-01-01T00:00:00.000Z" {
		t.Errorf("LastEditedTime = %q", got[0].LastEditedTime)
	}
	if got[0].DatabaseID != "db-1" {
		t.Errorf("DatabaseID = %q", got[0].DatabaseID)
	}
}

// TestSinceFirstCallReturnsAllAndCheckpoints exercises the bootstrap path:
// no checkpoint stored yet, so we return everything and write the latest
// last_edited_time to the kv state. Subsequent calls (S2) compare against it.
func TestSinceFirstCallReturnsAllAndCheckpoints(t *testing.T) {
	t.Parallel()

	c := &fakeClient{pages: []Page{
		{ID: "p1", Title: "old", LastEditedTime: "2025-01-01T00:00:00.000Z"},
		{ID: "p2", Title: "new", LastEditedTime: "2025-06-01T00:00:00.000Z"},
	}}
	cp := newMemCheckpointer()
	p := New(WithClient(c), WithDatabaseID("db"), WithCheckpointer(cp))

	got, err := p.Since(context.Background())
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("first Since len=%d", len(got))
	}
	stored, _ := cp.Get(context.Background(), checkpointKey("db"))
	if stored != "2025-06-01T00:00:00.000Z" {
		t.Fatalf("checkpoint = %q, want 2025-06-01...", stored)
	}
}

// TestSinceUsesStoredCheckpointAsFilter — second call only sees pages newer
// than the stored value. Asserts both the filter shape (we ask the API for
// >= checkpoint) and the result mapping (only the newer page comes back).
func TestSinceUsesStoredCheckpointAsFilter(t *testing.T) {
	t.Parallel()

	c := &fakeClient{pages: []Page{
		{ID: "p1", Title: "old", LastEditedTime: "2025-01-01T00:00:00.000Z"},
		{ID: "p2", Title: "newer", LastEditedTime: "2025-06-01T00:00:00.000Z"},
		{ID: "p3", Title: "newest", LastEditedTime: "2025-09-01T00:00:00.000Z"},
	}}
	cp := newMemCheckpointer()
	_ = cp.Set(context.Background(), checkpointKey("db"), "2025-06-01T00:00:00.000Z")

	p := New(WithClient(c), WithDatabaseID("db"), WithCheckpointer(cp))
	got, err := p.Since(context.Background())
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	// "on or after" inclusive, so p2 and p3 come back. The Plugin must
	// also advance the checkpoint to p3's timestamp.
	if len(got) != 2 {
		t.Fatalf("len=%d, %+v", len(got), got)
	}
	stored, _ := cp.Get(context.Background(), checkpointKey("db"))
	if stored != "2025-09-01T00:00:00.000Z" {
		t.Fatalf("checkpoint advanced to %q, want 2025-09-01...", stored)
	}
}

// TestSinceWithoutCheckpointerDegradesToList — same contract as the markdown
// package: a one-shot CLI invocation has no place to store state, so Since
// returns everything (List-equivalent) and does not error.
func TestSinceWithoutCheckpointerDegradesToList(t *testing.T) {
	t.Parallel()

	c := &fakeClient{pages: []Page{
		{ID: "p1", Title: "x", LastEditedTime: "2025-01-01T00:00:00.000Z"},
	}}
	p := New(WithClient(c), WithDatabaseID("db"))
	got, err := p.Since(context.Background())
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
}

// TestSinceEmptyResultLeavesCheckpointUnchanged — guards against a regression
// where "no new pages" overwrites the checkpoint with the empty string,
// effectively resetting future Since calls.
func TestSinceEmptyResultLeavesCheckpointUnchanged(t *testing.T) {
	t.Parallel()

	cp := newMemCheckpointer()
	_ = cp.Set(context.Background(), checkpointKey("db"), "2025-06-01T00:00:00.000Z")

	c := &fakeClient{pages: nil} // upstream has nothing newer
	p := New(WithClient(c), WithDatabaseID("db"), WithCheckpointer(cp))
	got, err := p.Since(context.Background())
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0, got %+v", got)
	}
	stored, _ := cp.Get(context.Background(), checkpointKey("db"))
	if stored != "2025-06-01T00:00:00.000Z" {
		t.Errorf("checkpoint clobbered to %q", stored)
	}
}

// TestSinceMonotone — even if upstream returns a page whose last_edited_time
// is older than the stored checkpoint (a clock skew or a manually-restored
// page), the checkpoint must not regress. Monotone advance is the property
// kvstate.CompareAndSwap exists for; here we exercise the same invariant
// at the Plugin layer.
func TestSinceMonotone(t *testing.T) {
	t.Parallel()

	cp := newMemCheckpointer()
	_ = cp.Set(context.Background(), checkpointKey("db"), "2025-06-01T00:00:00.000Z")

	c := &fakeClient{pages: []Page{
		{ID: "p1", Title: "older", LastEditedTime: "2025-01-01T00:00:00.000Z"},
	}}
	p := New(WithClient(c), WithDatabaseID("db"), WithCheckpointer(cp))
	if _, err := p.Since(context.Background()); err != nil {
		t.Fatalf("Since: %v", err)
	}
	stored, _ := cp.Get(context.Background(), checkpointKey("db"))
	if stored != "2025-06-01T00:00:00.000Z" {
		t.Errorf("checkpoint regressed to %q", stored)
	}
}

// TestSinceFiltersByOnOrAfter checks the *request* shape: when a checkpoint
// is stored, the Plugin must hand it down to QueryDatabase as OnOrAfter so
// we are not paging through the entire database every poll. Without this
// the network footprint grows unbounded as the database grows.
func TestSinceFiltersByOnOrAfter(t *testing.T) {
	t.Parallel()

	cp := newMemCheckpointer()
	_ = cp.Set(context.Background(), checkpointKey("db"), "2025-06-01T00:00:00.000Z")

	rec := &recordingClient{inner: &fakeClient{}}
	p := New(WithClient(rec), WithDatabaseID("db"), WithCheckpointer(cp))
	if _, err := p.Since(context.Background()); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if rec.lastOpts.OnOrAfter != "2025-06-01T00:00:00.000Z" {
		t.Fatalf("OnOrAfter = %q, want stored checkpoint", rec.lastOpts.OnOrAfter)
	}
}

// recordingClient wraps a fakeClient and records the most recent QueryOptions
// so a test can assert on the request shape (rather than just the response).
// Lives in the test file because the production Plugin has no need for it.
type recordingClient struct {
	inner    *fakeClient
	lastOpts QueryOptions
}

func (r *recordingClient) QueryDatabase(ctx context.Context, db string, opts QueryOptions) ([]Page, error) {
	r.lastOpts = opts
	return r.inner.QueryDatabase(ctx, db, opts)
}

func (r *recordingClient) UsersMe(ctx context.Context) error {
	return r.inner.UsersMe(ctx)
}

func (r *recordingClient) CreatePage(ctx context.Context, db, title string) (Page, error) {
	return r.inner.CreatePage(ctx, db, title)
}

func (r *recordingClient) UpdatePage(ctx context.Context, id string, archived bool) error {
	return r.inner.UpdatePage(ctx, id, archived)
}
