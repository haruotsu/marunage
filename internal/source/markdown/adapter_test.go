package markdown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// seed is the same helper writeFile uses elsewhere — local copy here so the
// adapter tests do not depend on test files in the parent package.
func seed(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return path
}

// TestAdapterImplementsPluginInterface is the compile-time witness that the
// adapter satisfies the mandatory contract. The var declaration is the
// assertion; if a method goes missing this file stops compiling.
func TestAdapterImplementsPluginInterface(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	var _ source.Plugin = a
	if a.Name() != "markdown" {
		t.Fatalf("Name() = %q, want markdown", a.Name())
	}
}

// TestAdapterImplementsOptionalCapabilities asserts the adapter opts into
// every optional sub-interface. The markdown plugin's Go API supports
// since/add/complete/delete, so the manifest will declare them; the registry
// validator checks both directions.
func TestAdapterImplementsOptionalCapabilities(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	if _, ok := any(a).(source.Sincer); !ok {
		t.Errorf("adapter must implement source.Sincer")
	}
	if _, ok := any(a).(source.Adder); !ok {
		t.Errorf("adapter must implement source.Adder")
	}
	if _, ok := any(a).(source.Completer); !ok {
		t.Errorf("adapter must implement source.Completer")
	}
	if _, ok := any(a).(source.Deleter); !ok {
		t.Errorf("adapter must implement source.Deleter")
	}
}

// TestAdapterListEquivalentToInner ensures the conversion from markdown.Task
// to source.Task is lossless for the fields that matter (title / done /
// external id / source / source path). Without this we could ship an
// adapter that quietly drops, say, SourcePath, leaving downstream materialise
// code with no idea where the task came from.
func TestAdapterListEquivalentToInner(t *testing.T) {
	t.Parallel()

	path := seed(t, "todo.md", "- [ ] foo\n- [x] bar\n")
	a := NewAdapter(New(WithFiles(path)))

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, %+v", len(got), got)
	}
	for i, tk := range got {
		if tk.Source != "markdown" {
			t.Errorf("task[%d].Source = %q", i, tk.Source)
		}
		if tk.SourcePath != path {
			t.Errorf("task[%d].SourcePath = %q, want %q", i, tk.SourcePath, path)
		}
		if tk.ExternalID == "" {
			t.Errorf("task[%d].ExternalID empty", i)
		}
	}
	if got[0].Title != "foo" || got[0].Done {
		t.Errorf("task[0] = %+v", got[0])
	}
	if got[1].Title != "bar" || !got[1].Done {
		t.Errorf("task[1] = %+v", got[1])
	}
}

// TestAdapterAuthStatusIsAuthenticated nails down the contract from the
// brief: the markdown source has no remote credential, so AuthStatus must
// always be authenticated. A future expired/revoked state would imply the
// plugin somehow lost access to local files, which is not a thing.
func TestAdapterAuthStatusIsAuthenticated(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	got, err := a.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("AuthStatus = %q, want %q", got, source.AuthAuthenticated)
	}
}

// TestAdapterSetupForwardsToInner asserts the adapter's Setup calls into
// markdown.Plugin.Setup so a CLI-driven `marunage discover --setup` (a
// future PR) actually creates the file.
func TestAdapterSetupForwardsToInner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "todo.md")
	a := NewAdapter(New(WithFiles(path)))

	if err := a.Setup(context.Background(), source.SetupOptions{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Setup did not create file: %v", err)
	}
}

// TestAdapterAddCompleteDeleteRoundTrip drives the bidirectional flow end-
// to-end through the adapter so we know the optional capabilities are wired
// up to the inner Plugin and the source.Task return value carries the data
// callers will actually use.
func TestAdapterAddCompleteDeleteRoundTrip(t *testing.T) {
	t.Parallel()

	path := seed(t, "todo.md", "")
	a := NewAdapter(New(WithFiles(path)))
	ctx := context.Background()

	added, err := a.Add(ctx, "first", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added.ExternalID == "" || added.Title != "first" || added.Source != "markdown" {
		t.Fatalf("Add returned %+v", added)
	}

	if err := a.Complete(ctx, added.ExternalID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "- [x] first") {
		t.Fatalf("Complete did not flip checkbox:\n%s", body)
	}

	if err := a.Delete(ctx, added.ExternalID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	body, _ = os.ReadFile(path)
	if strings.Contains(string(body), "first") {
		t.Fatalf("Delete left line behind:\n%s", body)
	}

	if err := a.Complete(ctx, "no-such-id"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("Complete unknown id: want ErrTaskNotFound, got %v", err)
	}
}

// TestAdapterSinceUsesCheckpointArg is the mechanical translation of the
// generic Since(checkpoint string) signature into markdown's
// Checkpointer-driven model. PR-70 wires KVStateRepo behind Checkpointer at
// the call site; the adapter itself has no opinion about the argument
// (markdown.Plugin.Since reads its own checkpoint key per file). What we
// assert here is that calling adapter.Since does not error and produces a
// list shaped like List would.
func TestAdapterSinceUsesCheckpointArg(t *testing.T) {
	t.Parallel()

	path := seed(t, "todo.md", "- [ ] alpha\n")
	a := NewAdapter(New(WithFiles(path)))
	got, err := a.Since(context.Background(), "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 1 || got[0].Title != "alpha" {
		t.Fatalf("Since = %+v", got)
	}
}
