// Package googletasks's client.go declares the narrow seam between the
// Plugin and the upstream Google Tasks API. Only the methods the plugin
// actually invokes are part of the interface, so a fake implementation
// (used in unit tests) does not have to mock the entire google.golang.org/
// api/tasks/v1 surface.
//
// Why our own GTask / GTaskList structs rather than reusing the API types:
// the upstream types come from a generated package that drags in a fairly
// large dependency tree. Keeping the data shape internal lets the unit
// tests exercise the Plugin without compiling that tree, and lets future
// PRs swap the transport (REST vs. gRPC, mocked HTTP, ...) without
// touching the Plugin.
package googletasks

import "context"

// GTaskList is the minimal view of one Google Tasks list the Plugin needs.
// Only ID is required for routing operations; Title is captured so a future
// CLI listing can show user-friendly names without re-fetching.
type GTaskList struct {
	ID    string
	Title string
}

// GTask is the minimal view of one upstream task. Status uses the same
// string values Google Tasks emits ("needsAction" / "completed") so the
// real client can pass them straight through and the fake can assert on
// the exact wire value without a translation table.
type GTask struct {
	ID     string
	Title  string
	Notes  string
	Status string // "needsAction" | "completed"
}

// Client is the abstraction every upstream backend implements. The methods
// are scoped to operations the Plugin actually performs:
//
//   - ListTaskLists: enumerate every list the account owns (used by List
//     and by Complete/Delete to locate a task by id without forcing the
//     caller to remember which list it came from).
//   - ListTasks: enumerate items in one list. Returns both completed and
//     pending tasks so List can surface Done=true rows.
//   - InsertTask: create a new task in a named list. Used by Add.
//   - PatchTask: update mutable fields (Status flip for Complete, Title
//     edits for a future PR). Returns the upstream-confirmed value.
//   - DeleteTask: remove a task by (list, task) id pair. Used by Delete.
//   - Ping: cheap health check used by AuthStatus to distinguish
//     authenticated from revoked credentials without doing a full List.
//
// Implementations MUST return ErrUnauthorized verbatim (or wrapped via
// errors.Is) for 401/403 responses; the Plugin translates that into
// source.AuthRevoked. Other errors travel up unchanged.
type Client interface {
	ListTaskLists(ctx context.Context) ([]GTaskList, error)
	ListTasks(ctx context.Context, tasklistID string) ([]GTask, error)
	InsertTask(ctx context.Context, tasklistID string, task GTask) (GTask, error)
	PatchTask(ctx context.Context, tasklistID, taskID string, patch GTask) (GTask, error)
	DeleteTask(ctx context.Context, tasklistID, taskID string) error
	Ping(ctx context.Context) error
}

// statusCompleted / statusNeedsAction are the wire values Google Tasks
// uses for Task.Status. Centralised as consts so List/Add/Complete share
// one source of truth rather than three string literals at risk of
// typos.
const (
	statusCompleted   = "completed"
	statusNeedsAction = "needsAction"
)

// defaultTaskListAlias is the special tasklist id Google exposes for
// "the user's primary tasklist". Plugin.Add uses it when no explicit
// override has been wired via WithDefaultTaskList.
const defaultTaskListAlias = "@default"
