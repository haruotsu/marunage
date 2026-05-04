// Package reaction implements the Slack Reaction Trigger Discovery source
// (docs/pr_split_plan.md PR-100). When a configured emoji reaction (e.g.
// :todo:, :inbox_tray:) is added to a Slack message, this plugin surfaces
// the message as a source.Task so the queue can pick it up automatically.
//
// The plugin polls for reaction events rather than subscribing to the Events
// API, which keeps the architecture synchronous and avoids requiring an
// inbound webhook endpoint. The underlying ReactionClient interface is
// abstract so tests can inject a fake without network I/O.
//
// Idempotency: ExternalID = "{channel_id}:{ts}:{reaction}:{user_id}" gives
// one task per (message, reaction-type, reacting-user) triple, matching the
// (source, external_id) UNIQUE constraint in the tasks table.
//
// DM on complete: when dm_on_complete is enabled and Complete is called,
// the plugin opens a DM channel with the reacting user (parsed from the
// ExternalID) and posts a "task done" notification. The user_id is embedded
// in the ExternalID so Complete does not need a separate metadata lookup.
package reaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/haruotsu/marunage/internal/source"
)

const (
	pluginName    = "slack:reaction"
	CheckpointKey = "slack:reaction:last_ts"
	titleMaxLen   = 200

	// externalIDParts is the number of colon-separated segments in a valid
	// ExternalID: channel_id, ts, reaction, user_id.
	externalIDParts = 4
)

// notifyMessageFormat is the DM sent to the reacting user when a task
// completes. %s receives the emoji name (without colons) so the user
// knows which reaction triggered the task.
const notifyMessageFormat = "Task done (reaction :%s: processed)"

var (
	// ErrClientNotConfigured is returned when the plugin was constructed
	// without WithClient. Every method that needs the Slack API returns
	// this typed error so callers can distinguish "not set up" from a
	// transient network failure.
	ErrClientNotConfigured = errors.New("slack/reaction: client not configured")

	// ErrInvalidTaskID is returned by Complete when externalID is empty.
	ErrInvalidTaskID = errors.New("slack/reaction: invalid task id")

	// ErrInvalidExternalID is returned by Complete when the externalID
	// cannot be parsed into the expected four-segment format.
	ErrInvalidExternalID = errors.New("slack/reaction: invalid external id format")
)

// ReactionEvent is the source-side view of one reaction-added event.
// The Client interface speaks this shape; the plugin lifts it to
// source.Task without depending on any concrete Slack wire format.
type ReactionEvent struct {
	// Reaction is the emoji name without surrounding colons (e.g. "todo").
	Reaction string

	// UserID is the Slack uid of the user who added the reaction.
	UserID string

	// ChannelID is the Slack channel containing the reacted message.
	ChannelID string

	// TS is the timestamp of the reacted message (e.g. "1700000000.000100").
	// Used as part of ExternalID and as the checkpoint cursor.
	TS string

	// Text is the content of the reacted message.
	Text string

	// Permalink is the slack.com/archives URL of the reacted message.
	Permalink string
}

// Client is the abstraction over Slack RPCs the reaction plugin needs.
// Implementations can back this with the Slack Web API, the MCP transport,
// or a test fake. The interface is intentionally narrow — only the four
// operations this package uses are declared here.
type Client interface {
	// FetchReactionEvents returns reaction events for messages where one of
	// the specified reactions was added, newer than sinceTS. The client is
	// responsible for filtering to the given reaction names; the plugin
	// forwards the configured list verbatim so the client can pick the most
	// efficient Slack API call for the job.
	FetchReactionEvents(ctx context.Context, reactions []string, sinceTS string) ([]ReactionEvent, error)

	// PostDM sends text to channelID. Used by Complete to deliver the
	// "task done" notification.
	PostDM(ctx context.Context, channelID, text string) error

	// OpenDM opens (or retrieves) the DM channel with the given Slack user
	// and returns the channel ID. Backed by conversations.open in production.
	OpenDM(ctx context.Context, userID string) (string, error)

	// AuthStatus reports the current credential state.
	AuthStatus(ctx context.Context) (source.AuthStatus, error)

	// Setup runs the one-time auth flow.
	Setup(ctx context.Context, nonInteractive bool) error
}

