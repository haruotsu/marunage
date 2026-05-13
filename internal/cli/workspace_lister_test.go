package cli

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"

	"github.com/haruotsu/marunage/internal/workspace/cmux"
)

// fakeCmuxRunner is the cmux.Runner stub used by the cmuxWorkspaceLister
// suite so the tests never spawn a real cmux. The shape mirrors the
// fakeRunner in internal/workspace/cmux/cmux_test.go so a future merge of these
// helpers stays cheap.
type fakeCmuxRunner struct {
	stdout []byte
	stderr []byte
	err    error

	gotName string
	gotArgs []string
}

func (f *fakeCmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.gotName = name
	f.gotArgs = append([]string(nil), args...)
	return f.stdout, f.stderr, f.err
}

// Happy path: harvest every workspace token from a multi-line cmux
// dashboard output. The fixture mirrors the real `cmux list-workspaces`
// banner format ("* workspace:N  <title>  [selected]") so a regression
// in the parser surfaces here rather than silently returning the wrong
// orphan set.
func TestCmuxWorkspaceLister_ParsesMultilineOutput(t *testing.T) {
	r := &fakeCmuxRunner{stdout: []byte(
		"  workspace:1  feature/x\n" +
			"* workspace:2  feature/y  [selected]\n" +
			"  workspace:42 hotfix\n",
	)}
	l := cmuxWorkspaceLister{runner: r}

	ids, err := l.ListWorkspaceIDs(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaceIDs: %v", err)
	}
	want := []string{"workspace:1", "workspace:2", "workspace:42"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v; want %v", ids, want)
	}
	// Pin the subcommand so a future "cmux ls" rename surfaces as a
	// failing test rather than a runtime error against a missing
	// subcommand.
	if r.gotName != "cmux" || len(r.gotArgs) != 1 || r.gotArgs[0] != "list-workspaces" {
		t.Errorf("Run called with name=%q args=%v; want cmux list-workspaces",
			r.gotName, r.gotArgs)
	}
}

// Empty stdout (cmux running but no workspaces) returns a non-nil empty
// slice so callers can rely on len() without a nil check, and so a
// `range` over the result is uniform with the populated case.
func TestCmuxWorkspaceLister_EmptyOutputReturnsEmptySlice(t *testing.T) {
	r := &fakeCmuxRunner{stdout: []byte("")}
	l := cmuxWorkspaceLister{runner: r}

	ids, err := l.ListWorkspaceIDs(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaceIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %v; want empty", ids)
	}
}

// A task title containing the literal substring "workspace:NN" (e.g. a
// commit message about workspaces) must NOT be picked up as an active
// workspace id. Without a guard the orphan diff would treat the
// stale-titled row as alive and keep its dead ws reference. Pinning the
// guard here is cheap insurance against a regex regression.
func TestCmuxWorkspaceLister_IgnoresWorkspaceSubstringInTitles(t *testing.T) {
	r := &fakeCmuxRunner{stdout: []byte(
		"  workspace:1  refactor workspace:99 helper\n" +
			"  workspace:2  done\n",
	)}
	l := cmuxWorkspaceLister{runner: r}

	ids, err := l.ListWorkspaceIDs(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaceIDs: %v", err)
	}
	want := []string{"workspace:1", "workspace:2"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v; want %v (workspace:99 is in a title, not the leading token)", ids, want)
	}
}

// Missing cmux binary surfaces as cmux.ErrCmuxNotFound so the rest of
// the codebase (PR-32 doctor, task_clean.go) can pattern-match via
// errors.Is rather than substring-checking the wrapped diagnostic.
// Mirrors the contract NewWorkspace honours in internal/workspace/cmux/cmux.go.
func TestCmuxWorkspaceLister_BinaryNotFoundMapsToErrCmuxNotFound(t *testing.T) {
	r := &fakeCmuxRunner{err: &exec.Error{Name: "cmux", Err: exec.ErrNotFound}}
	l := cmuxWorkspaceLister{runner: r}

	_, err := l.ListWorkspaceIDs(context.Background())
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if !errors.Is(err, cmux.ErrCmuxNotFound) {
		t.Errorf("err = %v; want errors.Is(err, cmux.ErrCmuxNotFound)", err)
	}
}

// Non-binary-not-found runner errors propagate with the stderr bytes
// included in the diagnostic so the operator sees cmux's reason, not
// just a Go-level "exit status 1".
func TestCmuxWorkspaceLister_OtherRunnerErrorsPropagate(t *testing.T) {
	r := &fakeCmuxRunner{
		err:    errors.New("boom"),
		stderr: []byte("cmux: subcommand crashed\n"),
	}
	l := cmuxWorkspaceLister{runner: r}

	_, err := l.ListWorkspaceIDs(context.Background())
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if errors.Is(err, cmux.ErrCmuxNotFound) {
		t.Errorf("err = %v; should NOT match ErrCmuxNotFound (only exec.ErrNotFound does)", err)
	}
	got := err.Error()
	for _, want := range []string{"cmux", "boom", "subcommand crashed"} {
		if !contains([]string{got}, want) && !containsSubstring(got, want) {
			t.Errorf("err message %q missing %q", got, want)
		}
	}
}

// containsSubstring is a tiny local helper so this file does not need
// to import strings just for one Contains call.
func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
