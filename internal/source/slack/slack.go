// Package slack implements the Slack Discovery source plugin promised in
// docs/pr_split_plan.md PR-82. The plugin pulls Slack mentions and DMs
// through a Client interface (initially backed by the Slack MCP server,
// later swappable for a direct Slack Web API client) and exposes them
// through the same Discovery contract markdown / gmail / github use.
//
// Phase 1 positioning: Slack also acts as the outbound completion-notify
// channel — Completer posts "タスク #N done" to a configured DM, so the
// queue layer's "task closed" hand-off can call source.Plugin.Complete
// uniformly across sources without a Slack-specific switch.
//
// Why a Client interface rather than wiring the MCP/web client directly:
// the MCP transport is async and lives outside the binary; we want unit
// tests to exercise the plugin's Since/Complete logic with a synchronous
// in-process fake (see slack_test.go::fakeClient). The runtime wiring
// (PR-71+) supplies a real Client adapter on top of the MCP RPC.
package slack

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/haruotsu/marunage/internal/source"
)

// pluginName is the canonical identifier the plugin and its adapter agree
// on. Kept as a package-level constant so a future rename is one edit
// (and the manifest catches the drift via ValidateAgainstManifest).
const pluginName = "slack"

// CheckpointKey is the kv_state key under which the Sincer stores the
// last-seen Slack ts. Exported so the daemon-side caller (PR-71) and tests
// can reference the same string instead of hard-coding it twice.
const CheckpointKey = "slack:last_ts"

// notifyMessageFormat is the documented "task done" notification template
// from docs/pr_split_plan.md PR-82. Centralised so a future copy change is
// one edit and the format never accidentally diverges between the test and
// the implementation.
const notifyMessageFormat = "タスク #%s done"

// titleMaxLen caps the Title field after we split a multi-line message.
// Slack messages can be very long; the queue's tasks.title column treats
// title as a one-line summary, and the Body field carries the full text.
// The constant matches the empirical maximum used by Slack's preview UI.
const titleMaxLen = 200

// Typed sentinel errors. Callers branch on errors.Is rather than parsing
// strings; the CLI binding (PR-70 discover) maps these to documented
// exit codes.
var (
	// ErrClientNotConfigured is returned by methods that need a real
	// Slack Client when the plugin was built without WithClient. Surfaced
	// as a typed error so the daemon (PR-71) can distinguish "not
	// configured" from a transport failure.
	ErrClientNotConfigured = errors.New("slack: client not configured")

	// ErrInvalidTaskID is returned by Complete when the caller passes an
	// empty externalID. Posting a DM with "#  done" would mislead the
	// recipient, so we fail loud instead.
	ErrInvalidTaskID = errors.New("slack: invalid task id")

	// ErrNotifyChannelRequired is returned by Complete when no
	// WithNotifyChannelID was supplied. The completion notifier has no
	// reasonable default destination, so we refuse rather than picking a
	// channel for the user.
	ErrNotifyChannelRequired = errors.New("slack: notify channel id is required")
)

// Message is the source-side view of one Slack message. The struct is
// the wire-format the Client interface speaks; the plugin lifts it into
// source.Task, so a future client implementation can emit this shape
// without depending on the source package.
type Message struct {
	// ChannelID is the Slack channel id the message was posted in.
	// For mentions this is a public/private channel; for DMs this is
	// the IM channel id (D...).
	ChannelID string

	// ChannelType is the Slack channel kind: "channel" / "im" / "mpim" /
	// "group". The plugin uses this to gate dm_id metadata population
	// and to let downstream callers pick channel-specific UI affordances.
	ChannelType string

	// TS is the message timestamp string (e.g. "1700000000.000100").
	// Slack ts values are decimal-formatted numbers used both as event
	// identifiers and as the Sincer checkpoint payload.
	TS string

	// ThreadTS is the parent message's ts when this message is a reply,
	// "" otherwise. Carried into RawMetadata so downstream completion
	// can post into the same thread.
	ThreadTS string

	// UserID is the Slack uid of the author (U... or W...).
	UserID string

	// Text is the rendered message body.
	Text string

	// Permalink is the slack.com/archives URL Slack returns for the
	// message. Used as Task.SourcePath so `marunage show` can deep-link.
	Permalink string
}

