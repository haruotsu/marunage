package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/haruotsu/marunage/internal/source"
)

// Runner is the shell-out function shape. Tests inject a scripted
// runner; production wires DefaultRunner via exec.CommandContext.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultRunner executes the binary via os/exec and returns stdout.
// Stderr is discarded to prevent OAuth tokens or PII from leaking into
// logs via (*exec.ExitError).Stderr.
func DefaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%s: %w (exit code %d)", name, err, exitErr.ExitCode())
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// GWSClient implements Client by shelling out to the `gws` binary.
type GWSClient struct {
	binary        string
	newerThanDays int
	runner        Runner
}

// GWSOption is the functional-option shape NewGWSClient accepts.
type GWSOption func(*GWSClient)

// WithGWSBinary overrides the path to the gws binary. Defaults to "gws".
func WithGWSBinary(path string) GWSOption {
	return func(c *GWSClient) { c.binary = path }
}

// WithNewerThan limits discovery to messages newer than n days by
// appending "newer_than:Nd" to the query. 0 means no time filter.
func WithNewerThan(days int) GWSOption {
	return func(c *GWSClient) { c.newerThanDays = days }
}

// WithGWSRunner overrides the binary executor for testing.
func WithGWSRunner(r Runner) GWSOption {
	return func(c *GWSClient) { c.runner = r }
}

// NewGWSClient constructs a GWSClient with sensible defaults.
func NewGWSClient(opts ...GWSOption) *GWSClient {
	c := &GWSClient{
		binary: "gws",
		runner: DefaultRunner,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// List implements Client.List. It calls messages.list to get IDs then
// messages.get (format=metadata) for each to fetch subject, snippet,
// labels, and from. The N+1 is bounded by maxResults in the list call
// and is acceptable given the narrow unread-mail query.
func (c *GWSClient) List(ctx context.Context, query string) ([]Message, error) {
	q := query
	if c.newerThanDays > 0 {
		q = fmt.Sprintf("%s newer_than:%dd", q, c.newerThanDays)
	}

	listParams := map[string]any{
		"userId": "me",
		"q":      q,
	}
	listJSON, _ := json.Marshal(listParams)
	out, err := c.runner(ctx, c.binary, "gmail", "users", "messages", "list",
		"--params", string(listJSON), "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("gmail gws: messages.list: %w", err)
	}

	var listResp gwsMessageListResponse
	if err := json.Unmarshal(out, &listResp); err != nil {
		return nil, fmt.Errorf("gmail gws: decode messages.list: %w", err)
	}
	if len(listResp.Messages) == 0 {
		return nil, nil
	}

	msgs := make([]Message, 0, len(listResp.Messages))
	for _, stub := range listResp.Messages {
		getParams := map[string]any{
			"userId":          "me",
			"id":              stub.ID,
			"format":          "metadata",
			"metadataHeaders": []string{"Subject", "From"},
		}
		getJSON, _ := json.Marshal(getParams)
		out, err := c.runner(ctx, c.binary, "gmail", "users", "messages", "get",
			"--params", string(getJSON), "--format", "json")
		if err != nil {
			return nil, fmt.Errorf("gmail gws: messages.get %s: %w", stub.ID, err)
		}
		var getResp gwsMessageGetResponse
		if err := json.Unmarshal(out, &getResp); err != nil {
			return nil, fmt.Errorf("gmail gws: decode messages.get %s: %w", stub.ID, err)
		}
		msgs = append(msgs, getResp.toMessage())
	}
	return msgs, nil
}

// ModifyLabels implements Client.ModifyLabels via messages.modify.
func (c *GWSClient) ModifyLabels(ctx context.Context, id string, req ModifyLabelsRequest) error {
	params := map[string]any{"userId": "me", "id": id}
	paramsJSON, _ := json.Marshal(params)

	body := map[string]any{
		"addLabelIds":    req.AddLabels,
		"removeLabelIds": req.RemoveLabels,
	}
	bodyJSON, _ := json.Marshal(body)

	_, err := c.runner(ctx, c.binary, "gmail", "users", "messages", "modify",
		"--params", string(paramsJSON),
		"--json", string(bodyJSON),
		"--format", "json")
	if err != nil {
		return fmt.Errorf("gmail gws: messages.modify %s: %w", id, err)
	}
	return nil
}

// AuthStatus runs a cheap probe to verify gws credentials are valid.
// Any runner error is treated as AuthNotConfigured rather than a hard
// error; callers that need the raw failure should use Authenticate.
func (c *GWSClient) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	if err := c.probe(ctx); err != nil {
		return source.AuthNotConfigured, nil
	}
	return source.AuthAuthenticated, nil
}

// Authenticate implements Client.Authenticate. Non-interactive callers
// get a typed error since gws auth login requires a browser. Interactive
// callers get the probe result surfaced verbatim so "gws not on PATH"
// and "token missing" are distinguishable.
func (c *GWSClient) Authenticate(ctx context.Context, opts source.SetupOptions) error {
	if opts.NonInteractive {
		return fmt.Errorf("gmail: gws auth must already be configured (run `gws auth login` separately; non-interactive Setup cannot launch a browser flow)")
	}
	if err := c.probe(ctx); err != nil {
		return fmt.Errorf("gmail: gws smoke test failed (run `gws auth login` and verify gws is on PATH): %w", err)
	}
	return nil
}

// probe calls users.getProfile — the cheapest authenticated Gmail
// endpoint — and returns the runner error verbatim. Shared between
// AuthStatus (which downgrades errors to AuthNotConfigured) and
// Authenticate (which surfaces them).
func (c *GWSClient) probe(ctx context.Context) error {
	params := map[string]any{"userId": "me"}
	paramsJSON, _ := json.Marshal(params)
	_, err := c.runner(ctx, c.binary, "gmail", "users", "getProfile",
		"--params", string(paramsJSON), "--format", "json")
	return err
}

// gwsMessageListResponse is the messages.list wire shape.
type gwsMessageListResponse struct {
	Messages []struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"messages"`
}

// gwsMessageGetResponse is the messages.get wire shape for format=metadata.
type gwsMessageGetResponse struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId"`
	LabelIDs []string `json:"labelIds"`
	Snippet  string   `json:"snippet"`
	Payload  struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
	} `json:"payload"`
}

func (g gwsMessageGetResponse) toMessage() Message {
	m := Message{
		ID:       g.ID,
		ThreadID: g.ThreadID,
		Snippet:  g.Snippet,
		Labels:   g.LabelIDs,
	}
	for _, h := range g.Payload.Headers {
		switch h.Name {
		case "Subject":
			m.Subject = h.Value
		case "From":
			m.From = h.Value
		}
	}
	return m
}
