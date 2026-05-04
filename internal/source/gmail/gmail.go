// Package gmail implements the Gmail Discovery source plugin promised in
// docs/requirement.md row #2 of the standard sources table and detailed
// in PR-80 of pr_split_plan.md.
//
// The plugin walks unread mail matching the configured Gmail search
// query (default `is:unread to:me -label:auto-archived`) and surfaces
// each message as a source.Task. Marking a task complete from the queue
// side flips the upstream message to "read" AND applies an auto-archive
// label so the same message does not re-appear on the next discovery
// loop.
//
// Design boundaries:
//
//   - The plugin talks to upstream through the Client interface in
//     client.go. PR-80 ships only the in-memory test fake; PR-71 (or a
//     follow-up) wires a real Gmail client (gws shell-out / OAuth-local /
//     google.golang.org/api/gmail/v1) behind the same seam.
//
//   - Per-source checkpoint state lives in the injected Checkpointer
//     (KVStateRepo at runtime). The plugin does not import internal/store
//     so PR-80 can land before PR-71 wires the registry to the database.
//
//   - The plugin is read-mostly: only Complete writes upstream. We do not
//     implement Adder (no "send mail from queue" use case) or Deleter
//     (deleting mail from the queue would be irrecoverable). Phase 1 has
//     no dedicated "write-back-only" SyncMode, so the manifest declares
//     bidirectional — the *narrow* shape of that bidirectionality is the
//     "no Adder, no Deleter" guarantee enforced by the adapter type and
//     the cross-check in RegisterBuiltin.
//
//   - Complete is upstream-mutating. Callers (PR-71 scheduler, web UI
//     mutation handlers) MUST gate it behind the same human-approval /
//     audit flow they use for any other outbound action; the plugin
//     itself does not see the surrounding policy and so cannot enforce
//     it. The interface deliberately accepts only an externalID so a
//     misuse (e.g. a daemon iterating "everything") is loud at the call
//     site rather than buried in this package.
//
//   - PII surface (downstream storage / log redaction concern):
//     Task.Body carries Gmail's per-message "snippet" (first ~200 char
//     preview, plain text); Task.Title carries the Subject; and
//     RawMetadata may carry "from" (sender address) and "labels" (the
//     user's full label set, which can include sensitive routing tags
//     like HR/Salary or Legal/Privileged). These flow into tasks.body
//     / tasks.raw_metadata and through every consumer that reads them.
//     The plugin does not redact — that is the storage and audit
//     layer's responsibility — but enumerates the surfaces here so the
//     downstream redaction policy review (and OpenClaw §11.1 PII
//     check) has a single source of truth.
package gmail

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/haruotsu/marunage/internal/source"
)

// Default values surface as package-level constants so tests can assert
// the contract without re-typing literal strings.
const (
	// DefaultQuery is the search Gmail receives when WithQuery is not
	// set. It mirrors docs/requirement.md PR-80 ("is:unread to:me
	// -label:auto-archived"): unread mail addressed to the user that
	// has not yet been auto-archived by a previous discovery run.
	DefaultQuery = "is:unread to:me -label:auto-archived"

	// DefaultCompleteLabel is the upstream label Complete attaches to
	// signal "marunage has handled this thread, do not re-pick". The
	// label name matches the negative selector in DefaultQuery so the
	// two work together as a single closed loop.
	DefaultCompleteLabel = "auto-archived"

	// DefaultCheckpointKey is the kv_state key Since uses to remember
	// the highest message id seen on the previous run. Mirrors the
	// `gmail_last_id` value documented in docs/requirement.md PR-80
	// and the [discovery.gmail] config defaults.
	DefaultCheckpointKey = "gmail_last_id"

	// pluginName is the canonical Source emitted on every Task and the
	// name under which the Adapter is registered. Kept private so
	// downstream code uses Adapter.Name() rather than reaching into the
	// internals of this package.
	pluginName = "gmail"

	// unreadLabel is the Gmail-internal label for unread messages.
	// Complete always removes it so the thread stops appearing in
	// "is:unread" searches.
	unreadLabel = "UNREAD"
)

// Typed sentinel errors. Callers branch on errors.Is rather than parsing
// strings.
var (
	// ErrClientNotSet is returned by methods that require a configured
	// Client but were called on a Plugin constructed without WithClient.
	// This is distinct from ErrClientNotConfigured (which a real Client
	// returns for "credentials missing"); ErrClientNotSet means the
	// programmer never wired a Client at all.
	ErrClientNotSet = errors.New("gmail: client not set")

	// ErrTaskNotFound is returned by Complete when the upstream message
	// id is missing. Equivalent to markdown.ErrTaskNotFound so the
	// queue layer can branch on a single sentinel across sources.
	ErrTaskNotFound = errors.New("gmail: task not found")
)

