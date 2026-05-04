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
	"unicode/utf8"

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
//
// Token storage contract for callers (the future Setup PR will live up
// to this, and any direct user of NewGoogleClient inherits it):
//
//   - The OAuth token MUST come from the secrets backend
//     (internal/secrets, e.g. OS Keychain / DPAPI / libsecret / pass).
//   - Plain-text persistence under ~/.marunage/ is forbidden by
//     requirement §9.1 (OpenClaw §11.1 lessons).
//   - The httpClient passed in here should already have a refresh
//     transport that writes the rotated refresh-token back through the
//     same secrets backend on rotation, so a long-running daemon never
//     drops back to the file system.
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
// package-level sentinels callers branch on:
//
//   - 401 / 403 -> ErrUnauthorized (AuthStatus -> AuthRevoked)
//   - 404       -> ErrUpstreamTaskMissing (Complete / Delete TOCTOU
//     translation -> ErrTaskNotFound)
//
// Other errors travel up unchanged.
//
// We deliberately truncate the upstream `Message` payload before letting
// it flow into the error chain. Google's API has been observed to
// reflect URL-encoded query parameters and a handful of identifying
// fields back into the message body; a verbatim wrap would let those
// segments leak into logs that the caller did not expect to redact.
// Truncation keeps the diagnostic value (response category) without
// the long tail. Defence-in-depth — `internal/logging.Redact` (PR-42b)
// is the primary line; this function is the "do not put it in the
// chain in the first place" complement.
func translateError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		msg := truncateMessage(apiErr.Message)
		switch apiErr.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: status=%d %s", ErrUnauthorized, apiErr.Code, msg)
		case http.StatusNotFound:
			return fmt.Errorf("%w: status=%d %s", ErrUpstreamTaskMissing, apiErr.Code, msg)
		}
		// Default branch: still return an error chain that exposes the
		// typed *googleapi.Error via errors.As (so callers can inspect
		// Code / Body / Header), but DO NOT let the verbose
		// Error.Error() rendering — which concatenates the full Body —
		// land in the wrapped error string. Otherwise a 5xx with a
		// reflected query/token in the response body would flow
		// straight into log sinks that have not yet had Redact applied.
		return fmt.Errorf("googletasks: upstream %d: %s [%w]", apiErr.Code, msg, scrubbedErr{inner: apiErr})
	}
	return err
}

// scrubbedErr is the wrapper translateError uses for unmapped
// googleapi error codes. It masks the verbose Error() rendering
// (which inlines the full Body) while keeping the typed value
// reachable via errors.As, so callers that explicitly want the raw
// payload can still recover it.
type scrubbedErr struct {
	inner *googleapi.Error
}

func (e scrubbedErr) Error() string {
	return fmt.Sprintf("googleapi error %d", e.inner.Code)
}

func (e scrubbedErr) Unwrap() error { return e.inner }

// truncateMessage keeps an upstream error string from leaking unbounded
// reflected content into the error chain. 120 bytes is enough to retain
// the upstream category ("Required field is missing", "Quota exceeded",
// ...) while bounding the worst-case payload size that a misconfigured
// log sink could capture. The caller is still free to read the full
// `*googleapi.Error` via `errors.As` if it wants to inspect the
// untruncated payload — the truncation only applies to what we put
// into the wrapped error string.
//
// We cut on a UTF-8 rune boundary, not a byte one: Google's API returns
// localised messages (Japanese, German, ...) where a byte-level cut
// would split a multi-byte sequence and the wrapped error string would
// no longer be valid UTF-8 — downstream log sinks would then either
// emit U+FFFD or refuse the line altogether.
func truncateMessage(s string) string {
	const limit = 120
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...(truncated)"
}
