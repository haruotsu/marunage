// webclient.go provides WebAPIClient, an HTTP-based Client backed by the Slack
// Web API (https://api.slack.com). It mirrors internal/source/slack.WebAPIClient
// (same conversations.list + conversations.history + chat.postMessage pattern,
// slackhog-compatible) but surfaces reaction events: it scans channel history
// for messages carrying one of the configured reactions and emits one
// ReactionEvent per reacting user.
package reaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	defaultBaseURL     = "https://slack.com"
)

// ErrWebAPINotImplemented is returned by methods that require a Slack token
// when none is configured. Set MARUNAGE_SLACK_TOKEN or pass a token to
// NewWebAPIClient to enable them.
var ErrWebAPINotImplemented = errors.New("slack/reaction: not implemented via Slack Web API (no token configured)")

// WebAPIClient implements Client against the Slack Web API. Construct via
// NewWebAPIClient; functional options allow test injection.
type WebAPIClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// WebAPIClientOption is the functional-option shape NewWebAPIClient accepts.
type WebAPIClientOption func(*WebAPIClient)

// WithHTTPClient overrides the *http.Client used for outbound requests.
func WithHTTPClient(c *http.Client) WebAPIClientOption {
	return func(w *WebAPIClient) { w.httpClient = c }
}

// NewWebAPIClient constructs a WebAPIClient. An empty baseURL defaults to
// https://slack.com; tests pass a slackhog/httptest server URL.
func NewWebAPIClient(baseURL, token string, opts ...WebAPIClientOption) *WebAPIClient {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
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

// FetchReactionEvents scans public + private channel history for messages
// carrying one of the configured reactions, emitting one ReactionEvent per
// reacting user. sinceTS is the Slack ts lower bound (exclusive); empty means
// no lower bound. Returns ErrWebAPINotImplemented when no token is configured.
func (c *WebAPIClient) FetchReactionEvents(ctx context.Context, reactions []string, sinceTS string) ([]ReactionEvent, error) {
	if c.token == "" {
		return nil, ErrWebAPINotImplemented
	}
	want := make(map[string]struct{}, len(reactions))
	for _, r := range reactions {
		want[r] = struct{}{}
	}
	channels, err := c.listChannels(ctx, "public_channel,private_channel")
	if err != nil {
		return nil, fmt.Errorf("slack/reaction webclient: list channels: %w", err)
	}
	var out []ReactionEvent
	for _, ch := range channels {
		msgs, err := c.fetchReactedHistory(ctx, ch, sinceTS)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			permalink := ""
			fetched := false
			for _, rx := range m.Reactions {
				if _, ok := want[rx.Name]; !ok {
					continue
				}
				// Resolve the permalink once per matched message (best-effort).
				if !fetched {
					permalink = c.permalink(ctx, ch, m.TS)
					fetched = true
				}
				for _, uid := range rx.Users {
					out = append(out, ReactionEvent{
						Reaction:  rx.Name,
						UserID:    uid,
						ChannelID: ch,
						TS:        m.TS,
						Text:      m.Text,
						Permalink: permalink,
					})
				}
			}
		}
	}
	return out, nil
}

// PostDM sends text to channelID via chat.postMessage.
func (c *WebAPIClient) PostDM(ctx context.Context, channelID, text string) error {
	body, err := json.Marshal(map[string]string{"channel": channelID, "text": text})
	if err != nil {
		return fmt.Errorf("slack/reaction webclient: marshal PostDM payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack/reaction webclient: build PostDM request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack/reaction webclient: PostDM HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack/reaction webclient: PostDM unexpected HTTP status %d", resp.StatusCode)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("slack/reaction webclient: decode PostDM response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack/reaction webclient: PostDM API error: %s", result.Error)
	}
	return nil
}

