// webclient.go provides WebAPIClient, an HTTP-based implementation of
// Client backed by the Slack Web API (https://api.slack.com).
//
// In tests, pass a slackhog-compatible server URL as baseURL so tests run
// in-process without hitting the real Slack API. slackhog
// (https://github.com/harakeishi/slackhog) implements the same HTTP
// endpoints (POST /api/chat.postMessage, etc.) that WebAPIClient calls,
// making the test infrastructure directly usable for manual end-to-end
// verification with `go tool slackhog`.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/haruotsu/marunage/internal/source"
)

// ErrWebAPINotImplemented is returned by WebAPIClient methods whose
// Slack API counterpart is handled by the MCP-backed Client rather than
// a direct Web API call. Callers that need FetchMentions / FetchDMs
// should inject a Client built with WithClient(mcpClient) instead.
var ErrWebAPINotImplemented = errors.New("slack: not implemented via Slack Web API (use MCP-backed client)")

// WebAPIClient is an HTTP-based implementation of Client that posts to
// the Slack Web API. It currently covers PostDM (chat.postMessage) and
// AuthStatus; FetchMentions / FetchDMs return ErrWebAPINotImplemented
// because those operations are routed through the Slack MCP transport.
//
// Construct via NewWebAPIClient; functional options allow test injection.
type WebAPIClient struct {
	baseURL    string // default "https://slack.com"; override in tests
	token      string
	httpClient *http.Client
}

// WebAPIClientOption is the functional-option shape NewWebAPIClient accepts.
type WebAPIClientOption func(*WebAPIClient)

// WithHTTPClient overrides the *http.Client used for outbound requests.
// Useful in tests to inject a transport that adds latency or records calls.
func WithHTTPClient(c *http.Client) WebAPIClientOption {
	return func(w *WebAPIClient) { w.httpClient = c }
}

// NewWebAPIClient constructs a WebAPIClient with the given base URL and
// bearer token. In production pass baseURL="https://slack.com"; in tests
// pass a slackhog server URL (e.g. srv.Server.URL from newSlackhogServer).
func NewWebAPIClient(baseURL, token string, opts ...WebAPIClientOption) *WebAPIClient {
	c := &WebAPIClient{
		baseURL:    baseURL,
		token:      token,
		httpClient: http.DefaultClient,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// PostDM sends text to channelID by calling the Slack chat.postMessage
// endpoint. Compatible with slackhog's HandleChatPostMessage handler so
// tests pointed at a slackhog instance work without modification.
func (c *WebAPIClient) PostDM(ctx context.Context, channelID, text string) error {
	payload := map[string]string{
		"channel": channelID,
		"text":    text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack webclient: marshal PostDM payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack webclient: build PostDM request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack webclient: PostDM HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("slack webclient: decode PostDM response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack webclient: PostDM API error: %s", result.Error)
	}
	return nil
}

// FetchMentions is not available through the Slack Web API on this client.
// Mention discovery requires the MCP-backed Client injected via WithClient.
func (c *WebAPIClient) FetchMentions(_ context.Context, _ string) ([]Message, error) {
	return nil, ErrWebAPINotImplemented
}

// FetchDMs is not available through the Slack Web API on this client.
// DM discovery requires the MCP-backed Client injected via WithClient.
func (c *WebAPIClient) FetchDMs(_ context.Context, _ string) ([]Message, error) {
	return nil, ErrWebAPINotImplemented
}

// AuthStatus returns AuthAuthenticated when a non-empty token is
// configured, AuthNotConfigured otherwise.
func (c *WebAPIClient) AuthStatus(_ context.Context) (source.AuthStatus, error) {
	if c.token == "" {
		return source.AuthNotConfigured, nil
	}
	return source.AuthAuthenticated, nil
}

// Setup reads the Slack bearer token from the SLACK_TOKEN environment
// variable when nonInteractive is true. Interactive auth flow is handled
// by the `marunage setup --source slack` wizard outside this package.
func (c *WebAPIClient) Setup(_ context.Context, nonInteractive bool) error {
	if nonInteractive {
		if t := os.Getenv("SLACK_TOKEN"); t != "" {
			c.token = t
		}
	}
	return nil
}