// nilClient is the zero-value default used when no WithClient option is
// supplied. Every method returns ErrClientNotConfigured so a misconfigured
// plugin surfaces a typed error instead of panicking on a nil-receiver call.
type nilClient struct{}

func (nilClient) FetchReactionEvents(context.Context, []string, string) ([]ReactionEvent, error) {
	return nil, ErrClientNotConfigured
}
func (nilClient) PostDM(context.Context, string, string) error { return ErrClientNotConfigured }
func (nilClient) OpenDM(context.Context, string) (string, error) {
	return "", ErrClientNotConfigured
}
func (nilClient) AuthStatus(context.Context) (source.AuthStatus, error) {
	return source.AuthNotConfigured, nil
}
func (nilClient) Setup(context.Context, bool) error { return ErrClientNotConfigured }

// Checkpointer is the minimal kv-state contract for persisting the
// last-seen ts across daemon runs. Mirrored from the parent slack package
// so this package can be tested independently without SQLite.
type Checkpointer interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// Plugin is the Slack Reaction Trigger Discovery source. Construct via
// New; the zero value is not usable. Thread-safety: List/Since/AuthStatus
// are safe for concurrent reads; concurrent Since calls race on the
// Checkpointer and must be serialised by the caller (same contract as the
// parent slack.Plugin).
type Plugin struct {
	client       Client
	checkpointer Checkpointer
	reactions    []string
	dmOnComplete bool
}

// Option is the functional-option shape accepted by New.
type Option func(*Plugin)

// WithClient injects a Slack Client. Pass a real implementation in
// production and a fake in tests.
func WithClient(c Client) Option { return func(p *Plugin) { p.client = c } }

// WithCheckpointer wires the kv-state cursor. When omitted, Since behaves
// like List (no stored checkpoint) — acceptable for one-shot CLI use but
// not for daemon mode.
func WithCheckpointer(c Checkpointer) Option { return func(p *Plugin) { p.checkpointer = c } }

// WithReactions sets the emoji names (without surrounding colons) that the
// plugin should watch. An empty list makes List/Since return immediately
// without calling the client.
func WithReactions(reactions []string) Option { return func(p *Plugin) { p.reactions = reactions } }

// WithDMOnComplete toggles the DM-notification behaviour in Complete.
// Off by default so a misconfigured plugin does not silently message users.
func WithDMOnComplete(on bool) Option { return func(p *Plugin) { p.dmOnComplete = on } }

// New constructs a Plugin with the given options. Default: nilClient
// (every method returns ErrClientNotConfigured), no checkpointer, no
// reactions, dm_on_complete=false.
func New(opts ...Option) *Plugin {
	p := &Plugin{client: nilClient{}}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns the canonical plugin identifier used as Task.Source.
func (p *Plugin) Name() string { return pluginName }

// List returns all reaction-triggered tasks with no lower time bound.
// Use Since for incremental fetches in daemon mode.
func (p *Plugin) List(ctx context.Context) ([]source.Task, error) {
	return p.fetch(ctx, "")
}

// Since returns reaction events newer than checkpoint. When checkpoint is
// empty the plugin reads the stored cursor from its Checkpointer (or uses
// "" when no Checkpointer is wired). After a successful fetch the stored
// cursor is advanced to the maximum ts seen — but only when that maximum
// is strictly greater than the effective cursor, preserving monotonicity.
func (p *Plugin) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	effective := checkpoint
	if effective == "" && p.checkpointer != nil {
		stored, err := p.checkpointer.Get(ctx, CheckpointKey)
		if err != nil {
			return nil, fmt.Errorf("slack/reaction: read checkpoint: %w", err)
		}
		effective = stored
	}
	tasks, err := p.fetch(ctx, effective)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 || p.checkpointer == nil {
		return tasks, nil
	}
	maxTS := ""
	for _, t := range tasks {
		ts, _ := t.RawMetadata["ts"].(string)
		if compareTS(ts, maxTS) > 0 {
			maxTS = ts
		}
	}
	if compareTS(maxTS, effective) <= 0 {
		return tasks, nil
	}
	if err := p.checkpointer.Set(ctx, CheckpointKey, maxTS); err != nil {
		return nil, fmt.Errorf("slack/reaction: persist checkpoint: %w", err)
	}
	return tasks, nil
}

