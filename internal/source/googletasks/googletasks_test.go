package googletasks

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestPluginName pins the plugin's canonical identifier. The registry,
// manifest, and Task.Source must all read "googletasks"; if any of them
// drifts the cross-source dispatcher would silently address the wrong
// plugin.
func TestPluginName(t *testing.T) {
	t.Parallel()

	p := New()
	if p.Name() != "googletasks" {
		t.Fatalf("Name() = %q, want googletasks", p.Name())
	}
}

// TestPluginImplementsContract is the compile-time witness that *Plugin
// satisfies the mandatory source.Plugin interface plus the three optional
// capabilities the manifest declares (Adder / Completer / Deleter). If a
// method goes missing, this test fails to compile.
func TestPluginImplementsContract(t *testing.T) {
	t.Parallel()

	var _ source.Plugin = (*Plugin)(nil)
	var _ source.Adder = (*Plugin)(nil)
	var _ source.Completer = (*Plugin)(nil)
	var _ source.Deleter = (*Plugin)(nil)
}

// TestAuthStatusWithoutClient is the "first run" state every other test
// implicitly relies on: a plugin built without a Client must report
// AuthNotConfigured so the CLI can prompt the user to run setup. Returning
// AuthAuthenticated here would later trick the discovery loop into calling
// List, which would then fail with ErrNotConfigured — a confusing UX.
func TestAuthStatusWithoutClient(t *testing.T) {
	t.Parallel()

	got, err := New().AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Fatalf("AuthStatus = %q, want %q", got, source.AuthNotConfigured)
	}
}

// TestAuthStatusReachable confirms that a Client whose Ping succeeds is
// reported as authenticated. AuthStatus is the documented "cheap probe"
// method, so it must call into the Client rather than e.g. always
// returning authenticated whenever a Client is configured (which would
// silently swallow a revoked token).
func TestAuthStatusReachable(t *testing.T) {
	t.Parallel()

	p := New(WithClient(newFakeClient()))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Fatalf("AuthStatus = %q, want %q", got, source.AuthAuthenticated)
	}
}

// TestAuthStatusUnauthorized translates an upstream ErrUnauthorized into
// AuthRevoked. The status enum has no "unauthorized" member; revoked is
// the documented mapping for "credential present but rejected", which is
// exactly how an OAuth scope removal looks from the client's viewpoint.
func TestAuthStatusUnauthorized(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	fc.pingErr = ErrUnauthorized
	p := New(WithClient(fc))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthRevoked {
		t.Fatalf("AuthStatus = %q, want %q", got, source.AuthRevoked)
	}
}

// TestAuthStatusUpstreamErrorPropagates checks that a non-auth upstream
// failure (network glitch, 500) bubbles up as a real error rather than
// being silently mapped to one of the AuthStatus enum values. Mapping it
// to "expired" or "revoked" would tell the user to re-run setup, which
// would not fix a transient network problem.
func TestAuthStatusUpstreamErrorPropagates(t *testing.T) {
	t.Parallel()

	boom := errors.New("upstream 500")
	fc := newFakeClient()
	fc.pingErr = boom
	p := New(WithClient(fc))
	if _, err := p.AuthStatus(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("AuthStatus err = %v, want wraps %v", err, boom)
	}
}

// TestListWithoutClient pins the boundary condition: methods that need
// the upstream must fail loudly instead of returning an empty list (which
// would look like "no tasks" — a quietly-wrong answer).
func TestListWithoutClient(t *testing.T) {
	t.Parallel()

	if _, err := New().List(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("List err = %v, want ErrNotConfigured", err)
	}
}

// TestListSurfacesUpstreamTasks is the meat of the read path: tasks
// returned by the upstream must arrive as source.Tasks with the documented
// field mapping (Source, ExternalID, Title, Body, Done, SourcePath).
func TestListSurfacesUpstreamTasks(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	fc.addTask(defaultTaskListAlias, GTask{ID: "t1", Title: "buy milk", Notes: "2L", Status: statusNeedsAction})
	fc.addTask(defaultTaskListAlias, GTask{ID: "t2", Title: "walk dog", Status: statusCompleted})
	p := New(WithClient(fc))

	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, %+v", len(got), got)
	}
	first := got[0]
	if first.Source != pluginName || first.ExternalID != "t1" || first.Title != "buy milk" {
		t.Errorf("first = %+v", first)
	}
	if first.Body != "2L" {
		t.Errorf("first.Body = %q, want %q", first.Body, "2L")
	}
	if first.Done {
		t.Errorf("first.Done = true, want false")
	}
	if first.SourcePath != "tasklists/"+defaultTaskListAlias {
		t.Errorf("first.SourcePath = %q", first.SourcePath)
	}
	if !got[1].Done {
		t.Errorf("second.Done = false, want true (status=%q)", statusCompleted)
	}
}

