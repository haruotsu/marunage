package markdown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