// Checkpointer is the minimal key/value store the plugin needs to drive
// Since's incremental gate. The interface is local to this package so
// PR-80 builds without a hard dependency on internal/store.KVStateRepo;
// at runtime PR-71's registry wires the real repo behind it.
type Checkpointer interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// Plugin is the entry point for the Gmail source. Construct with New
// and reuse.
//
// The Plugin's only mutable shared state is the configuration set by
// Option values — those are written by New and only read after.
// `mu` therefore exists as defence-in-depth around Complete's call to
// Client.ModifyLabels: the contract requires Client implementations to
// be safe under concurrent use, but a hand-rolled fake or a partial
// `gws` shell-out wrapper might not be, and serialising at this layer
// removes one source of "looks-fine-in-tests / corrupts-in-prod" risk.
// Since reads no shared state and so does NOT take the lock; that
// asymmetry is deliberate, not an oversight.
type Plugin struct {
	client        Client
	query         string
	completeLabel string
	checkpointer  Checkpointer
	checkpointKey string

	mu sync.Mutex
}

// Option is the functional-option shape New accepts. Mirrors the pattern
// used in markdown.New / store.NewTaskRepo so callers see a consistent
// style across the codebase.
type Option func(*Plugin)

// WithClient injects the Gmail client implementation. Without one, every
// upstream-facing method (List, Since, Complete, AuthStatus, Setup)
// surfaces ErrClientNotSet — except AuthStatus, which reports
// AuthNotConfigured so `marunage auth-status` does not crash on a fresh
// install.
func WithClient(c Client) Option { return func(p *Plugin) { p.client = c } }

// WithQuery overrides the default Gmail search. Pass any string Gmail's
// search syntax accepts; the plugin does no parsing.
func WithQuery(q string) Option { return func(p *Plugin) { p.query = q } }

// WithCompleteLabel overrides the upstream label applied by Complete.
// Useful when a deployment uses a non-default archive label (e.g.
// "Marunage/Done").
func WithCompleteLabel(label string) Option {
	return func(p *Plugin) { p.completeLabel = label }
}

// WithCheckpointer wires the Since-gate persistence. Optional: without a
// Checkpointer, Since degrades to List behaviour (no state remembered
// between calls). The single-shot CLI path is fine without one; the
// daemon path must supply it.
func WithCheckpointer(c Checkpointer) Option {
	return func(p *Plugin) { p.checkpointer = c }
}

// WithCheckpointKey overrides the kv_state key used to persist the
// last-seen message id. Defaults to DefaultCheckpointKey. Most callers
// leave this unset — the override exists so a deployment running
// multiple Gmail accounts under the same database can give each its own
// checkpoint slot.
func WithCheckpointKey(key string) Option {
	return func(p *Plugin) { p.checkpointKey = key }
}

// New constructs a Plugin with the given options applied to the
// documented defaults. Defaults are filled in before option application
// so an Option can override any of them.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		query:         DefaultQuery,
		completeLabel: DefaultCompleteLabel,
		checkpointKey: DefaultCheckpointKey,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Query reports the resolved search string after option application.
// Used by tests and by RegisterBuiltin / Phase 1 setup paths that need
// to log the effective configuration; not part of the source.Plugin
// contract.
func (p *Plugin) Query() string { return p.query }

// CompleteLabel reports the resolved auto-archive label.
func (p *Plugin) CompleteLabel() string { return p.completeLabel }

// CheckpointKey reports the resolved kv_state key.
func (p *Plugin) CheckpointKey() string { return p.checkpointKey }

// List walks the configured Gmail query and converts each upstream
// message into a source.Task. Order matches what the client returns —
// for the official Gmail API that means newest first, which the
// Sincer path relies on for correct checkpoint advancement.
func (p *Plugin) List(ctx context.Context) ([]source.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.client == nil {
		return nil, ErrClientNotSet
	}
	msgs, err := p.client.List(ctx, p.query)
	if err != nil {
		return nil, fmt.Errorf("gmail list: %w", err)
	}
	out := make([]source.Task, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toTask(m, p.completeLabel))
	}
	return out, nil
}

