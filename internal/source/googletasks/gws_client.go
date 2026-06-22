package googletasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// GWSClient implements Client by shelling out to the `gws` binary's Google
// Tasks subcommands (`gws tasks tasklists|tasks ...`). It mirrors the gmail /
// calendar gws clients so the whole Google suite shares one auth path
// (`gws auth login`) and one transport.
type GWSClient struct {
	binary string
	runner Runner
}

// Runner executes the gws binary and returns its stdout. Injectable so tests
// never spawn a real process.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// GWSOption customises a GWSClient.
type GWSOption func(*GWSClient)

// WithBinary overrides the gws binary path (default "gws").
func WithBinary(path string) GWSOption {
	return func(c *GWSClient) { c.binary = path }
}

// WithRunner overrides the process runner (tests only).
func WithRunner(r Runner) GWSOption {
	return func(c *GWSClient) { c.runner = r }
}

// NewGWSClient builds a gws-backed Client with sensible defaults.
func NewGWSClient(opts ...GWSOption) *GWSClient {
	c := &GWSClient{binary: "gws", runner: defaultRunner}
	for _, o := range opts {
		o(c)
	}
	return c
}

// gwsAuthExitCode is the exit status gws uses for "credentials missing or
// invalid". We map it onto ErrUnauthorized so AuthStatus can report
// AuthRevoked without parsing stderr text.
const gwsAuthExitCode = 2

// defaultRunner executes gws via os/exec, mapping the auth exit code onto
// ErrUnauthorized and surfacing stderr on other failures.
func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ee.ExitCode() == gwsAuthExitCode {
				return nil, fmt.Errorf("%w: %s", ErrUnauthorized, strings.TrimSpace(string(ee.Stderr)))
			}
			if len(ee.Stderr) > 0 {
				return nil, fmt.Errorf("gws: %s", strings.TrimSpace(string(ee.Stderr)))
			}
		}
		return nil, err
	}
	return out, nil
}

// jsonBody trims any human-readable prefix gws prints before the JSON
// (e.g. "Using keyring backend: keyring") so json.Unmarshal sees clean input.
func jsonBody(out []byte) []byte {
	for i, b := range out {
		if b == '{' || b == '[' {
			return out[i:]
		}
	}
	return out
}

func (c *GWSClient) run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := c.runner(ctx, c.binary, args...)
	if err != nil {
		return nil, err
	}
	return jsonBody(out), nil
}

type gwsTaskList struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type gwsTaskListsResponse struct {
	Items []gwsTaskList `json:"items"`
}

type gwsTask struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Notes  string `json:"notes"`
	Status string `json:"status"`
}

type gwsTasksResponse struct {
	Items []gwsTask `json:"items"`
}

// ListTaskLists runs `gws tasks tasklists list`.
func (c *GWSClient) ListTaskLists(ctx context.Context) ([]GTaskList, error) {
	out, err := c.run(ctx, "tasks", "tasklists", "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("googletasks gws: tasklists.list: %w", err)
	}
	var resp gwsTaskListsResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("googletasks gws: decode tasklists.list: %w", err)
	}
	lists := make([]GTaskList, len(resp.Items))
	for i, l := range resp.Items {
		lists[i] = GTaskList(l)
	}
	return lists, nil
}

// ListTasks runs `gws tasks tasks list` for one tasklist, including completed
// rows so the Plugin can surface Done=true.
func (c *GWSClient) ListTasks(ctx context.Context, tasklistID string) ([]GTask, error) {
	params, err := json.Marshal(map[string]any{
		"tasklist":      tasklistID,
		"showCompleted": true,
		"showHidden":    true,
		"maxResults":    100,
	})
	if err != nil {
		return nil, fmt.Errorf("googletasks gws: encode tasks.list params: %w", err)
	}
	out, err := c.run(ctx, "tasks", "tasks", "list", "--params", string(params), "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("googletasks gws: tasks.list: %w", err)
	}
	var resp gwsTasksResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("googletasks gws: decode tasks.list: %w", err)
	}
	tasks := make([]GTask, len(resp.Items))
	for i, t := range resp.Items {
		tasks[i] = GTask(t)
	}
	return tasks, nil
}

// InsertTask runs `gws tasks tasks insert`.
func (c *GWSClient) InsertTask(ctx context.Context, tasklistID string, task GTask) (GTask, error) {
	params, err := json.Marshal(map[string]any{"tasklist": tasklistID})
	if err != nil {
		return GTask{}, fmt.Errorf("googletasks gws: encode insert params: %w", err)
	}
	body, err := json.Marshal(map[string]any{"title": task.Title, "notes": task.Notes})
	if err != nil {
		return GTask{}, fmt.Errorf("googletasks gws: encode insert body: %w", err)
	}
	out, err := c.run(ctx, "tasks", "tasks", "insert", "--params", string(params), "--json", string(body), "--format", "json")
	if err != nil {
		return GTask{}, fmt.Errorf("googletasks gws: tasks.insert: %w", err)
	}
	return decodeTask(out)
}

// PatchTask runs `gws tasks tasks patch`, sending only the fields present in
// patch (Status flip for Complete, Title/Notes edits).
func (c *GWSClient) PatchTask(ctx context.Context, tasklistID, taskID string, patch GTask) (GTask, error) {
	params, err := json.Marshal(map[string]any{"tasklist": tasklistID, "task": taskID})
	if err != nil {
		return GTask{}, fmt.Errorf("googletasks gws: encode patch params: %w", err)
	}
	fields := map[string]any{}
	if patch.Status != "" {
		fields["status"] = patch.Status
	}
	if patch.Title != "" {
		fields["title"] = patch.Title
	}
	if patch.Notes != "" {
		fields["notes"] = patch.Notes
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return GTask{}, fmt.Errorf("googletasks gws: encode patch body: %w", err)
	}
	out, err := c.run(ctx, "tasks", "tasks", "patch", "--params", string(params), "--json", string(body), "--format", "json")
	if err != nil {
		return GTask{}, fmt.Errorf("googletasks gws: tasks.patch: %w", err)
	}
	return decodeTask(out)
}

// DeleteTask runs `gws tasks tasks delete`.
func (c *GWSClient) DeleteTask(ctx context.Context, tasklistID, taskID string) error {
	params, err := json.Marshal(map[string]any{"tasklist": tasklistID, "task": taskID})
	if err != nil {
		return fmt.Errorf("googletasks gws: encode delete params: %w", err)
	}
	if _, err := c.runner(ctx, c.binary, "tasks", "tasks", "delete", "--params", string(params)); err != nil {
		return fmt.Errorf("googletasks gws: tasks.delete: %w", err)
	}
	return nil
}

// Ping runs the cheapest authenticated call (tasklists.list) to distinguish a
// live credential from a revoked one.
func (c *GWSClient) Ping(ctx context.Context) error {
	if _, err := c.run(ctx, "tasks", "tasklists", "list", "--format", "json"); err != nil {
		return fmt.Errorf("googletasks gws: ping: %w", err)
	}
	return nil
}

func decodeTask(out []byte) (GTask, error) {
	var t gwsTask
	if err := json.Unmarshal(out, &t); err != nil {
		return GTask{}, fmt.Errorf("googletasks gws: decode task: %w", err)
	}
	return GTask(t), nil
}