// Client is the abstraction over Slack RPCs the plugin needs. The
// interface intentionally exposes only the four operations the plugin
// uses; a fake in tests implements them directly, and the production
// wiring (PR-71+) bridges this to the Slack MCP server.
type Client interface {
	// FetchMentions returns the user's mentions newer than sinceTS.
	// Empty sinceTS means "no lower bound — return all available items
	// the upstream is willing to surface in one call".
	FetchMentions(ctx context.Context, sinceTS string) ([]Message, error)

	// FetchDMs returns DMs newer than sinceTS, with the same lower-bound
	// semantics as FetchMentions.
	FetchDMs(ctx context.Context, sinceTS string) ([]Message, error)

	// PostDM sends text to channelID. Used by Completer to post the
	// "タスク #N done" notification.
	PostDM(ctx context.Context, channelID, text string) error

	// AuthStatus reports the current credential state. Forwarded by
	// Plugin.AuthStatus and consulted by the daemon's startup probe.
	AuthStatus(ctx context.Context) (source.AuthStatus, error)

	// Setup runs the one-time auth flow. nonInteractive is forwarded
	// from source.SetupOptions; clients that need user input should
	// return an error rather than blocking on stdin.
	Setup(ctx context.Context, nonInteractive bool) error
}

// nilClient is the zero-value default returned to Plugin.client when the
// caller did not pass WithClient. Every method returns
// ErrClientNotConfigured so calling List/Since on a misconfigured plugin
// surfaces a typed error instead of crashing on a nil-receiver call.
type nilClient struct{}

func (nilClient) FetchMentions(context.Context, string) ([]Message, error) {
	return nil, ErrClientNotConfigured
}

func (nilClient) FetchDMs(context.Context, string) ([]Message, error) {
	return nil, ErrClientNotConfigured
}

func (nilClient) PostDM(context.Context, string, string) error {
	return ErrClientNotConfigured
}

func (nilClient) AuthStatus(context.Context) (source.AuthStatus, error) {
	return source.AuthNotConfigured, nil
}

func (nilClient) Setup(context.Context, bool) error {
	return ErrClientNotConfigured
}

// Checkpointer is the minimal kv-state contract the plugin needs to
// remember the last-seen Slack ts across runs. Defined locally (rather
// than imported from internal/store) so this package can be unit-tested
// without bringing in SQLite — a parallel-PR-friendly choice that mirrors
// the markdown package.
type Checkpointer interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// Plugin is the Slack Discovery source. Construct via New and reuse it:
// the struct is safe for concurrent reads (List / Since / AuthStatus do
// not mutate state) but Set on a Checkpointer races with itself, so
// callers must serialise concurrent Since invocations themselves.
type Plugin struct {
	client          Client
	checkpointer    Checkpointer
	includeMentions bool
	includeDM       bool
	notifyChannelID string
}

// Option is the functional-option shape New accepts. Mirrors markdown's
// Option type so callers see a uniform style across the codebase.
type Option func(*Plugin)

// WithClient injects a Slack Client. Callers should pass a real
// MCP-backed implementation in production and a fake in tests.
func WithClient(c Client) Option { return func(p *Plugin) { p.client = c } }

// WithCheckpointer wires the kv-state-backed Sincer cursor. Optional:
// when omitted, Since degrades to "behave like List with sinceTS=arg",
// which is the right behaviour for a one-shot CLI invocation but
// unsuitable for a long-running daemon.
func WithCheckpointer(c Checkpointer) Option { return func(p *Plugin) { p.checkpointer = c } }

// WithIncludeMentions toggles the FetchMentions branch. Off by default
// so a fresh install does not start polling Slack until the user opts
// in via config.toml's [discovery.slack] include_mentions key.
func WithIncludeMentions(on bool) Option { return func(p *Plugin) { p.includeMentions = on } }

// WithIncludeDM toggles the FetchDMs branch. Same opt-in rationale as
// WithIncludeMentions.
func WithIncludeDM(on bool) Option { return func(p *Plugin) { p.includeDM = on } }

// WithNotifyChannelID configures the destination for Completer's
// notification DM. The id is typically a Slack DM channel id (D...) or
// a group channel id (G...). Empty (the default) makes Complete error
// rather than guess.
func WithNotifyChannelID(id string) Option {
	return func(p *Plugin) { p.notifyChannelID = id }
}