// Since returns only the messages newer than the persisted checkpoint.
// Newness is determined by the Client's returned order: the plugin walks
// the slice top-to-bottom (newest first) and stops at the message id
// that matches the stored checkpoint. After a successful, non-empty
// run, the checkpoint advances to the head of the new slice.
//
// Without a Checkpointer the plugin degrades to List — there is no
// place to remember state, and returning everything is the only safe
// answer for a one-shot CLI invocation.
func (p *Plugin) Since(ctx context.Context, _ string) ([]source.Task, error) {
	if p.checkpointer == nil {
		return p.List(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.client == nil {
		return nil, ErrClientNotSet
	}

	prev, err := p.checkpointer.Get(ctx, p.checkpointKey)
	if err != nil {
		return nil, fmt.Errorf("gmail checkpoint get: %w", err)
	}

	msgs, err := p.client.List(ctx, p.query)
	if err != nil {
		return nil, fmt.Errorf("gmail list: %w", err)
	}

	// Truncate at the previously-seen id. Anything from that index
	// onward was returned on a previous run.
	if prev != "" {
		for i, m := range msgs {
			if m.ID == prev {
				msgs = msgs[:i]
				break
			}
		}
	}

	if len(msgs) == 0 {
		// Nothing new — leave the checkpoint untouched so the next
		// run still has a stable anchor.
		return []source.Task{}, nil
	}

	out := make([]source.Task, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toTask(m, p.completeLabel))
	}

	// Advance the checkpoint to the newest seen id. msgs[0] is newest
	// per the Gmail API ordering contract above.
	if err := p.checkpointer.Set(ctx, p.checkpointKey, msgs[0].ID); err != nil {
		return nil, fmt.Errorf("gmail checkpoint set: %w", err)
	}
	return out, nil
}

// Complete marks the upstream message as read AND applies the configured
// auto-archive label so the next discovery run does not re-pick the
// same thread. The two label edits are issued in a single
// ModifyLabels call so the upstream side never sees a half-applied
// "marked read but not archived" state.
func (p *Plugin) Complete(ctx context.Context, externalID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.client == nil {
		return ErrClientNotSet
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.client.ModifyLabels(ctx, externalID, ModifyLabelsRequest{
		AddLabels:    []string{p.completeLabel},
		RemoveLabels: []string{unreadLabel},
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrClientMessageNotFound) {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, externalID)
	}
	return fmt.Errorf("gmail complete: %w", err)
}

// AuthStatus reports the current credential health. A nil client is
// reported as AuthNotConfigured so a fresh install can answer
// `marunage auth-status` without panicking. A cancelled ctx fails up-
// front so the credential probe never reaches the network.
func (p *Plugin) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.client == nil {
		return source.AuthNotConfigured, nil
	}
	st, err := p.client.AuthStatus(ctx)
	if err != nil {
		switch {
		case errors.Is(err, ErrClientNotConfigured):
			return source.AuthNotConfigured, nil
		case errors.Is(err, ErrClientCredentialsExpired):
			return source.AuthExpired, nil
		case errors.Is(err, ErrClientCredentialsRevoked):
			return source.AuthRevoked, nil
		}
		return "", fmt.Errorf("gmail auth status: %w", err)
	}
	return st, nil
}

// Setup runs the client's authentication flow. Returns ErrClientNotSet
// when the plugin was constructed without WithClient; otherwise
// forwards opts so the client can honour NonInteractive. A cancelled
// ctx fails up-front so the auth dance is never started.
func (p *Plugin) Setup(ctx context.Context, opts source.SetupOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.client == nil {
		return ErrClientNotSet
	}
	if err := p.client.Authenticate(ctx, opts); err != nil {
		return fmt.Errorf("gmail setup: %w", err)
	}
	return nil
}

// toTask is the Message → source.Task lifter used by both List and
// Since. Pulled out so the conversion has one definition; otherwise a
// future field addition (e.g. From → Body prefix) would need two
// identical edits.
//
// completeLabel is consulted to decide whether the message is already
// upstream-done: if Gmail already carries the auto-archive label we
// surface Done=true so the queue's reconciliation logic can mark the
// row finished without issuing another ModifyLabels call.
func toTask(m Message, completeLabel string) source.Task {
	done := false
	for _, lbl := range m.Labels {
		if lbl == completeLabel {
			done = true
			break
		}
	}
	// Build RawMetadata sparsely: empty optional fields are omitted
	// rather than stored as zero values. Web UI / audit consumers
	// otherwise have to filter "" everywhere, and the from key in
	// particular is sender PII that should not be surfaced when there
	// is nothing to surface. When every optional field is empty the
	// map stays nil so a downstream `len(RawMetadata) == 0` test gives
	// the same answer as `RawMetadata == nil`.
	var meta map[string]any
	put := func(k string, v any) {
		if meta == nil {
			meta = map[string]any{}
		}
		meta[k] = v
	}
	if m.ThreadID != "" {
		put("thread_id", m.ThreadID)
	}
	if len(m.Labels) > 0 {
		// Defensive copy so a downstream mutation of RawMetadata cannot
		// reach back into the caller's Message slice.
		put("labels", append([]string(nil), m.Labels...))
	}
	if m.From != "" {
		put("from", m.From)
	}
	return source.Task{
		Source:      pluginName,
		ExternalID:  m.ID,
		Title:       m.Subject,
		Body:        m.Snippet,
		Done:        done,
		SourcePath:  messageURL(m.ID),
		RawMetadata: meta,
	}
}

// messageURL renders the canonical Gmail web URL for a message id so the
// queue / web UI can deep-link back to the source. The "/u/0/" segment
// always points at the primary signed-in account; users with multiple
// accounts may need to swap the index manually, which is acceptable
// since the link is a hint, not authoritative.
func messageURL(id string) string {
	return "https://mail.google.com/mail/u/0/#inbox/" + id
}
