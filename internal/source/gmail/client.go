// Package gmail's client.go declares the interface the Gmail source plugin
// uses to talk to an upstream Gmail account. The plugin code is written
// against this interface so:
//
//   - tests can swap in an in-memory fake (no network, no OAuth dance);
//   - PR-80 itself ships without a hard dependency on a specific Gmail
//     SDK — a follow-up PR can plug in `gws` CLI shell-out, OAuth-local,
//     or the official Google API client behind the same seam without
//     touching the plugin's logic.
//
// Why a Discovery-local client type rather than reusing google.golang.org/
// api/gmail/v1 directly: the plugin only needs four operations (list,
// modify labels, authenticate, report status). Pulling the whole gmail/v1
// surface would inflate every test fixture in this package and would tie
// the plugin to a particular Google client release; keeping the seam
// narrow lets a future implementation choose any backing transport.
package gmail

import (
	"context"
	"errors"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// Message is the source-local view of one Gmail message that the plugin
// converts into a source.Task. The struct intentionally mirrors only the
// fields the plugin reads — Subject, Snippet, Labels — so the fake
// client used by unit tests does not have to fabricate the entire
// Gmail message envelope (headers, parts, attachments, ...).
type Message struct {
	// ID is the upstream-stable Gmail message id. Persisted as
	// Task.ExternalID so the (source, external_id) UNIQUE index can
	// dedupe across discovery runs.
	ID string

	// ThreadID is Gmail's conversation grouping. Surfaced through
	// Task.RawMetadata so triage / web UI can later collapse a thread
	// into one card without having to ask Gmail again.
	ThreadID string

	// Subject is the rendered subject line. Becomes Task.Title.
	Subject string

	// Snippet is Gmail's short preview text. Becomes Task.Body. We
	// deliberately do not pull the full message body here — a 100KB
	// HTML email would overflow tasks.body and Claude's prompt budget,
	// and the snippet is enough for the triage step.
	Snippet string

	// Labels is the message's current label set, copied verbatim. The
	// plugin reads it to decide whether the message is already
	// "Done" upstream (carries the auto-archived label) and surfaces
	// the slice through RawMetadata for downstream filters.
	Labels []string

	// From is the sender's address as Gmail rendered it. Optional;
	// kept here so a future "Body = From + Snippet" formatter does not
	// need a client surface change.
	From string

	// Date is the upstream "internalDate" timestamp. Optional, used
	// for ordering hints when the plugin walks the result in newest-
	// first order. Zero value is fine.
	Date time.Time
}

// ModifyLabelsRequest is the parameter struct for Client.ModifyLabels.
// Wrapping (Add / Remove) into a struct keeps the call sites readable —
// `client.ModifyLabels(ctx, id, req)` — and lets a future "rename label"
// op slot in without breaking the existing signature.
type ModifyLabelsRequest struct {
	// AddLabels are label IDs to attach to the message.
	AddLabels []string
	// RemoveLabels are label IDs to remove from the message. The plugin
	// passes "UNREAD" here as part of Complete to mark the thread read.
	RemoveLabels []string
}

// Client is the seam every Gmail plugin implementation talks to. The four
// methods correspond to "what the plugin actually does upstream":
//
//   - List   : `users.messages.list` for the configured query.
//   - ModifyLabels : `users.messages.modify` to mark read + apply auto-archive.
//   - Authenticate : kick off the credential dance during `marunage setup`.
//   - AuthStatus : report current credential health for `auth-status`.
type Client interface {
	List(ctx context.Context, query string) ([]Message, error)
	ModifyLabels(ctx context.Context, messageID string, req ModifyLabelsRequest) error
	Authenticate(ctx context.Context, opts source.SetupOptions) error
	AuthStatus(ctx context.Context) (source.AuthStatus, error)
}

// Sentinel errors a Client implementation can return so the plugin can
// translate them to the package-level typed errors callers branch on.
//
// We accept ANY error for "transport failure" — only the credential and
// "message not found" states have stable downstream meaning, so only
// those have dedicated sentinels.
var (
	// ErrClientMessageNotFound is returned by ModifyLabels (and
	// optionally List) when the upstream id no longer exists. The plugin
	// translates this into ErrTaskNotFound so callers can Is-check
	// against a single typed error regardless of backend.
	ErrClientMessageNotFound = errors.New("gmail: message not found upstream")

	// ErrClientNotConfigured signals that the client has never been
	// authenticated. AuthStatus surfaces this as AuthNotConfigured.
	ErrClientNotConfigured = errors.New("gmail: client not configured")

	// ErrClientCredentialsExpired signals an expired OAuth token. Maps
	// to AuthExpired so the CLI can suggest re-running setup.
	ErrClientCredentialsExpired = errors.New("gmail: credentials expired")

	// ErrClientCredentialsRevoked signals a revoked OAuth grant. Maps
	// to AuthRevoked.
	ErrClientCredentialsRevoked = errors.New("gmail: credentials revoked")
)
