package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/haruotsu/marunage/internal/source"
)

// CommandClient implements Client by shelling out to an operator-supplied
// command — the "generic command adapter". It is deliberately agent-agnostic:
// the command may bridge to the Slack MCP server, call the Slack Web API,
// replay a saved export, or anything else. marunage knows nothing about MCP or
// claude; it only knows this contract:
//
//	<argv...> mentions [sinceTS]   -> stdout: JSON array of messages
//	<argv...> dms      [sinceTS]   -> stdout: JSON array of messages
//	<argv...> post-dm  <channelID> -> message text on stdin; exit 0 on success
//	<argv...> auth-status          -> stdout: authenticated|expired|revoked|not_configured
//
// Each message object uses snake_case keys mirroring Message:
// {channel_id, channel_type, ts, thread_ts, user_id, text, permalink}.
type CommandClient struct {
	argv   []string
	runner cmdRunner
}

// cmdRunner abstracts process execution so tests inject a fake instead of
// spawning the real adapter.
type cmdRunner interface {
	run(ctx context.Context, stdin string, argv []string) ([]byte, error)
}

// CommandOption customises a CommandClient.
type CommandOption func(*CommandClient)

// withCmdRunner swaps the process launcher (tests only).
func withCmdRunner(r cmdRunner) CommandOption {
	return func(c *CommandClient) { c.runner = r }
}

// NewCommandClient builds a CommandClient that invokes argv. argv[0] is the
// program; the operation/args are appended per call.
func NewCommandClient(argv []string, opts ...CommandOption) *CommandClient {
	c := &CommandClient{argv: argv, runner: execCmdRunner{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// wireMessage is the JSON shape the adapter command emits, decoded into the
// internal Message. Keeping a separate struct lets the on-the-wire contract
// stay snake_case while Message stays Go-idiomatic.
type wireMessage struct {
	ChannelID   string `json:"channel_id"`
	ChannelType string `json:"channel_type"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	UserID      string `json:"user_id"`
	Text        string `json:"text"`
	Permalink   string `json:"permalink"`
}

// ErrCommandNotConfigured is returned when a CommandClient is built with no
// program to run.
var ErrCommandNotConfigured = errors.New("slack: command adapter argv is empty")

func (c *CommandClient) fetch(ctx context.Context, op, sinceTS string) ([]Message, error) {
	if len(c.argv) == 0 {
		return nil, ErrCommandNotConfigured
	}
	argv := append(append([]string(nil), c.argv...), op)
	if sinceTS != "" {
		argv = append(argv, sinceTS)
	}
	out, err := c.runner.run(ctx, "", argv)
	if err != nil {
		return nil, fmt.Errorf("slack command %s: %w", op, err)
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	var wire []wireMessage
	if err := json.Unmarshal(out, &wire); err != nil {
		return nil, fmt.Errorf("slack command %s: decode json: %w", op, err)
	}
	msgs := make([]Message, len(wire))
	for i, w := range wire {
		msgs[i] = Message(w)
	}
	return msgs, nil
}

// FetchMentions runs `<argv> mentions [sinceTS]`.
func (c *CommandClient) FetchMentions(ctx context.Context, sinceTS string) ([]Message, error) {
	return c.fetch(ctx, "mentions", sinceTS)
}

// FetchDMs runs `<argv> dms [sinceTS]`.
func (c *CommandClient) FetchDMs(ctx context.Context, sinceTS string) ([]Message, error) {
	return c.fetch(ctx, "dms", sinceTS)
}

// PostDM runs `<argv> post-dm <channelID>` with text piped on stdin.
func (c *CommandClient) PostDM(ctx context.Context, channelID, text string) error {
	if len(c.argv) == 0 {
		return ErrCommandNotConfigured
	}
	argv := append(append([]string(nil), c.argv...), "post-dm", channelID)
	if _, err := c.runner.run(ctx, text, argv); err != nil {
		return fmt.Errorf("slack command post-dm: %w", err)
	}
	return nil
}

// AuthStatus runs `<argv> auth-status` and maps stdout onto a source.AuthStatus.
// A run failure is reported as not-configured rather than an error so the
// daemon's startup probe degrades gracefully.
func (c *CommandClient) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	if len(c.argv) == 0 {
		return source.AuthNotConfigured, nil
	}
	out, err := c.runner.run(ctx, "", append(append([]string(nil), c.argv...), "auth-status"))
	if err != nil {
		return source.AuthNotConfigured, nil
	}
	switch strings.TrimSpace(string(out)) {
	case string(source.AuthExpired):
		return source.AuthExpired, nil
	case string(source.AuthRevoked):
		return source.AuthRevoked, nil
	case string(source.AuthNotConfigured):
		return source.AuthNotConfigured, nil
	default:
		// Anything else (including "authenticated" or empty) is treated as
		// ready: a command that ran cleanly is a configured adapter.
		return source.AuthAuthenticated, nil
	}
}

// Setup is a no-op: the command adapter owns its own auth out of band (e.g.
// the MCP server / agent it bridges to is configured separately).
func (c *CommandClient) Setup(context.Context, bool) error { return nil }

// execCmdRunner is the production cmdRunner backed by os/exec.
type execCmdRunner struct{}

func (execCmdRunner) run(ctx context.Context, stdin string, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, ErrCommandNotConfigured
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}