// OpenDM opens (or retrieves) the IM channel with userID via
// conversations.open and returns its channel id.
func (c *WebAPIClient) OpenDM(ctx context.Context, userID string) (string, error) {
	body, err := json.Marshal(map[string]string{"users": userID})
	if err != nil {
		return "", fmt.Errorf("slack/reaction webclient: marshal OpenDM payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/conversations.open", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("slack/reaction webclient: build OpenDM request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("slack/reaction webclient: OpenDM HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("slack/reaction webclient: decode OpenDM response: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("slack/reaction webclient: OpenDM API error: %s", result.Error)
	}
	return result.Channel.ID, nil
}

// AuthStatus reports AuthAuthenticated when a token is configured. Local-only
// (no round-trip); a revoked token still reports authenticated until a call
// fails.
func (c *WebAPIClient) AuthStatus(_ context.Context) (source.AuthStatus, error) {
	if c.token == "" {
		return source.AuthNotConfigured, nil
	}
	return source.AuthAuthenticated, nil
}

// Setup reads MARUNAGE_SLACK_TOKEN when nonInteractive and no token is set.
func (c *WebAPIClient) Setup(_ context.Context, nonInteractive bool) error {
	if nonInteractive && c.token == "" {
		if t := os.Getenv("MARUNAGE_SLACK_TOKEN"); t != "" {
			c.token = t
		}
	}
	return nil
}

// listChannels returns channel IDs of the given comma-separated types from
// conversations.list, excluding IM channels (reactions are scanned in
// public/private channels). Mirrors slack.WebAPIClient.listChannels.
func (c *WebAPIClient) listChannels(ctx context.Context, types string) ([]string, error) {
	u := c.baseURL + "/api/conversations.list?limit=200"
	if types != "" {
		u += "&types=" + types
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("slack/reaction webclient: build conversations.list request: %w", err)
	}
	c.authorize(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack/reaction webclient: conversations.list HTTP: %w", err)
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
		return nil, fmt.Errorf("slack/reaction webclient: decode conversations.list: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("slack/reaction webclient: conversations.list api error: %s", result.Error)
	}
	var ids []string
	for _, ch := range result.Channels {
		// Skip DM channels: reactions to DMs are out of scope; IM ids start "D".
		if ch.IsIM || strings.HasPrefix(ch.ID, "D") {
			continue
		}
		ids = append(ids, ch.ID)
	}
	return ids, nil
}

// reactedMessage is the subset of a conversations.history message we read.
type reactedMessage struct {
	Text      string
	TS        string
	Reactions []messageReaction
}

type messageReaction struct {
	Name  string   `json:"name"`
	Users []string `json:"users"`
	Count int      `json:"count"`
}

// fetchReactedHistory returns messages (with their reactions) for channelID
// newer than sinceTS. A non-2xx response is treated as empty (slackhog-friendly).
func (c *WebAPIClient) fetchReactedHistory(ctx context.Context, channelID, sinceTS string) ([]reactedMessage, error) {
	u := c.baseURL + "/api/conversations.history?channel=" + channelID + "&limit=50"
	if sinceTS != "" {
		u += "&oldest=" + sinceTS
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("slack/reaction webclient: build conversations.history request: %w", err)
	}
	c.authorize(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack/reaction webclient: conversations.history HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil // endpoint not supported by this server; skip silently
	}
	var result struct {
		OK       bool `json:"ok"`
		Messages []struct {
			Text      string            `json:"text"`
			TS        string            `json:"ts"`
			Reactions []messageReaction `json:"reactions"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("slack/reaction webclient: decode conversations.history: %w", err)
	}
	out := make([]reactedMessage, 0, len(result.Messages))
	for _, m := range result.Messages {
		out = append(out, reactedMessage{Text: m.Text, TS: m.TS, Reactions: m.Reactions})
	}
	return out, nil
}

// permalink resolves the slack.com/archives URL for a message via
// chat.getPermalink. Best-effort: any failure yields an empty string rather
// than aborting the fetch.
func (c *WebAPIClient) permalink(ctx context.Context, channelID, ts string) string {
	q := url.Values{}
	q.Set("channel", channelID)
	q.Set("message_ts", ts)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/chat.getPermalink?"+q.Encode(), nil)
	if err != nil {
		return ""
	}
	c.authorize(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var result struct {
		OK        bool   `json:"ok"`
		Permalink string `json:"permalink"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.OK {
		return ""
	}
	return result.Permalink
}

func (c *WebAPIClient) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}
