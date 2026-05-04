// Package googletasks implements the Google Tasks Discovery source plugin
// promised in docs/pr_split_plan.md as PR-84.
//
// The plugin owns one Google account's task lists and exposes them through
// the cross-source contract defined in internal/source: List / Setup /
// AuthStatus mandatory, Add / Complete / Delete optional. Sync is
// bidirectional — marunage tasks added through Add land in the user's
// default tasklist, and a marunage-side "done" mirrors over to a Google
// Tasks "completed" status via Complete.
//
// Why a Client interface instead of holding *tasks.Service directly: the
// upstream API surface is large and only a handful of operations matter
// for Discovery (list / insert / patch / delete). Hiding the upstream
// behind a small interface keeps the plugin testable with a fake (no
// network, no OAuth flow) and lets a future PR swap the transport
// without touching plugin logic.
package googletasks

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/haruotsu/marunage/internal/source"
)

// pluginName is the canonical Source value emitted on every Task and the
// name under which the plugin registers. Centralised as a const so the
// manifest, registry key, and Task.Source cannot drift.
const pluginName = "googletasks"

// Typed sentinel errors. Callers branch on errors.Is rather than parsing
// strings; the CLI binding maps these to documented exit codes.
var (
	// ErrNotConfigured is returned by every operation that needs the
	// upstream client when no Client has been supplied. Phase 1 surfaces
	// this as "run `marunage setup --source googletasks`" in the CLI.
	ErrNotConfigured = errors.New("googletasks: source not configured")

	// ErrInvalidTitle is returned by Add when the supplied title is
	// empty. Google Tasks accepts whitespace-only titles, but they
	// produce an unidentifiable item in the user's list, so we reject
	// at the boundary instead of letting that confusion propagate
	// upstream.
	ErrInvalidTitle = errors.New("googletasks: invalid title")

	// ErrInvalidTaskID is returned by Complete / Delete when the
	// externalID argument is empty. Distinct from ErrTaskNotFound so a
	// caller passing "" sees a programmer-error message instead of an
	// "absent in upstream" message.
	ErrInvalidTaskID = errors.New("googletasks: invalid task id")

	// ErrTaskNotFound is returned by Complete / Delete when the
	// externalID does not match any task in any tasklist visible to the
	// configured account, OR when a TOCTOU race deletes the task between
	// findTaskList and the upstream Patch / Delete call.
	ErrTaskNotFound = errors.New("googletasks: task not found")

	// ErrAmbiguousTaskID is returned by Complete / Delete when the same
	// task id is observed in more than one tasklist. Google's API does
	// not document task ids as globally unique across lists, so we
	// refuse to silently pick the first hit and risk completing or
	// deleting the wrong row.
	ErrAmbiguousTaskID = errors.New("googletasks: ambiguous task id (present in multiple lists)")

	// ErrUnauthorized is returned by Client implementations to signal
	// the upstream rejected the credential (401 / 403). The plugin
	// translates this into source.AuthRevoked at the AuthStatus level.
	ErrUnauthorized = errors.New("googletasks: unauthorized")

	// ErrUpstreamTaskMissing is the sentinel a Client implementation
	// returns from PatchTask / DeleteTask when the upstream answers 404
	// for the (tasklist, task) pair. Plugin's Complete / Delete catch
	// it and translate to ErrTaskNotFound, so callers branch on a
	// stable, package-level type rather than an SDK-specific shape.
	ErrUpstreamTaskMissing = errors.New("googletasks: upstream task missing")
)

// Plugin is the Google Tasks source. Construct one with New and reuse it;
// the struct is concurrency-safe because all upstream mutations go through
// the underlying Client (which the real implementation backs with a
// goroutine-safe *tasks.Service).
type Plugin struct {
	upstream Client

	// defaultListID is the tasklist Add writes into. Empty means
	// "@default", which is the special Google Tasks alias for the
	// account's primary list. Configurable via WithDefaultTaskList so a
	// user with a marunage-specific list can pin it without renaming.
	defaultListID string

	mu sync.RWMutex
}

// Option mutates Plugin construction. Mirrors the functional-option style
// used in internal/source/markdown and internal/store so callers see a
// consistent shape across the codebase.
type Option func(*Plugin)

// WithClient injects the upstream Client. Tests pass a fake; production
// callers pass a real *tasks.Service-backed Client constructed from an
// authenticated OAuth token.
func WithClient(c Client) Option {
	return func(p *Plugin) { p.upstream = c }
}

// WithDefaultTaskList overrides the tasklist id Add writes into. Empty or
// unset uses Google Tasks' "@default" alias.
func WithDefaultTaskList(id string) Option {
	return func(p *Plugin) { p.defaultListID = id }
}