// TestListMergesMultipleTaskLists confirms List walks every list the
// account exposes, not just the default one. A user with separate "work"
// and "personal" lists must see both in one List call; otherwise the
// Discovery loop would silently miss half their tasks.
func TestListMergesMultipleTaskLists(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	fc.addList("work", "Work")
	fc.addTask(defaultTaskListAlias, GTask{ID: "p1", Title: "personal", Status: statusNeedsAction})
	fc.addTask("work", GTask{ID: "w1", Title: "ship pr", Status: statusNeedsAction})
	p := New(WithClient(fc))

	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, %+v", len(got), got)
	}
	seen := map[string]string{}
	for _, tk := range got {
		seen[tk.ExternalID] = tk.SourcePath
	}
	if seen["p1"] != "tasklists/"+defaultTaskListAlias {
		t.Errorf("p1 SourcePath = %q", seen["p1"])
	}
	if seen["w1"] != "tasklists/work" {
		t.Errorf("w1 SourcePath = %q", seen["w1"])
	}
}

// TestListPropagatesContextCancellation guards against a Plugin that
// happily plows through every list after the caller cancelled the
// context. Discovery's outer loop treats cancellation as "shut down
// now"; honouring it here keeps shutdown bounded.
func TestListPropagatesContextCancellation(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	p := New(WithClient(fc))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List err = %v, want context.Canceled", err)
	}
}

// TestListPropagatesUpstreamError fails closed: an upstream 500 must come
// back as an error, not as an empty slice. An empty slice would tell the
// queue "this source has no tasks", which would then mark previously-
// queued tasks as done by the reconciliation logic.
func TestListPropagatesUpstreamError(t *testing.T) {
	t.Parallel()

	boom := errors.New("upstream 500")
	fc := newFakeClient()
	fc.listErr = boom
	p := New(WithClient(fc))
	if _, err := p.List(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("List err = %v, want wraps %v", err, boom)
	}
}

// TestAddWithoutClient guards the Add boundary the same way List does.
func TestAddWithoutClient(t *testing.T) {
	t.Parallel()

	if _, err := New().Add(context.Background(), "x", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Add err = %v, want ErrNotConfigured", err)
	}
}

// TestAddRejectsEmptyTitle is the boundary check that keeps a malformed
// task off the upstream entirely. Google Tasks accepts "" as a title and
// renders the row blank, which is impossible for the user to triage later.
func TestAddRejectsEmptyTitle(t *testing.T) {
	t.Parallel()

	p := New(WithClient(newFakeClient()))
	if _, err := p.Add(context.Background(), "", ""); !errors.Is(err, ErrInvalidTitle) {
		t.Fatalf("Add err = %v, want ErrInvalidTitle", err)
	}
}

// TestAddInsertsIntoDefaultTaskList covers the common case: no override
// configured, so Add must land in @default. ExternalID comes back from
// the upstream-confirmed insert (Google assigns the id, not the caller).
func TestAddInsertsIntoDefaultTaskList(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	p := New(WithClient(fc))

	got, err := p.Add(context.Background(), "ship pr", "include changelog")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.Source != pluginName || got.Title != "ship pr" || got.ExternalID == "" {
		t.Fatalf("Add returned %+v", got)
	}
	if got.Body != "include changelog" {
		t.Errorf("Body = %q, want %q", got.Body, "include changelog")
	}
	if len(fc.inserts) != 1 || fc.inserts[0].ListID != defaultTaskListAlias {
		t.Fatalf("inserts trace = %+v", fc.inserts)
	}
	if fc.inserts[0].Task.Title != "ship pr" || fc.inserts[0].Task.Notes != "include changelog" {
		t.Errorf("inserted task = %+v", fc.inserts[0].Task)
	}
}