// New constructs a Plugin with the given options. Defaults: nilClient
// for client (so every method returns ErrClientNotConfigured until
// configured), no checkpointer (Since=List), both feeds off.
func New(opts ...Option) *Plugin {
	p := &Plugin{client: nilClient{}}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns the canonical plugin identifier.
func (p *Plugin) Name() string { return pluginName }

// List returns every currently-known mention/DM with no lower bound.
// Use Since for incremental fetches; List is the discoverability seam
// for a fresh install or a "show me everything" CLI dump.
func (p *Plugin) List(ctx context.Context) ([]source.Task, error) {
	return p.fetch(ctx, "")
}

// Since returns mentions/DMs newer than checkpoint. When checkpoint is
// empty, the plugin reads the stored value from its Checkpointer (or
// uses "" when no checkpointer is wired). After a successful fetch the
// stored checkpoint is advanced to the maximum ts across both feeds —
// but only when that maximum is strictly greater than the effective
// checkpoint (the explicit argument or the value just read from the
// store). The cursor is therefore monotonically non-decreasing: an
// upstream that ignores `oldest` and replays stale items cannot drag
// the persisted value backwards (regression-pinned by
// TestSinceDoesNotRegressCheckpointWhenFetchReturnsOlderItems).
//
// Why "maximum ts" rather than "newest per-feed": Slack ts values are
// monotonically increasing event ids, and a single global cursor
// matches the Sincer interface (one checkpoint string) without losing
// safety — a future call passing the persisted value still asks the
// upstream for items strictly greater than the largest seen ts, so no
// item is double-counted.
func (p *Plugin) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	effective := checkpoint
	if effective == "" && p.checkpointer != nil {
		stored, err := p.checkpointer.Get(ctx, CheckpointKey)
		if err != nil {
			return nil, fmt.Errorf("slack: read checkpoint: %w", err)
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
	// Compute the maximum ts across the fetched tasks. RawMetadata["ts"]
	// is populated by messageToTask as a string; the type assert falls
	// back to "" on the impossible "missing key" case, which compareTS
	// treats as the minimum and therefore cannot promote a malformed row
	// over a real one.
	maxTS := ""
	for _, t := range tasks {
		ts, _ := t.RawMetadata["ts"].(string)
		if compareTS(ts, maxTS) > 0 {
			maxTS = ts
		}
	}
	// Defense-in-depth against an upstream that ignores the `oldest`
	// argument: the persisted checkpoint must be monotonic, never
	// regress. If every fetched ts is older than the cursor we asked
	// from, keep the existing checkpoint (i.e. write nothing) so a
	// misbehaving Client cannot trick the Sincer into re-feeding stale
	// items on the next call.
	if compareTS(maxTS, effective) <= 0 {
		return tasks, nil
	}
	if err := p.checkpointer.Set(ctx, CheckpointKey, maxTS); err != nil {
		return nil, fmt.Errorf("slack: persist checkpoint: %w", err)
	}
	return tasks, nil
}

// Complete posts the documented "タスク #<id> done" notification to the
// configured Slack DM/channel. externalID is the marunage task id; the
// plugin does not interpret it (any non-empty string is accepted) so a
// future PR that switches the queue id format does not need to touch
// this function.
func (p *Plugin) Complete(ctx context.Context, externalID string) error {
	if externalID == "" {
		return ErrInvalidTaskID
	}
	if p.notifyChannelID == "" {
		return ErrNotifyChannelRequired
	}
	text := fmt.Sprintf(notifyMessageFormat, externalID)
	if err := p.client.PostDM(ctx, p.notifyChannelID, text); err != nil {
		return fmt.Errorf("slack: post completion DM: %w", err)
	}
	return nil
}

// AuthStatus forwards to the underlying Client. nilClient returns
// AuthNotConfigured (with a nil error) so a freshly-built Plugin still
// reports a sensible state without an extra wrapper here.
func (p *Plugin) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return p.client.AuthStatus(ctx)
}

// Setup forwards opts.NonInteractive to the underlying Client. The
// Client decides whether the auth flow needs the user (interactive
// OAuth) or can run from environment-supplied credentials.
func (p *Plugin) Setup(ctx context.Context, opts source.SetupOptions) error {
	return p.client.Setup(ctx, opts.NonInteractive)
}

// fetch is the shared core for List/Since: dispatches to FetchMentions
// and/or FetchDMs based on the include flags, lifts each Message into a
// source.Task, and returns the merged slice.
func (p *Plugin) fetch(ctx context.Context, sinceTS string) ([]source.Task, error) {
	var out []source.Task
	if p.includeMentions {
		msgs, err := p.client.FetchMentions(ctx, sinceTS)
		if err != nil {
			return nil, fmt.Errorf("slack: fetch mentions: %w", err)
		}
		for _, m := range msgs {
			out = append(out, messageToTask(m))
		}
	}
	if p.includeDM {
		msgs, err := p.client.FetchDMs(ctx, sinceTS)
		if err != nil {
			return nil, fmt.Errorf("slack: fetch dms: %w", err)
		}
		for _, m := range msgs {
			out = append(out, messageToTask(m))
		}
	}
	return out, nil
}

// messageToTask is the lift from Slack's wire shape to source.Task. The
// mapping is documented in the package godoc and pinned by the
// adapter / list tests:
//
//	Source       = "slack"
//	ExternalID   = "<channel_id>:<ts>"
//	Title        = first line of Text (truncated to titleMaxLen)
//	Body         = full Text (only when multi-line)
//	SourcePath   = Permalink
//	RawMetadata  = { channel_id, channel_type, ts, thread_ts, user_id [, dm_id] }
func messageToTask(m Message) source.Task {
	title, body := splitTitleBody(m.Text)
	meta := map[string]any{
		"channel_id":   m.ChannelID,
		"channel_type": m.ChannelType,
		"ts":           m.TS,
		"thread_ts":    m.ThreadTS,
		"user_id":      m.UserID,
	}
	if m.ChannelType == "im" {
		meta["dm_id"] = m.ChannelID
	}
	return source.Task{
		Source:      pluginName,
		ExternalID:  m.ChannelID + ":" + m.TS,
		Title:       title,
		Body:        body,
		SourcePath:  m.Permalink,
		RawMetadata: meta,
	}
}

// splitTitleBody returns the first line as title (truncated) and the
// full text as body when the message is multi-line. Single-line
// messages return ("", body=="") in body so the caller can detect
// "nothing more to show" without comparing strings.
func splitTitleBody(text string) (title, body string) {
	idx := strings.IndexByte(text, '\n')
	if idx < 0 {
		return truncate(text, titleMaxLen), ""
	}
	return truncate(text[:idx], titleMaxLen), text
}

// truncate caps s at n runes, not bytes, so a multi-byte character is
// not split mid-byte (which would produce invalid UTF-8 in the title).
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

// compareTS compares two Slack ts strings numerically. Slack ts values
// are decimal numbers like "1700000000.000100"; lexicographic ordering
// is correct only when both sides have the same width, so we split on
// the dot, compare the integer half by length-then-lex (integer halves
// never carry leading zeros from Slack), and compare the fractional
// half after right-padding the shorter side with '0' so 0.1 and 0.10
// compare equal and 0.0001 sorts above 0.00009.
//
// Returns -1 / 0 / 1 like strings.Compare. An empty string is treated
// as "before everything else", which is the documented Sincer "no
// checkpoint yet" sentinel.
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

// splitTS returns (integer, fractional) halves of a Slack ts string.
// A missing dot means the whole value is the integer half.
func splitTS(s string) (string, string) {
	idx := strings.IndexByte(s, '.')
	if idx < 0 {
		return s, ""
	}
	return s[:idx], s[idx+1:]
}

// compareIntegerPart compares two non-negative decimal-digit strings by
// value: shorter strings are smaller (assumes no leading zeros — Slack
// ts integer halves never carry them), with lexicographic fallback when
// widths agree.
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

// compareFractionalPart compares two fractional halves (the part after
// the dot, no leading "0."). Right-pad the shorter side with '0' so
// "1" and "100" compare equal as fractions, and "0001" sorts above
// "00009" (0.0001 > 0.00009). Lex compare after padding works because
// every byte is a decimal digit.
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

// padRightZeros returns s padded on the right with '0' until it is at
// least n bytes long. s longer than n is returned unchanged.
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
