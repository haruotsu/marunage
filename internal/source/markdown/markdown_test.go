package markdown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	mu  sync.Mutex
	kv  map[string]string
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
