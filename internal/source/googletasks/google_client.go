// Package googletasks's google_client.go is the production-side glue
// between the Plugin's narrow Client interface (declared in client.go)
// and Google's generated tasks/v1 SDK. The plugin tests never compile
// against this file — they exercise Plugin against the in-memory fake
// in fake_client_test.go — so any change here cannot accidentally
// regress the unit-test coverage.
//
// Why a thin wrapper rather than reusing *tasks.Service directly: the
// generated SDK exposes the entire REST surface (oauth scopes, batched
// reads, paged iterators, ...) but the Plugin only needs six methods.
// The wrapper crystallises the contract so a future migration to a
// different transport (gRPC, mock HTTP for integration tests, ...) is a
// drop-in replacement at the seam, not a sprawling refactor inside the
// Plugin.
package googletasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	tasksapi "google.golang.org/api/tasks/v1"
)

// GoogleClient is the production Client backed by Google's generated
// tasks/v1 SDK. Exported so callers in cmd/ can construct one with the
// authenticated *http.Client of their choice and pass it to the Plugin
// via WithClient.
//
// Concurrency: *tasks.Service is documented as goroutine-safe, so the
// wrapper holds it directly and does no extra locking. The Plugin's
// own RWMutex protects only the upstream-Client swap (a future Setup
// path); concurrent List/Add against the same GoogleClient is safe.
type GoogleClient struct {
	svc *tasksapi.Service
}

// NewGoogleClient wraps an authenticated *http.Client (typically the
// output of `golang.org/x/oauth2.NewClient(ctx, source)`) into a
// Plugin-compatible Client. Returns an error if the SDK refuses to
// build a Service from the supplied transport — most commonly a nil
// httpClient or a transport that has no token source attached.
//
// We pass option.WithHTTPClient so the caller stays in charge of
// auth/refresh: this package does not import golang.org/x/oauth2 (the
// `go.mod` entry comes from the wrapper, not from here) and therefore
// cannot accidentally hard-code an OAuth flow that ignores the user's
// chosen secret backend.
func NewGoogleClient(ctx context.Context, httpClient *http.Client) (*GoogleClient, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("googletasks: NewGoogleClient requires a non-nil *http.Client")
	}
	svc, err := tasksapi.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("googletasks: tasks.NewService: %w", err)
	}
	return &GoogleClient{svc: svc}, nil
}

// ListTaskLists pulls every list visible to the configured account.
// Pagination is handled inside the loop because the upstream caps each
// page at 100 lists; for marunage, where most users have a single
// list, the second page is rarely fetched.
func (c *GoogleClient) ListTaskLists(ctx context.Context) ([]GTaskList, error) {
	var out []GTaskList
	call := c.svc.Tasklists.List().Context(ctx).MaxResults(100)
	if err := call.Pages(ctx, func(resp *tasksapi.TaskLists) error {
		for _, l := range resp.Items {
			out = append(out, GTaskList{ID: l.Id, Title: l.Title})
		}
		return nil
	}); err != nil {
		return nil, translateError(err)
	}
	return out, nil
}

// ListTasks reads every task in tasklistID. We pass ShowCompleted(true)
// and ShowHidden(true) so List in the Plugin can surface Done=true rows
// for the queue's reconciliation logic; without these the upstream
// silently filters completed-and-hidden items.
func (c *GoogleClient) ListTasks(ctx context.Context, tasklistID string) ([]GTask, error) {
	var out []GTask
	call := c.svc.Tasks.List(tasklistID).
		Context(ctx).
		ShowCompleted(true).
		ShowHidden(true).
		MaxResults(100)
	if err := call.Pages(ctx, func(resp *tasksapi.Tasks) error {
		for _, t := range resp.Items {
			out = append(out, GTask{
				ID:     t.Id,
				Title:  t.Title,
				Notes:  t.Notes,
				Status: t.Status,
			})
		}
		return nil
	}); err != nil {
		return nil, translateError(err)
	}
	return out, nil
}

// InsertTask creates a new task in tasklistID. The upstream-confirmed
// value (with its assigned id and any server-side defaults) is what the
// Plugin's Add then translates into source.Task — reusing the response
// rather than the request body avoids drift between "what we asked for"
// and "what Google actually stored".
func (c *GoogleClient) InsertTask(ctx context.Context, tasklistID string, task GTask) (GTask, error) {
	got, err := c.svc.Tasks.Insert(tasklistID, &tasksapi.Task{
		Title:  task.Title,
		Notes:  task.Notes,
		Status: task.Status,
	}).Context(ctx).Do()
	if err != nil {
		return GTask{}, translateError(err)
	}
	return GTask{
		ID:     got.Id,
		Title:  got.Title,
		Notes:  got.Notes,
		Status: got.Status,
	}, nil
}

// PatchTask updates only the non-empty fields of patch. We deliberately
// use Patch (not Update) so a Status-only flip from Complete does not
// blank the upstream Title / Notes; an Update call would PUT the full
// resource and zero out fields the patch does not name.
func (c *GoogleClient) PatchTask(ctx context.Context, tasklistID, taskID string, patch GTask) (GTask, error) {
	body := &tasksapi.Task{}
	if patch.Title != "" {
		body.Title = patch.Title
	}
	if patch.Notes != "" {
		body.Notes = patch.Notes
	}
	if patch.Status != "" {
		body.Status = patch.Status
	}
	got, err := c.svc.Tasks.Patch(tasklistID, taskID, body).Context(ctx).Do()
	if err != nil {
		return GTask{}, translateError(err)
	}
	return GTask{
		ID:     got.Id,
		Title:  got.Title,
		Notes:  got.Notes,
		Status: got.Status,
	}, nil
}

// DeleteTask drops the task from tasklistID. The upstream returns 204 on
// success; the SDK collapses that to a nil error which we forward.
func (c *GoogleClient) DeleteTask(ctx context.Context, tasklistID, taskID string) error {
	if err := c.svc.Tasks.Delete(tasklistID, taskID).Context(ctx).Do(); err != nil {
		return translateError(err)
	}
	return nil
}

// Ping is the cheap probe AuthStatus uses. We hit the smallest-possible
// list endpoint with MaxResults(1) so a healthy account costs a single
// short HTTP request rather than a full listing on every status check.
// We deliberately do NOT cache the result here: AuthStatus is called
// rarely (once per CLI invocation, occasionally inside a loop), and
// caching would silently turn a freshly-revoked token into a "still
// authenticated" answer for the cache TTL window.
func (c *GoogleClient) Ping(ctx context.Context) error {
	_, err := c.svc.Tasklists.List().Context(ctx).MaxResults(1).Do()
	if err != nil {
		return translateError(err)
	}
	return nil
}

// translateError lifts the SDK's typed *googleapi.Error into the
// package-level ErrUnauthorized sentinel for 401 / 403 responses, so
// AuthStatus can branch on errors.Is without duplicating Google's
// error-shape into the Plugin. Other errors travel up unchanged
// (wrapped only with package context) so the caller still sees the
// original payload for debugging.
func translateError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		if apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusForbidden {
			return fmt.Errorf("%w: %s", ErrUnauthorized, apiErr.Message)
		}
	}
	return err
}
