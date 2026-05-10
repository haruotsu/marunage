// webclient.go provides WebAPIClient, an HTTP-based implementation of
// Client backed by the Slack Web API (https://api.slack.com).
//
// In tests, pass a slackhog-compatible server URL as baseURL so tests run
// in-process without hitting the real Slack API. slackhog
// (https://github.com/harakeishi/slackhog) implements the same HTTP
// endpoints (POST /api/chat.postMessage, etc.) that WebAPIClient calls,
// making the test infrastructure directly usable for manual end-to-end
// verification with `docker compose up slackhog`.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

const defaultHTTPTimeout = 30 * time.Second

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
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack webclient: PostDM unexpected HTTP status %d", resp.StatusCode)
	}
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

// FetchMentions retrieves messages that mention the authenticated user via
// Slack's search.messages API. sinceTS is a Slack ts string used as the
// `oldest` lower bound; empty means no lower bound.
// Returns ErrWebAPINotImplemented when no token is configured.
func (c *WebAPIClient) FetchMentions(ctx context.Context, sinceTS string) ([]Message, error) {
	if c.token == "" {
		return nil, ErrWebAPINotImplemented
	}
	u := c.baseURL + "/api/search.messages?query=mention%3Ame&sort=timestamp&count=50"
	if sinceTS != "" {
		u += "&oldest=" + sinceTS
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("slack webclient: build FetchMentions request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack webclient: FetchMentions HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		OK       bool `json:"ok"`
		Messages struct {
			Matches []struct {
				Text      string `json:"text"`
				TS        string `json:"ts"`
				Permalink string `json:"permalink"`
				Username  string `json:"username"`
				Channel   struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"channel"`
			} `json:"matches"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("slack webclient: decode FetchMentions response: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("slack webclient: FetchMentions API returned ok=false")
	}
	out := make([]Message, 0, len(result.Messages.Matches))
	for _, m := range result.Messages.Matches {
		out = append(out, Message{
			ChannelID:   m.Channel.ID,
			ChannelType: "channel",
			TS:          m.TS,
			UserID:      m.Username,
			Text:        m.Text,
			Permalink:   m.Permalink,
		})
	}
	return out, nil
}

// FetchDMs retrieves direct messages via conversations.list (type=im) then
// conversations.history for each DM channel. sinceTS is the Slack ts lower
// bound (exclusive); empty means no lower bound.
// Returns ErrWebAPINotImplemented when no token is configured.
func (c *WebAPIClient) FetchDMs(ctx context.Context, sinceTS string) ([]Message, error) {
	if c.token == "" {
		return nil, ErrWebAPINotImplemented
	}
	channels, err := c.listIMChannels(ctx)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, ch := range channels {
		msgs, err := c.fetchHistory(ctx, ch, sinceTS)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
	}
	return out, nil
}

// listIMChannels returns channel IDs whose is_im flag is true.
func (c *WebAPIClient) listIMChannels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/conversations.list?limit=200", nil)
	if err != nil {
		return nil, fmt.Errorf("slack webclient: build conversations.list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack webclient: conversations.list HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		OK       bool `json:"ok"`
		Channels []struct {
			ID   string `json:"id"`
			IsIM bool   `json:"is_im"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("slack webclient: decode conversations.list: %w", err)
	}
	var ids []string
	for _, ch := range result.Channels {
		if ch.IsIM {
			ids = append(ids, ch.ID)
		}
	}
	return ids, nil
}

// fetchHistory returns messages for channelID newer than sinceTS.
func (c *WebAPIClient) fetchHistory(ctx context.Context, channelID, sinceTS string) ([]Message, error) {
	u := c.baseURL + "/api/conversations.history?channel=" + channelID + "&limit=50"
	if sinceTS != "" {
		u += "&oldest=" + sinceTS
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("slack webclient: build conversations.history request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack webclient: conversations.history HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		OK       bool `json:"ok"`
		Messages []struct {
			Text    string `json:"text"`
			TS      string `json:"ts"`
			User    string `json:"user"`
			Channel string `json:"channel"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("slack webclient: decode conversations.history: %w", err)
	}
	out := make([]Message, 0, len(result.Messages))
	for _, m := range result.Messages {
		ch := m.Channel
		if ch == "" {
			ch = channelID
		}
		out = append(out, Message{
			ChannelID:   ch,
			ChannelType: "im",
			TS:          m.TS,
			UserID:      m.User,
			Text:        m.Text,
		})
	}
	return out, nil
}

// AuthStatus returns AuthAuthenticated when a non-empty token is configured,
// AuthNotConfigured otherwise. Note: this check is local only — it does not
// verify token validity against the Slack API (no round-trip). A revoked or
// expired token will still report AuthAuthenticated until a PostDM fails.
func (c *WebAPIClient) AuthStatus(_ context.Context) (source.AuthStatus, error) {
	if c.token == "" {
		return source.AuthNotConfigured, nil
	}
	return source.AuthAuthenticated, nil
}

// Setup reads the Slack bearer token from the MARUNAGE_SLACK_TOKEN environment
// variable when nonInteractive is true and no token is already configured.
// Interactive auth flow is handled by the `marunage setup --source slack`
// wizard outside this package.
func (c *WebAPIClient) Setup(_ context.Context, nonInteractive bool) error {
	if nonInteractive && c.token == "" {
		if t := os.Getenv("MARUNAGE_SLACK_TOKEN"); t != "" {
			c.token = t
		}
	}
	return nil
}