// Complete delivers the "task done" DM to the reacting user when
// dm_on_complete is enabled. externalID must be in the four-segment format
// "{channel_id}:{ts}:{reaction}:{user_id}" produced by eventToTask. Empty
// externalID returns ErrInvalidTaskID; a malformed one (wrong segment count)
// returns ErrInvalidExternalID. When dm_on_complete is false, Complete is a
// no-op and returns nil.
func (p *Plugin) Complete(ctx context.Context, externalID string) error {
	if externalID == "" {
		return ErrInvalidTaskID
	}
	if !p.dmOnComplete {
		return nil
	}
	userID, err := userIDFromExternalID(externalID)
	if err != nil {
		return err
	}
	dmCh, err := p.client.OpenDM(ctx, userID)
	if err != nil {
		// userID is not included in the error message to avoid leaking Slack
		// user identifiers into application logs (OWASP A2 / audit-log hygiene).
		return fmt.Errorf("slack/reaction: open DM: %w", err)
	}
	// Extract the reaction name from the externalID to include in the
	// notification message so the user knows which reaction triggered the task.
	reactionName := reactionFromExternalID(externalID)
	text := fmt.Sprintf(notifyMessageFormat, reactionName)
	if err := p.client.PostDM(ctx, dmCh, text); err != nil {
		return fmt.Errorf("slack/reaction: post completion DM: %w", err)
	}
	return nil
}

// AuthStatus forwards to the underlying Client.
func (p *Plugin) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return p.client.AuthStatus(ctx)
}

// Setup forwards opts.NonInteractive to the underlying Client.
func (p *Plugin) Setup(ctx context.Context, opts source.SetupOptions) error {
	return p.client.Setup(ctx, opts.NonInteractive)
}

// fetch is the shared core for List/Since. Returns immediately with no
// client call when no reactions are configured.
func (p *Plugin) fetch(ctx context.Context, sinceTS string) ([]source.Task, error) {
	if len(p.reactions) == 0 {
		return nil, nil
	}
	events, err := p.client.FetchReactionEvents(ctx, p.reactions, sinceTS)
	if err != nil {
		return nil, fmt.Errorf("slack/reaction: fetch events: %w", err)
	}
	tasks := make([]source.Task, 0, len(events))
	for _, e := range events {
		tasks = append(tasks, eventToTask(e))
	}
	return tasks, nil
}

// eventToTask lifts a ReactionEvent into a source.Task.
//
// Mapping:
//
//	Source      = "slack:reaction"
//	ExternalID  = "{channel_id}:{ts}:{reaction}:{user_id}"
//	Title       = first line of Text (truncated to titleMaxLen)
//	Body        = full Text (when multi-line, "" otherwise)
//	Notes       = JSON {"permalink":"<url>","message":"<text>"} — satisfies
//	              the tasks.notes CHECK (json_valid) constraint from 0001_init.sql
//	SourcePath  = Permalink
//	RawMetadata = { reaction, user_id, channel_id, ts, permalink }
func eventToTask(e ReactionEvent) source.Task {
	title, body := splitTitleBody(e.Text)
	notesRaw, err := json.Marshal(map[string]string{
		"permalink": e.Permalink,
		"message":   e.Text,
	})
	if err != nil {
		// json.Marshal on map[string]string never fails; fallback is defensive.
		notesRaw = []byte(`{}`)
	}
	notes := string(notesRaw)
	meta := map[string]any{
		"reaction":   e.Reaction,
		"user_id":    e.UserID,
		"channel_id": e.ChannelID,
		"ts":         e.TS,
		"permalink":  e.Permalink,
	}
	return source.Task{
		Source:      pluginName,
		ExternalID:  e.ChannelID + ":" + e.TS + ":" + e.Reaction + ":" + e.UserID,
		Title:       title,
		Body:        body,
		Notes:       notes,
		SourcePath:  e.Permalink,
		RawMetadata: meta,
	}
}

