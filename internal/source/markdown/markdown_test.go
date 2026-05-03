package markdown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFile is a small helper that creates a fresh Markdown file under
// t.TempDir() with the given body. Centralised so individual tests stay
// focused on the assertion they care about.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return path
}

func TestListReturnsTasksFromSingleFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "# Notes\n\n- [ ] foo\n- [x] bar\n")

	p := New(WithFiles(path))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 tasks, got %d: %+v", len(got), got)
	}
	if got[0].Title != "foo" || got[0].Done {
		t.Errorf("task[0] = %+v", got[0])
	}
	if got[1].Title != "bar" || !got[1].Done {
		t.Errorf("task[1] = %+v", got[1])
	}
}

func TestListReturnsTasksFromMultipleFilesInOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a := writeFile(t, dir, "a.md", "- [ ] alpha\n")
	b := writeFile(t, dir, "b.md", "- [ ] beta\n")

	p := New(WithFiles(a, b))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	titles := make([]string, len(got))
	for i, tk := range got {
		titles[i] = tk.Title
	}
	want := []string{"alpha", "beta"}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("titles = %v, want %v", titles, want)
		}
	}
}

func TestListMissingFileReturnsEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.md")
	p := New(WithFiles(missing))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 tasks, got %+v", got)
	}
}

func TestListGeneratesAndPersistsExternalID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "- [ ] foo\n")

	p := New(WithFiles(path))
	first, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List#1: %v", err)
	}
	if len(first) != 1 || first[0].ExternalID == "" {
		t.Fatalf("first list missing ExternalID: %+v", first)
	}

	// File should now contain the marker we just emitted.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after list: %v", err)
	}
	if !strings.Contains(string(body), "<!-- marunage:id="+first[0].ExternalID) {
		t.Fatalf("marker not persisted, body=%q", body)
	}

	// Second list should observe the same ExternalID and not double-write.
	second, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List#2: %v", err)
	}
	if len(second) != 1 || second[0].ExternalID != first[0].ExternalID {
		t.Fatalf("ExternalID not stable: first=%v second=%v", first, second)
	}
}

func TestListExistingMarkerIsReused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md",
		"- [ ] foo <!-- marunage:id=already-set source=markdown -->\n")

	p := New(WithFiles(path))
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ExternalID != "already-set" {
		t.Fatalf("want ExternalID=already-set, got %+v", got)
	}
}

func TestListInvalidMarkerSurfacesTypedError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "- [ ] foo <!-- marunage:bogus -->\n")
	p := New(WithFiles(path))
	_, err := p.List(context.Background())
	if !errors.Is(err, ErrInvalidMarker) {
		t.Fatalf("want ErrInvalidMarker, got %v", err)
	}
}

// memCheckpointer is an in-memory Checkpointer that lets Since tests run
// without standing up the SQLite-backed KVStateRepo (which lives in a
// separate PR). Storing values in a map mirrors the (key,string) shape
// of kv_state exactly enough for these tests.
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

func TestSinceFirstCallReturnsAllAndPersistsCheckpoint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "- [ ] foo\n")

	cp := newMemCheckpointer()
	p := New(WithFiles(path), WithCheckpointer(cp))
	got, err := p.Since(context.Background())
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 1 || got[0].Title != "foo" {
		t.Fatalf("first Since = %+v", got)
	}
	// Checkpoint must be set so the next Since call can compare against it.
	v, _ := cp.Get(context.Background(), checkpointKey(path))
	if v == "" {
		t.Fatalf("checkpoint not persisted, kv=%v", cp.kv)
	}
}

func TestSinceSkipsUnchangedFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "- [ ] foo\n")
	cp := newMemCheckpointer()
	p := New(WithFiles(path), WithCheckpointer(cp))

	if _, err := p.Since(context.Background()); err != nil {
		t.Fatalf("Since#1: %v", err)
	}
	// Second call without touching the file: the marker we just wrote
	// did update the file, so the second call should still observe a
	// change and return the marker-bearing version. The third call
	// however should be empty.
	if _, err := p.Since(context.Background()); err != nil {
		t.Fatalf("Since#2: %v", err)
	}
	got, err := p.Since(context.Background())
	if err != nil {
		t.Fatalf("Since#3: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("third Since should be empty, got %+v", got)
	}
}

func TestAddAppendsTaskWithMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "# Notes\n\n- [ ] existing\n")
	p := New(WithFiles(path))

	got, err := p.Add(context.Background(), "new task", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.Title != "new task" || got.Done || got.ExternalID == "" {
		t.Fatalf("returned task = %+v", got)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "- [ ] new task <!-- marunage:id="+got.ExternalID) {
		t.Fatalf("body missing new task line:\n%s", body)
	}
	// Existing content must be preserved.
	if !strings.Contains(string(body), "- [ ] existing") {
		t.Fatalf("existing line lost:\n%s", body)
	}
}

func TestAddRequiresAtLeastOneFile(t *testing.T) {
	t.Parallel()

	p := New()
	_, err := p.Add(context.Background(), "x", "")
	if !errors.Is(err, ErrNoFilesConfigured) {
		t.Fatalf("want ErrNoFilesConfigured, got %v", err)
	}
}

func TestCompleteFlipsCheckbox(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md",
		"- [ ] foo <!-- marunage:id=abc source=markdown -->\n")
	p := New(WithFiles(path))

	if err := p.Complete(context.Background(), "abc"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "- [x] foo") {
		t.Fatalf("checkbox not flipped, body=%q", body)
	}
}

