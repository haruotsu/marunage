// Package source defines the Discovery plugin contract that PR-70 introduces.
// Source plugins (Markdown, Gmail, Calendar, Slack, GitHub, Google Tasks, ...)
// implement the same Go interface so the upcoming `marunage discover` loop
// (PR-71) can iterate over them uniformly without per-source switch
// statements.
//
// The contract mirrors the subcommand list in docs/requirement.md lines
// 102-114:
//
//	list / setup / auth-status         -- mandatory, captured in Plugin
//	since / add / complete / delete    -- optional, captured in Sincer /
//	                                       Adder / Completer / Deleter
//
// Optional capabilities use Go interface segregation rather than a single
// fat interface with "ErrNotSupported" stubs: callers do a type assertion
// (`p.(source.Adder)`) before invoking the optional method, which means
// the compiler — not a runtime nil check — guarantees that "this plugin
// can add" is honoured. The price is one extra interface declaration per
// capability; the gain is that `marunage discover` cannot accidentally
// dispatch an Add against a read-only source.
//
// Why this package does not import internal/source/markdown: the markdown
// adapter (internal/source/markdown/adapter.go) imports this package to
// declare it implements Plugin. Going the other way would create an import
// cycle, and would also bake markdown into every test that references the
// interface. Keep this file dependency-free.
package source

import (
	"context"
	"errors"
)

// AuthStatus is the four-state authentication summary documented in
// requirement.md lines 102-114. Defined as a typed string so callers can
// store it in a map / DB column unchanged, while still getting compile-time
// help if they mistype a constant name.
type AuthStatus string

const (
	// AuthAuthenticated means the source is ready to talk to its upstream
	// (or, for local sources like Markdown, that no external credential is
	// required).
	AuthAuthenticated AuthStatus = "authenticated"
	// AuthNotConfigured means setup has never been run for this source.
	AuthNotConfigured AuthStatus = "not_configured"
	// AuthExpired means the credential exists but has aged out and the user
	// must re-run setup (OAuth refresh failed, token TTL elapsed, ...).
	AuthExpired AuthStatus = "expired"
	// AuthRevoked means the credential is present but the upstream rejected
	// it (user removed the OAuth grant, secret was rotated externally, ...).
	AuthRevoked AuthStatus = "revoked"
)

// Task is the Discovery-side neutral view of one upstream item. Fields are
// intentionally a superset of the queue's tasks-table columns from
// requirement.md so the downstream materialisation layer (PR-71) can map
// 1:1 without losing data.
//
// Why a Discovery-local type rather than reusing internal/store.Task: the
// queue type carries id / status / priority columns that the source layer
// has no business setting. Keeping the types disjoint forces every PR that
// shuttles data between the two layers to think about the mapping
// explicitly, which is exactly what requirement.md's "explicit triage hand-
// off" rule wants.
type Task struct {
	// Source is the plugin name that produced this task ("markdown",
	// "gmail", ...). The queue layer copies it verbatim into the
	// tasks.source column so a row's origin is auditable forever.
	Source string

	// ExternalID is the upstream-stable identifier (Gmail message id,
	// Slack ts, Markdown marunage:id=..., ...). Combined with Source it
	// is the (source, external_id) UNIQUE index requirement.md describes.
	ExternalID string

	// Title is the one-line summary that becomes tasks.title.
	Title string

	// Body is multi-line richer detail (Gmail body, Slack message,
	// Markdown sub-bullets, ...). May be empty for sources that have
	// nothing beyond the title.
	Body string

	// Notes is reserved for plugin-side annotations a future PR may emit
	// (e.g. Markdown "indented sub-text capture"). Empty for PR-70.
	Notes string

	// Priority, when non-empty, is a plugin's hint to triage; the queue
	// is free to override. "" means "let triage decide".
	Priority string

	// SourcePath is the file/URL/channel the task came from. Used by
	// `marunage show` to render a "where did this come from" link and by
	// the dispatcher's reversibility audit. Empty when the source has no
	// notion of a path (e.g. a generic webhook).
	SourcePath string

	// Done flags upstream completion at observation time. The queue's
	// reconciliation logic uses this to skip re-enqueuing already-done
	// items and to mark previously-queued items finished.
	Done bool

	// RawMetadata is the bag of source-specific extras (Gmail labels,
	// Slack thread_ts, Markdown marker fields). Stored as map[string]any
	// rather than a typed struct so each source can stash whatever it
	// needs without growing this package.
	RawMetadata map[string]any
}

// SetupOptions carries the knobs Setup needs without forcing every plugin to
// invent its own option-bag type. Today the field set is small; PR-71 may
// extend it (e.g. an io.Writer for interactive prompts), but the shape is
// already a struct so additions are non-breaking.
type SetupOptions struct {
	// NonInteractive requests that Setup avoid prompting the user. CLI
	// callers pass this through `--non-interactive` to support automation;
	// plugins that need user input should return an error rather than
	// blocking on stdin.
	NonInteractive bool
}

// Plugin is the mandatory contract every Discovery source implements. The
// three methods correspond directly to the `list` / `setup` / `auth-status`
// subcommands documented in requirement.md.
type Plugin interface {
	// Name returns the stable identifier under which the registry
	// dispatches calls. It also becomes Task.Source.
	Name() string

	// List returns every currently-known task. Per requirement.md,
	// implementations MUST NOT cache: the loop uses Since when it wants
	// incremental behaviour.
	List(ctx context.Context) ([]Task, error)

	// Setup runs the plugin's authentication / smoke-test flow. opts is a
	// struct so the signature stays stable as new knobs (PR-71 daemon
	// mode, future telemetry hooks) get added.
	Setup(ctx context.Context, opts SetupOptions) error

	// AuthStatus reports the current credential state without performing
	// any mutating I/O.
	AuthStatus(ctx context.Context) (AuthStatus, error)
}

// Sincer is the optional `since` capability. Plugins that can return only
// items changed after a checkpoint implement this; the rest leave it off,
// and the loop falls back to List.
type Sincer interface {
	Since(ctx context.Context, checkpoint string) ([]Task, error)
}

// Adder is the optional `add` capability for bidirectional sources (Markdown,
// Google Tasks). Read-only sources (Gmail, Slack search) leave this off.
type Adder interface {
	Add(ctx context.Context, title, notes string) (Task, error)
}

// Completer is the optional `complete` capability. Implementations mark the
// upstream item done; the queue layer calls this on transitions to done so
// the source-of-truth stays consistent.
type Completer interface {
	Complete(ctx context.Context, externalID string) error
}

// Deleter is the optional `delete` capability. Used when the queue removes
// a task and the user opted to propagate the removal upstream.
type Deleter interface {
	Delete(ctx context.Context, externalID string) error
}

// ErrPluginNotFound is returned by Registry.Get for an unknown name. It
// lives in this file (rather than registry.go) so callers branching on
// plugin lookups can reference it without dragging the whole registry
// type into their import set — handy for tests that only need the typed
// error to assert on.
var ErrPluginNotFound = errors.New("source: plugin not found")