// New constructs a Plugin with the given options. A Plugin built with no
// Client is still callable: every operation returns ErrNotConfigured and
// AuthStatus reports AuthNotConfigured, which is the documented "first run,
// pre-setup" state.
func New(opts ...Option) *Plugin {
	p := &Plugin{}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name reports the canonical plugin identifier.
func (p *Plugin) Name() string { return pluginName }

// client returns the configured upstream Client under read lock so a
// concurrent setup-driven swap (a future PR-71 Setup will refresh the
// Client when re-auth completes) cannot tear with an in-flight List.
func (p *Plugin) client() Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.upstream
}

// targetList returns the configured destination for Add. "@default" is
// used when WithDefaultTaskList was not supplied.
func (p *Plugin) targetList() string {
	if p.defaultListID == "" {
		return defaultTaskListAlias
	}
	return p.defaultListID
}

// List enumerates every task across every tasklist the configured Client
// reports. The brief calls out "TaskList → Tasks" — a single ListTaskLists
// call followed by per-list ListTasks — and the upstream API mirrors that
// shape exactly, so we walk it sequentially.
//
// Two design choices worth noting here so future readers do not undo
// them:
//
//   - SourcePath is `tasklists/<id>` rather than the bare list id. The
//     prefix gives a future `marunage show` a stable URL-shaped value to
//     render and matches the upstream REST path, so a debugging user
//     can paste it after `https://www.googleapis.com/tasks/v1/` to find
//     the row in the Google API explorer.
//   - We do NOT short-circuit when one ListTasks fails. A partial result
//     would be worse than an error for the queue's reconciliation logic,
//     which uses "list size" as a heuristic for "did the source go
//     silent?". Returning the error fails closed.
func (p *Plugin) List(ctx context.Context) ([]source.Task, error) {
	c := p.client()
	if c == nil {
		return nil, ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lists, err := c.ListTaskLists(ctx)
	if err != nil {
		return nil, fmt.Errorf("googletasks: list tasklists: %w", err)
	}
	var out []source.Task
	for _, l := range lists {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tasks, err := c.ListTasks(ctx, l.ID)
		if err != nil {
			return nil, fmt.Errorf("googletasks: list tasks in %q: %w", l.ID, err)
		}
		for _, t := range tasks {
			out = append(out, makeSourceTask(l, t))
		}
	}
	return out, nil
}

// makeSourceTask is the (GTaskList, GTask) -> source.Task lift shared by
// List and Add. RawMetadata carries the tasklist id+title so the queue
// layer can disambiguate rows whose upstream task ids happen to collide
// across lists (Google's API does not document global uniqueness, and
// the queue's (source, external_id) UNIQUE constraint would otherwise
// silently fold the duplicates into a single row).
func makeSourceTask(l GTaskList, t GTask) source.Task {
	return source.Task{
		Source:     pluginName,
		ExternalID: t.ID,
		Title:      t.Title,
		Body:       t.Notes,
		Done:       t.Status == statusCompleted,
		SourcePath: "tasklists/" + l.ID,
		RawMetadata: map[string]any{
			"tasklist_id":    l.ID,
			"tasklist_title": l.Title,
		},
	}
}

// Setup is the OAuth / smoke-test entry point for the source. PR-84
// scaffolds the contract; the real OAuth dance lands in a follow-up PR
// once the secrets backend integration story is settled. Returning
// ErrNotConfigured here is the documented "not implemented yet" signal —
// the CLI surface for `marunage setup --source googletasks` will report
// it as "wire OAuth in PR-XX".
func (p *Plugin) Setup(ctx context.Context, opts source.SetupOptions) error {
	_ = opts
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrNotConfigured
}

// AuthStatus translates the upstream Client's reachability into the
// four-state enum from internal/source. The mapping is:
//
//   - no Client                 -> AuthNotConfigured
//   - Ping returns nil          -> AuthAuthenticated
//   - Ping returns ErrUnauthorized -> AuthRevoked
//   - any other Ping error      -> propagated unchanged (the caller
//     decides whether a transient
//     network failure should retry
//     or surface to the user).
//
// AuthExpired is NOT used today because the Google Tasks token refresh
// is handled inside the real Client (a transparent OAuth refresher
// wrapping the http.RoundTripper). By the time Ping fails with
// ErrUnauthorized the refresh has already been attempted and lost — so
// the credential is genuinely revoked, not merely expired.
func (p *Plugin) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	c := p.client()
	if c == nil {
		return source.AuthNotConfigured, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := c.Ping(ctx); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return source.AuthRevoked, nil
		}
		return "", fmt.Errorf("googletasks: ping: %w", err)
	}
	return source.AuthAuthenticated, nil
}

