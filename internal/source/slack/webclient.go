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
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

const defaultHTTPTimeout = 30 * time.Second

// ErrWebAPINotImplemented is returned by WebAPIClient methods that require
// a Slack token but none is configured. Callers should set MARUNAGE_SLACK_TOKEN
// or pass WithClient(NewWebAPIClient(baseURL, token)) to enable these methods.
var ErrWebAPINotImplemented = errors.New("slack: not implemented via Slack Web API (use MCP-backed client)")

// WebAPIClient is an HTTP-based implementation of Client backed by the Slack
// Web API. When a token is provided it handles PostDM, AuthStatus,
// FetchMentions, and FetchDMs directly via conversations.list +
// conversations.history. When no token is configured, FetchMentions and
// FetchDMs return ErrWebAPINotImplemented.
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

// FetchMentions retrieves messages from monitored channels via
// conversations.history and returns all of them as potential mentions.
// In production, callers should configure triage to filter for actual
// @mentions; here we surface all channel messages and let Orient decide.
// sinceTS is the Slack ts lower bound (exclusive); empty means no lower bound.
// Returns ErrWebAPINotImplemented when no token is configured.
func (c *WebAPIClient) FetchMentions(ctx context.Context, sinceTS string) ([]Message, error) {
	if c.token == "" {
		return nil, ErrWebAPINotImplemented
	}
	// List all channels (public + private, not IM) the user is a member of.
	channels, err := c.listChannels(ctx, "public_channel,private_channel")
	if err != nil {
		return nil, fmt.Errorf("slack webclient: FetchMentions list channels: %w", err)
	}
	var out []Message
	for _, ch := range channels {
		msgs, err := c.fetchHistory(ctx, ch, sinceTS)
		if err != nil {
			return nil, err
		}
		for i := range msgs {
			msgs[i].ChannelType = "channel"
		}
		out = append(out, msgs...)
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
	channels, err := c.listChannels(ctx, "im")
	if err != nil {
		return nil, fmt.Errorf("slack webclient: FetchDMs list channels: %w", err)
	}
	var out []Message
	for _, ch := range channels {
		msgs, err := c.fetchHistory(ctx, ch, sinceTS)
		if err != nil {
			return nil, err
		}
		for i := range msgs {
			msgs[i].ChannelType = "im"
		}
		out = append(out, msgs...)
	}
	return out, nil
}

// listChannels returns channel IDs of the given types from conversations.list.
// types is a comma-separated Slack channel type string, e.g. "im" or
// "public_channel,private_channel".
func (c *WebAPIClient) listChannels(ctx context.Context, types string) ([]string, error) {
	u := c.baseURL + "/api/conversations.list?limit=200"
	if types != "" {
		u += "&types=" + types
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Channels []struct {
			ID   string `json:"id"`
			IsIM bool   `json:"is_im"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("slack webclient: decode conversations.list: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("slack webclient: conversations.list api error: %s", result.Error)
	}
	// Real Slack DM channels always start with "D"; public/private channels
	// start with "C" or "G". Use the ID prefix as a fallback when is_im is
	// not set correctly (e.g. slackhog sets is_im=false for all channels).
	wantIM := types == "im"
	var ids []string
	for _, ch := range result.Channels {
		isDM := ch.IsIM || strings.HasPrefix(ch.ID, "D")
		if wantIM && !isDM {
			continue
		}
		if !wantIM && isDM {
			continue
		}
		ids = append(ids, ch.ID)
	}
	return ids, nil
}

// fetchHistory returns messages for channelID newer than sinceTS.
// A non-2xx response (e.g. 404 from servers that do not implement
// conversations.history) is treated as an empty result rather than an
// error so a partial server (slackhog in test) does not block all channels.
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil // endpoint not supported by this server; skip silently
	}

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
			ChannelID: ch,
			TS:        m.TS,
			UserID:    m.User,
			Text:      m.Text,
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
