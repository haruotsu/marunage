package googletasks

import (
	"context"
	"fmt"
	"sync"
)

// fakeClient is the test double the Plugin tests drive. It is tiny on
// purpose: only the operations Plugin actually uses are present, and every
// observable side effect (which list received an insert, which task got
// patched / deleted) is recorded so tests can assert against the call
// trace without resorting to mocks.
type fakeClient struct {
	mu sync.Mutex

	// lists is the in-memory model: list id -> list of tasks. The order
	// of the slices is the order returned by ListTaskLists / ListTasks
	// so tests can pin a deterministic shape.
	lists       []GTaskList
	tasks       map[string][]GTask
	nextTaskNum int

	// pingErr / listErr / addErr / patchErr / delErr let an individual
	// test inject upstream failures without inventing a new fake every
	// time. Set them in a table-driven test before invoking the Plugin
	// method. listErr fires from both ListTaskLists and ListTasks; if a
	// test ever needs to distinguish the two, split into separate fields
	// at that point.
	pingErr  error
	listErr  error
	addErr   error
	patchErr error
	delErr   error

	// trace records observable side effects for assertions. inserts /
	// patches / deletes carry the (listID, ...) tuple the Plugin
	// produced so tests can confirm the right list was addressed.
	inserts []traceInsert
	patches []tracePatch
	deletes []traceDelete
}

type traceInsert struct {
	ListID string
	Task   GTask
}

type tracePatch struct {
	ListID string
	TaskID string
	Patch  GTask
}

type traceDelete struct {
	ListID string
	TaskID string
}

// newFakeClient seeds a fake with one list ("@default") and no tasks. Tests
// that need additional lists call addList; tests that need pre-existing
// tasks call addTask.
func newFakeClient() *fakeClient {
	return &fakeClient{
		lists: []GTaskList{{ID: defaultTaskListAlias, Title: "My Tasks"}},
		tasks: map[string][]GTask{defaultTaskListAlias: nil},
	}
}

func (f *fakeClient) addList(id, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists = append(f.lists, GTaskList{ID: id, Title: title})
	if _, ok := f.tasks[id]; !ok {
		f.tasks[id] = nil
	}
}

func (f *fakeClient) addTask(listID string, t GTask) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[listID] = append(f.tasks[listID], t)
}

func (f *fakeClient) ListTaskLists(ctx context.Context) ([]GTaskList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]GTaskList, len(f.lists))
	copy(out, f.lists)
	return out, nil
}

func (f *fakeClient) ListTasks(ctx context.Context, tasklistID string) ([]GTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	src, ok := f.tasks[tasklistID]
	if !ok {
		return nil, fmt.Errorf("fake: unknown tasklist %q", tasklistID)
	}
	out := make([]GTask, len(src))
	copy(out, src)
	return out, nil
}

func (f *fakeClient) InsertTask(ctx context.Context, tasklistID string, task GTask) (GTask, error) {
	if err := ctx.Err(); err != nil {
		return GTask{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return GTask{}, f.addErr
	}
	if _, ok := f.tasks[tasklistID]; !ok {
		return GTask{}, fmt.Errorf("fake: unknown tasklist %q", tasklistID)
	}
	f.nextTaskNum++
	task.ID = fmt.Sprintf("task-%d", f.nextTaskNum)
	if task.Status == "" {
		task.Status = statusNeedsAction
	}
	f.tasks[tasklistID] = append(f.tasks[tasklistID], task)
	f.inserts = append(f.inserts, traceInsert{ListID: tasklistID, Task: task})
	return task, nil
}

func (f *fakeClient) PatchTask(ctx context.Context, tasklistID, taskID string, patch GTask) (GTask, error) {
	if err := ctx.Err(); err != nil {
		return GTask{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.patchErr != nil {
		return GTask{}, f.patchErr
	}
	tasks, ok := f.tasks[tasklistID]
	if !ok {
		return GTask{}, fmt.Errorf("fake: unknown tasklist %q", tasklistID)
	}
	for i, t := range tasks {
		if t.ID != taskID {
			continue
		}
		if patch.Title != "" {
			t.Title = patch.Title
		}
		if patch.Notes != "" {
			t.Notes = patch.Notes
		}
		if patch.Status != "" {
			t.Status = patch.Status
		}
		f.tasks[tasklistID][i] = t
		f.patches = append(f.patches, tracePatch{ListID: tasklistID, TaskID: taskID, Patch: patch})
		return t, nil
	}
	// Surface a 404-like error verbatim; the Plugin translates it into
	// ErrTaskNotFound when relevant. The fake intentionally does not
	// return ErrTaskNotFound to avoid baking the Plugin's translation
	// rules into the test double.
	return GTask{}, errFakeTaskMissing
}

func (f *fakeClient) DeleteTask(ctx context.Context, tasklistID, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		return f.delErr
	}
	tasks, ok := f.tasks[tasklistID]
	if !ok {
		return fmt.Errorf("fake: unknown tasklist %q", tasklistID)
	}
	for i, t := range tasks {
		if t.ID != taskID {
			continue
		}
		f.tasks[tasklistID] = append(tasks[:i], tasks[i+1:]...)
		f.deletes = append(f.deletes, traceDelete{ListID: tasklistID, TaskID: taskID})
		return nil
	}
	return errFakeTaskMissing
}

func (f *fakeClient) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

// errFakeTaskMissing is the "404" the fake returns when a Patch / Delete
// targets an unknown task id. It wraps the package-level
// ErrUpstreamTaskMissing so the Plugin's TOCTOU translation (Complete /
// Delete catching ErrUpstreamTaskMissing -> ErrTaskNotFound) fires
// uniformly whether the source is the fake or the real GoogleClient.
var errFakeTaskMissing = fmt.Errorf("%w: fake", ErrUpstreamTaskMissing)

// fakeMissingError is the helper tests inject into fake.patchErr /
// fake.delErr to simulate a TOCTOU race: the row was visible at
// findTaskList, then the upstream removed it before the patch /
// delete landed. Exposing it as a function (rather than the bare
// errFakeTaskMissing var) keeps the fake's internal sentinel out of
// every test that just wants to say "act like upstream returned 404".
func fakeMissingError() error { return errFakeTaskMissing }