// Add inserts a new task in the configured target tasklist. notes lands
// in the upstream Notes field; the queue layer surfaces it as task body.
func (p *Plugin) Add(ctx context.Context, title, notes string) (source.Task, error) {
	c := p.client()
	if c == nil {
		return source.Task{}, ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return source.Task{}, err
	}
	if title == "" {
		return source.Task{}, ErrInvalidTitle
	}
	listID := p.targetList()
	got, err := c.InsertTask(ctx, listID, GTask{
		Title:  title,
		Notes:  notes,
		Status: statusNeedsAction,
	})
	if err != nil {
		return source.Task{}, fmt.Errorf("googletasks: insert into %q: %w", listID, err)
	}
	// We do not have the GTaskList Title in hand here (Add is addressed
	// by id only), so we synthesize a minimal one. The queue layer cares
	// about tasklist_id for dedup; tasklist_title is a UX hint only and
	// can be filled in on the next List sweep.
	return makeSourceTask(GTaskList{ID: listID}, got), nil
}

// Complete patches the upstream task to status="completed". The brief
// calls this out as the rear half of the marunage-side "done" mirror:
// when the queue marks a task done, this method propagates the state to
// Google Tasks so the user sees the same status in both places.
//
// Why findTaskList(): the source.Plugin contract takes only the
// externalID, not (tasklist, taskID) pair, so we have to discover which
// list the task lives in. The cost is one extra ListTasks call per list
// in the worst case; in practice most users have one tasklist so the
// overhead is invisible. Caching the mapping is left for a follow-up
// PR once we have evidence the cost matters.
func (p *Plugin) Complete(ctx context.Context, externalID string) error {
	c := p.client()
	if c == nil {
		return ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if externalID == "" {
		return ErrInvalidTaskID
	}
	listID, err := p.findTaskList(ctx, c, externalID)
	if err != nil {
		return err
	}
	if _, err := c.PatchTask(ctx, listID, externalID, GTask{Status: statusCompleted}); err != nil {
		// TOCTOU race: the row existed at findTaskList but vanished
		// before the patch landed. Translate to the typed sentinel so
		// callers branch on errors.Is rather than parsing strings.
		if errors.Is(err, ErrUpstreamTaskMissing) {
			return ErrTaskNotFound
		}
		return fmt.Errorf("googletasks: patch %q in %q: %w", externalID, listID, err)
	}
	return nil
}

// Delete removes the upstream task. Same locator strategy as Complete.
func (p *Plugin) Delete(ctx context.Context, externalID string) error {
	c := p.client()
	if c == nil {
		return ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if externalID == "" {
		return ErrInvalidTaskID
	}
	listID, err := p.findTaskList(ctx, c, externalID)
	if err != nil {
		return err
	}
	if err := c.DeleteTask(ctx, listID, externalID); err != nil {
		// Same TOCTOU race shape as Complete — translate to the typed
		// sentinel so the caller's branch is identical for both ops.
		if errors.Is(err, ErrUpstreamTaskMissing) {
			return ErrTaskNotFound
		}
		return fmt.Errorf("googletasks: delete %q in %q: %w", externalID, listID, err)
	}
	return nil
}

// findTaskList walks every tasklist the upstream knows about and returns
// the id of the list that contains taskID.
//
// Returns ErrTaskNotFound when no list claims the id, and
// ErrAmbiguousTaskID when more than one does. We deliberately keep
// scanning every list (rather than short-circuiting on the first hit)
// so the ambiguity surfaces loudly: Google's API does not document
// task ids as globally unique, and silently picking the first match
// would silently flip the wrong row.
func (p *Plugin) findTaskList(ctx context.Context, c Client, taskID string) (string, error) {
	lists, err := c.ListTaskLists(ctx)
	if err != nil {
		return "", fmt.Errorf("googletasks: list tasklists: %w", err)
	}
	var found string
	for _, l := range lists {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		tasks, err := c.ListTasks(ctx, l.ID)
		if err != nil {
			return "", fmt.Errorf("googletasks: list tasks in %q: %w", l.ID, err)
		}
		for _, t := range tasks {
			if t.ID != taskID {
				continue
			}
			if found != "" {
				return "", fmt.Errorf("%w: %q in %q and %q", ErrAmbiguousTaskID, taskID, found, l.ID)
			}
			found = l.ID
		}
	}
	if found == "" {
		return "", ErrTaskNotFound
	}
	return found, nil
}