func TestCompleteUnknownIDReturnsTyped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "- [ ] foo\n")
	p := New(WithFiles(path))

	err := p.Complete(context.Background(), "missing-id")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("want ErrTaskNotFound, got %v", err)
	}
}

func TestDeleteRemovesLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md",
		"# Notes\n\n- [ ] foo <!-- marunage:id=abc -->\n- [ ] bar <!-- marunage:id=def -->\n")
	p := New(WithFiles(path))

	if err := p.Delete(context.Background(), "abc"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)
	if strings.Contains(s, "foo") {
		t.Fatalf("foo line still present:\n%s", body)
	}
	if !strings.Contains(s, "bar") {
		t.Fatalf("bar line accidentally deleted:\n%s", body)
	}
	// Surrounding prose preserved.
	if !strings.Contains(s, "# Notes") {
		t.Fatalf("heading removed:\n%s", body)
	}
}

func TestSetupCreatesMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "todo.md")
	p := New(WithFiles(path))

	if err := p.Setup(context.Background()); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
	// Idempotent: a second Setup with existing content must not clobber.
	if err := os.WriteFile(path, []byte("- [ ] preserved\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := p.Setup(context.Background()); err != nil {
		t.Fatalf("Setup#2: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "preserved") {
		t.Fatalf("Setup clobbered existing content: %q", body)
	}
}

func TestConcurrentAddDoesNotCorrupt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "")
	p := New(WithFiles(path))

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			if _, err := p.Add(context.Background(), fmtN(i), ""); err != nil {
				t.Errorf("Add#%d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != n {
		t.Fatalf("want %d tasks, got %d (file content may be corrupt)", n, len(got))
	}
}

func fmtN(i int) string { return "task-" + strconv.Itoa(i) }

// TestAddRejectsTitleWithNewline guards against a title that would split
// the checklist line in two. A naively appended "- [ ] foo\nbar <!-- ... -->"
// produces one orphan "- [ ] foo" line and one stray "bar <!-- ... -->"
// line; the marker is unreachable, Complete / Delete cannot find the task
// any more, and the next List would mint a fresh id for the orphan. We
// surface the bad input as an explicit error so the caller knows the
// write was rejected before the file changed.
func TestAddRejectsTitleWithNewline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md", "")
	p := New(WithFiles(path))

	_, err := p.Add(context.Background(), "first line\nsecond line", "")
	if err == nil {
		t.Fatalf("Add accepted a multi-line title; want error")
	}
	// File must be untouched (we reject before atomic write).
	body, _ := os.ReadFile(path)
	if len(body) != 0 {
		t.Fatalf("file mutated despite rejected Add: %q", body)
	}
}

// TestListPreservesPartialMarkerFieldsWhenInjectingID locks in the contract
// that a hand-edited line carrying a partial marunage marker (e.g.
// "<!-- marunage:source=upstream -->" with no id=) keeps its existing
// source / extra metadata when List injects the missing id. Without this
// guarantee, the user's bookkeeping silently disappears the first time
// List runs over a file someone curated by hand.
func TestListPreservesPartialMarkerFieldsWhenInjectingID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md",
		"- [ ] foo <!-- marunage:source=upstream extra1=value1 -->\n")
	p := New(WithFiles(path))

	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ExternalID == "" {
		t.Fatalf("List returned %+v", got)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "source=upstream") {
		t.Errorf("source=upstream lost after marker injection:\n%s", body)
	}
	if !strings.Contains(s, "extra1=value1") {
		t.Errorf("extra1=value1 lost after marker injection:\n%s", body)
	}
	if !strings.Contains(s, "id="+got[0].ExternalID) {
		t.Errorf("id= not added:\n%s", body)
	}
}

// TestCompletePreservesCRLF guards against silent line-ending rewrites on
// Windows-authored files. A user editing todo.md on Windows expects mutations
// (Complete / Delete / List's marker injection) to keep the file's CRLF
// encoding. Anything else bloats the next git diff with an unrelated EOL
// flip and is exactly the kind of "silent rewrite" docs/requirement.md's
// Reversibility invariant tries to prevent.
func TestCompletePreservesCRLF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "todo.md",
		"# Notes\r\n\r\n- [ ] foo <!-- marunage:id=abc source=markdown -->\r\n- [ ] bar\r\n")
	p := New(WithFiles(path))

	if err := p.Complete(context.Background(), "abc"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "- [x] foo") {
		t.Fatalf("checkbox not flipped:\n%s", body)
	}
	// Every newline in the original was CRLF. Mutating the file must not
	// strip any of them.
	if strings.Count(s, "\r\n") < 4 {
		t.Fatalf("CRLF stripped after Complete (count=%d):\n%q", strings.Count(s, "\r\n"), s)
	}
}

func TestSinceReturnsModifiedFilesOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a := writeFile(t, dir, "a.md", "- [ ] alpha\n")
	b := writeFile(t, dir, "b.md", "- [ ] beta\n")
	cp := newMemCheckpointer()
	p := New(WithFiles(a, b), WithCheckpointer(cp))

	if _, err := p.Since(context.Background()); err != nil {
		t.Fatalf("Since#1: %v", err)
	}
	// Drain the marker-injection follow-up so both checkpoints are caught up.
	if _, err := p.Since(context.Background()); err != nil {
		t.Fatalf("Since#drain: %v", err)
	}

	// Bump mtime of b only (a future timestamp keeps us safe from
	// filesystem mtime resolution rounding).
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(b, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := p.Since(context.Background())
	if err != nil {
		t.Fatalf("Since#2: %v", err)
	}
	if len(got) != 1 || got[0].Title != "beta" {
		t.Fatalf("want only beta, got %+v", got)
	}
}
