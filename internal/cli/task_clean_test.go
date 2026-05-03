package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// fakeWorkspaceLister is the test seam for PR-22 `marunage clean`. Tests
// install one via withWorkspaceListerFactory so the SUT never calls a real
// cmux. The default productionWorkspaceListerFactory is exercised in a
// dedicated test rather than from every clean test.
type fakeWorkspaceLister struct {
	ids []string
	err error
}

func (f *fakeWorkspaceLister) ListWorkspaceIDs(_ context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]string(nil), f.ids...), nil
}

// installFakeWorkspaceLister registers a fresh fake as the active
// workspaceLister factory, mirroring installFakeRepo / installFakeMirror.
func installFakeWorkspaceLister(t *testing.T, alive ...string) *fakeWorkspaceLister {
	t.Helper()
	lister := &fakeWorkspaceLister{ids: alive}
	withWorkspaceListerFactory(t, func(_ context.Context, _ string) (workspaceLister, error) {
		return lister, nil
	})
	return lister
}

// 11. No flags = dry-run = no DB mutations. The orphan reference must
// stay on the row so a subsequent --apply pass can still clean it.
func TestTaskClean_DryRunIsDefault(t *testing.T) {
	repo := installFakeRepo(t)
	installFakeWorkspaceLister(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusFailed, WS: "workspace:9"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("clean exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[1].WS; got != "workspace:9" {
		t.Errorf("dry-run mutated WS: got %q; want %q", got, "workspace:9")
	}
}

// 12. Dry-run reports each orphan ws so the operator can inspect before
// running --apply. Pinning both the id and the ws marker keeps a future
// "compact the report" rewrite from accidentally hiding one of them.
func TestTaskClean_DryRunReportsOrphanWorkspaceReferences(t *testing.T) {
	repo := installFakeRepo(t)
	installFakeWorkspaceLister(t)
	repo.rows[7] = store.Task{ID: 7, Source: "manual", Title: "x", Status: store.StatusFailed, WS: "workspace:99"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("clean exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "7") {
		t.Errorf("output should mention task id 7; got %q", out)
	}
	if !strings.Contains(out, "workspace:99") {
		t.Errorf("output should mention orphan ws; got %q", out)
	}
}

// 13. Tasks without a ws are not orphan candidates and must not appear
// in the report.
func TestTaskClean_DryRunIgnoresTasksWithoutWS(t *testing.T) {
	repo := installFakeRepo(t)
	installFakeWorkspaceLister(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "no-ws", Status: store.StatusPending}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("clean exit=%d; stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "no-ws") {
		t.Errorf("task without WS should not appear; got %q", stdout.String())
	}
}

// 14. ws references that DO exist in cmux are alive — not orphan — and
// must not be reported.
func TestTaskClean_DryRunIgnoresAliveWorkspaces(t *testing.T) {
	repo := installFakeRepo(t)
	installFakeWorkspaceLister(t, "workspace:1", "workspace:2")
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "alive", Status: store.StatusRunning, WS: "workspace:1"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("clean exit=%d; stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "alive") {
		t.Errorf("alive task should not appear; got %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "workspace:1") {
		t.Errorf("alive ws should not appear; got %q", stdout.String())
	}
}

// 15. --apply clears the orphan ws reference via SetWorkspace(id, "").
// The local row's WS becomes empty so the next clean run finds nothing.
func TestTaskClean_ApplyClearsOrphanWorkspaceReferences(t *testing.T) {
	repo := installFakeRepo(t)
	installFakeWorkspaceLister(t)
	repo.rows[7] = store.Task{ID: 7, Source: "manual", Title: "orphan", Status: store.StatusFailed, WS: "workspace:99"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean", "--apply"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("clean --apply exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[7].WS; got != "" {
		t.Errorf("--apply did not clear WS: got %q; want empty", got)
	}
}

// 16. --apply must not touch tasks whose ws still exists in cmux.
func TestTaskClean_ApplyDoesNotTouchAliveWorkspaces(t *testing.T) {
	repo := installFakeRepo(t)
	installFakeWorkspaceLister(t, "workspace:1")
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "alive", Status: store.StatusRunning, WS: "workspace:1"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean", "--apply"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("clean --apply exit=%d; stderr=%q", code, stderr.String())
	}
	if got := repo.rows[1].WS; got != "workspace:1" {
		t.Errorf("--apply mutated alive WS: got %q; want %q", got, "workspace:1")
	}
}

// 17. --apply prints a count summary so a script can grep the result
// without parsing per-row lines.
func TestTaskClean_ApplyReportsClearedCount(t *testing.T) {
	repo := installFakeRepo(t)
	installFakeWorkspaceLister(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "a", Status: store.StatusFailed, WS: "workspace:91"}
	repo.rows[2] = store.Task{ID: 2, Source: "manual", Title: "b", Status: store.StatusFailed, WS: "workspace:92"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean", "--apply"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("clean --apply exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "2") {
		t.Errorf("output should mention cleared count 2; got %q", stdout.String())
	}
}

// 18. Repo open errors propagate.
func TestTaskClean_PropagatesRepoErrors(t *testing.T) {
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})
	installFakeWorkspaceLister(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// 19. cmux lister errors propagate. Pinning this keeps a future "swallow
// lister failures and treat all rows as alive" shortcut from silently
// regressing the orphan detection.
func TestTaskClean_PropagatesWorkspaceListerErrors(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "x", Status: store.StatusFailed, WS: "workspace:1"}
	withWorkspaceListerFactory(t, func(_ context.Context, _ string) (workspaceLister, error) {
		return &fakeWorkspaceLister{err: errors.New("cmux down")}, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// 20. --json emits a structured report so CI / scripts can parse without
// touching the human prose.
func TestTaskClean_JSONFlagOutputsStructuredReport(t *testing.T) {
	repo := installFakeRepo(t)
	installFakeWorkspaceLister(t)
	repo.rows[3] = store.Task{ID: 3, Source: "manual", Title: "x", Status: store.StatusFailed, WS: "workspace:8"}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"clean", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("clean --json exit=%d; stderr=%q", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if _, ok := got["orphans"]; !ok {
		t.Errorf("JSON should contain 'orphans' key; got %+v", got)
	}
	if _, ok := got["applied"]; !ok {
		t.Errorf("JSON should contain 'applied' key; got %+v", got)
	}
}

// productionWorkspaceListerFactory must hand back something non-nil when
// no test hook is installed, mirroring the productionMirrorFactory
// contract. The exact type / behaviour is opaque to this test — it pins
// only the "no panic on cold call" invariant.
func TestWorkspaceLister_ProductionFactoryReturnsNonNil(t *testing.T) {
	l, err := productionWorkspaceListerFactory(context.Background(), "")
	if err != nil {
		t.Fatalf("productionWorkspaceListerFactory: %v", err)
	}
	if l == nil {
		t.Fatal("productionWorkspaceListerFactory returned nil")
	}
}

// withWorkspaceListerFactory's hook must be respected by
// activeWorkspaceListerFactory, and the factory must restore the
// previous hook after the test (mirrors withMirrorFactory).
func TestWorkspaceLister_TestHookIsRespected(t *testing.T) {
	want := &fakeWorkspaceLister{ids: []string{"workspace:1"}}
	withWorkspaceListerFactory(t, func(_ context.Context, _ string) (workspaceLister, error) {
		return want, nil
	})
	got, err := activeWorkspaceListerFactory()(context.Background(), "")
	if err != nil {
		t.Fatalf("activeWorkspaceListerFactory: %v", err)
	}
	if got != want {
		t.Errorf("activeWorkspaceListerFactory returned %v; want injected fake", got)
	}
}