// userIDFromExternalID extracts the user_id segment from an ExternalID in
// the "{channel_id}:{ts}:{reaction}:{user_id}" format. Returns
// ErrInvalidExternalID when the input has fewer than four colon-separated
// segments.
//
// Note: channel_id, ts, and reaction may themselves contain colons (Slack
// channel IDs and ts strings do not, but reaction names are alphanumeric).
// We split from the right to isolate user_id, which is always the last
// segment and never contains a colon (Slack user IDs are alphanumeric).
func userIDFromExternalID(externalID string) (string, error) {
	// Split on ":" keeping all segments. ExternalID was built as
	// channel:ts:reaction:userID — a simple strings.Split is sufficient
	// because none of the four fields contain colons in practice. We
	// require at least externalIDParts segments to catch obviously
	// malformed IDs early.
	parts := strings.Split(externalID, ":")
	if len(parts) < externalIDParts {
		return "", fmt.Errorf("%w: %q (need at least %d colon-separated segments)",
			ErrInvalidExternalID, externalID, externalIDParts)
	}
	userID := parts[len(parts)-1]
	if userID == "" {
		return "", fmt.Errorf("%w: %q (user_id segment is empty)", ErrInvalidExternalID, externalID)
	}
	return userID, nil
}

// reactionFromExternalID extracts the reaction name from an ExternalID in
// the "{channel_id}:{ts}:{reaction}:{user_id}" format. Returns the reaction
// segment (second-to-last), or the full externalID as fallback if parsing
// fails (so the notification message is always non-empty).
func reactionFromExternalID(externalID string) string {
	parts := strings.Split(externalID, ":")
	if len(parts) < externalIDParts {
		return externalID
	}
	return parts[len(parts)-2]
}

// splitTitleBody returns the first line as title (truncated to titleMaxLen
// runes) and the full text as body when the message is multi-line. Single-
// line messages return ("", "") in body.
func splitTitleBody(text string) (title, body string) {
	idx := strings.IndexByte(text, '\n')
	if idx < 0 {
		return truncate(text, titleMaxLen), ""
	}
	return truncate(text[:idx], titleMaxLen), text
}

// truncate caps s at n runes so a multi-byte character is never split
// mid-byte (which would produce invalid UTF-8).
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// compareTS compares two Slack ts strings numerically. Slack ts values are
// decimal numbers like "1700000000.000100"; lexicographic ordering is correct
// only when both sides have the same width. We split on the dot, compare the
// integer half by length-then-lex, then the fractional half after right-padding
// to equal width. An empty string is treated as "before everything else" — the
// documented "no checkpoint yet" sentinel.
//
// This mirrors the unexported compareTS in the parent slack package; it is
// duplicated here rather than exported there to keep the packages independently
// testable.
func compareTS(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return -1
	}
	if b == "" {
		return 1
	}
	aInt, aFrac := splitTS(a)
	bInt, bFrac := splitTS(b)
	if c := compareIntegerPart(aInt, bInt); c != 0 {
		return c
	}
	return compareFractionalPart(aFrac, bFrac)
}

func splitTS(s string) (string, string) {
	idx := strings.IndexByte(s, '.')
	if idx < 0 {
		return s, ""
	}
	return s[:idx], s[idx+1:]
}

func compareIntegerPart(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareFractionalPart(a, b string) int {
	if a == b {
		return 0
	}
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	a = padRightZeros(a, maxLen)
	b = padRightZeros(b, maxLen)
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func padRightZeros(s string, n int) string {
	if len(s) >= n {
		return s
	}
	pad := make([]byte, n-len(s))
	for i := range pad {
		pad[i] = '0'
	}
	return s + string(pad)
}