// TestAddRespectsDefaultTaskListOverride verifies WithDefaultTaskList
// pins the destination. A user who keeps their marunage tasks in a
// dedicated list must not have new tasks leak into the personal default.
func TestAddRespectsDefaultTaskListOverride(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	fc.addList("marunage", "Marunage")
	p := New(WithClient(fc), WithDefaultTaskList("marunage"))

	if _, err := p.Add(context.Background(), "x", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(fc.inserts) != 1 || fc.inserts[0].ListID != "marunage" {
		t.Fatalf("inserts trace = %+v", fc.inserts)
	}
}

// TestAddPropagatesUpstreamError is the symmetric guard to the List one:
// the queue must see a real failure rather than a fabricated "succeeded
// but empty" Task.
func TestAddPropagatesUpstreamError(t *testing.T) {
	t.Parallel()

	boom := errors.New("upstream 500")
	fc := newFakeClient()
	fc.addErr = boom
	p := New(WithClient(fc))
	if _, err := p.Add(context.Background(), "x", ""); !errors.Is(err, boom) {
		t.Fatalf("Add err = %v, want wraps %v", err, boom)
	}
}

// TestCompleteFlipsStatusInDefaultList is the mirror flow the brief calls
// out: marunage marks the task done, Plugin.Complete must turn that into
// a Google Tasks status="completed" patch.
func TestCompleteFlipsStatusInDefaultList(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	fc.addTask(defaultTaskListAlias, GTask{ID: "t1", Title: "x", Status: statusNeedsAction})
	p := New(WithClient(fc))

	if err := p.Complete(context.Background(), "t1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(fc.patches) != 1 {
		t.Fatalf("patches = %+v", fc.patches)
	}
	got := fc.patches[0]
	if got.ListID != defaultTaskListAlias || got.TaskID != "t1" || got.Patch.Status != statusCompleted {
		t.Errorf("patch = %+v", got)
	}
}

// TestCompleteFindsTaskInNonDefaultList covers the multi-list case: the
// id might live in a list the user created themselves. The Plugin must
// search across lists rather than blindly addressing @default.
func TestCompleteFindsTaskInNonDefaultList(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	fc.addList("work", "Work")
	fc.addTask("work", GTask{ID: "w1", Title: "x", Status: statusNeedsAction})
	p := New(WithClient(fc))

	if err := p.Complete(context.Background(), "w1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(fc.patches) != 1 || fc.patches[0].ListID != "work" {
		t.Fatalf("patches = %+v", fc.patches)
	}
}

// TestCompleteUnknownIDReturnsTypedError confirms the typed sentinel so
// callers can branch on errors.Is rather than parse strings.
func TestCompleteUnknownIDReturnsTypedError(t *testing.T) {
	t.Parallel()

	p := New(WithClient(newFakeClient()))
	if err := p.Complete(context.Background(), "absent"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Complete err = %v, want ErrTaskNotFound", err)
	}
}

// TestCompleteRejectsEmptyID guards the boundary: an empty externalID is
// always a programmer error, never an "absent on upstream" condition.
func TestCompleteRejectsEmptyID(t *testing.T) {
	t.Parallel()

	p := New(WithClient(newFakeClient()))
	if err := p.Complete(context.Background(), ""); !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("Complete err = %v, want ErrInvalidTaskID", err)
	}
}

// TestCompleteWithoutClient mirrors the Add / List boundary check.
func TestCompleteWithoutClient(t *testing.T) {
	t.Parallel()

	if err := New().Complete(context.Background(), "t1"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Complete err = %v, want ErrNotConfigured", err)
	}
}

// TestDeleteRemovesUpstream confirms Delete addresses the right list and
// actually drops the row. We assert via fake.tasks rather than just the
// trace so a future bug that traces the delete but skips the side effect
// would still go red here.
func TestDeleteRemovesUpstream(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	fc.addTask(defaultTaskListAlias, GTask{ID: "t1", Title: "x", Status: statusNeedsAction})
	p := New(WithClient(fc))

	if err := p.Delete(context.Background(), "t1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fc.deletes) != 1 || fc.deletes[0].ListID != defaultTaskListAlias || fc.deletes[0].TaskID != "t1" {
		t.Fatalf("deletes = %+v", fc.deletes)
	}
	if len(fc.tasks[defaultTaskListAlias]) != 0 {
		t.Fatalf("default list still has tasks: %+v", fc.tasks[defaultTaskListAlias])
	}
}

// TestDeleteFindsTaskInNonDefaultList symmetric multi-list test.
func TestDeleteFindsTaskInNonDefaultList(t *testing.T) {
	t.Parallel()

	fc := newFakeClient()
	fc.addList("work", "Work")
	fc.addTask("work", GTask{ID: "w1", Title: "x", Status: statusNeedsAction})
	p := New(WithClient(fc))

	if err := p.Delete(context.Background(), "w1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fc.deletes) != 1 || fc.deletes[0].ListID != "work" {
		t.Fatalf("deletes = %+v", fc.deletes)
	}
}

// TestDeleteUnknownIDReturnsTypedError matches Complete's contract.
func TestDeleteUnknownIDReturnsTypedError(t *testing.T) {
	t.Parallel()

	p := New(WithClient(newFakeClient()))
	if err := p.Delete(context.Background(), "absent"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Delete err = %v, want ErrTaskNotFound", err)
	}
}

// TestDeleteRejectsEmptyID matches Complete's contract.
func TestDeleteRejectsEmptyID(t *testing.T) {
	t.Parallel()

	p := New(WithClient(newFakeClient()))
	if err := p.Delete(context.Background(), ""); !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("Delete err = %v, want ErrInvalidTaskID", err)
	}
}

// TestDeleteWithoutClient matches the boundary contract.
func TestDeleteWithoutClient(t *testing.T) {
	t.Parallel()

	if err := New().Delete(context.Background(), "t1"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Delete err = %v, want ErrNotConfigured", err)
	}
}
