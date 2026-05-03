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
	// configured account.
	ErrTaskNotFound = errors.New("googletasks: task not found")

	// ErrUnauthorized is returned by Client implementations to signal
	// the upstream rejected the credential (401 / 403). The plugin
	// translates this into source.AuthRevoked at the AuthStatus level.
	ErrUnauthorized = errors.New("googletasks: unauthorized")
)

// Plugin is the Google Tasks source. Construct one with New and reuse it;
// the struct is concurrency-safe because all upstream mutations go through
// the underlying Client (which the real implementation backs with a
// goroutine-safe *tasks.Service).
type Plugin struct {
	client Client

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
	return func(p *Plugin) { p.client = c }
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

// List returns every task across every tasklist the configured Client can
// see. Implementation lands in a later test cycle.
func (p *Plugin) List(ctx context.Context) ([]source.Task, error) {
	return nil, ErrNotConfigured
}

// Setup runs the OAuth / smoke-test flow for the source. Implementation
// lands in a later test cycle.
func (p *Plugin) Setup(ctx context.Context, opts source.SetupOptions) error {
	return ErrNotConfigured
}

// AuthStatus reports the current credential state. Implementation lands
// in a later test cycle.
func (p *Plugin) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return source.AuthNotConfigured, nil
}

// Add inserts a new task in the default tasklist. Implementation lands
// in a later test cycle.
func (p *Plugin) Add(ctx context.Context, title, notes string) (source.Task, error) {
	return source.Task{}, ErrNotConfigured
}

// Complete flips the upstream task identified by externalID to status
// "completed". Implementation lands in a later test cycle.
func (p *Plugin) Complete(ctx context.Context, externalID string) error {
	return ErrNotConfigured
}

// Delete removes the upstream task identified by externalID. Implementation
// lands in a later test cycle.
func (p *Plugin) Delete(ctx context.Context, externalID string) error {
	return ErrNotConfigured
}
