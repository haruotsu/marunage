package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// slackhogServer is a slackhog-compatible (https://github.com/harakeishi/slackhog)
// in-process mock Slack API server for use in tests. It implements the same
// HTTP endpoints as slackhog (POST /api/chat.postMessage, etc.) so tests
// written against this helper can be pointed at a real slackhog instance by
// changing the base URL without rewriting the assertions.
type slackhogServer struct {
	Server   *httptest.Server
	mu       sync.Mutex
	messages []capturedMessage
}

// capturedMessage stores the fields the tests assert on from a received
// chat.postMessage call.
type capturedMessage struct {
	Channel string
	Text    string
	Token   string // Bearer token stripped of "Bearer " prefix
}

// newSlackhogServer starts a local slackhog-compatible mock and registers
// t.Cleanup to shut it down.
func newSlackhogServer(t *testing.T) *slackhogServer {
	t.Helper()
	s := &slackhogServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat.postMessage", s.handleChatPostMessage)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

// handleChatPostMessage parses a JSON or form-encoded chat.postMessage
// request (matching slackhog's parseRequest behaviour) and returns the
// same Slack-compatible {"ok": true, "channel": ..., "ts": ...} response.
func (s *slackhogServer) handleChatPostMessage(w http.ResponseWriter, r *http.Request) {
	var channel, text string
	ct := r.Header.Get("Content-Type")
	if len(ct) >= 16 && ct[:16] == "application/json" {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		channel = payload["channel"]
		text = payload["text"]
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		channel = r.FormValue("channel")
		text = r.FormValue("text")
	}

	tok := r.Header.Get("Authorization")
	if len(tok) > 7 && tok[:7] == "Bearer " {
		tok = tok[7:]
	}

	s.mu.Lock()
	s.messages = append(s.messages, capturedMessage{Channel: channel, Text: text, Token: tok})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"channel": channel,
		"ts":      "1700000000.000001",
	})
}

// Messages returns a snapshot of all received messages, safe to call
// from the test goroutine after the PostDM call completes.
func (s *slackhogServer) Messages() []capturedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedMessage(nil), s.messages...)
}

// WC1: PostDM POSTs to /api/chat.postMessage and the slackhog-compatible
// server stores the correct channel and text.
func TestWebAPIClientPostDMSendsToSlackhogServer(t *testing.T) {
	t.Parallel()
	srv := newSlackhogServer(t)
	client := NewWebAPIClient(srv.Server.URL, "xoxb-test")

	if err := client.PostDM(context.Background(), "D-notify", "タスク #42 done"); err != nil {
		t.Fatalf("PostDM: %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("captured %d message(s), want 1", len(msgs))
	}
	if msgs[0].Channel != "D-notify" {
		t.Errorf("channel = %q, want D-notify", msgs[0].Channel)
	}
	if msgs[0].Text != "タスク #42 done" {
		t.Errorf("text = %q, want タスク #42 done", msgs[0].Text)
	}
}

// WC2: PostDM includes an Authorization: Bearer <token> header.
func TestWebAPIClientPostDMIncludesBearerToken(t *testing.T) {
	t.Parallel()
	srv := newSlackhogServer(t)
	client := NewWebAPIClient(srv.Server.URL, "xoxb-my-secret")

	if err := client.PostDM(context.Background(), "D", "hi"); err != nil {
		t.Fatalf("PostDM: %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 || msgs[0].Token != "xoxb-my-secret" {
		t.Errorf("Authorization token = %q, want xoxb-my-secret", func() string {
			if len(msgs) > 0 {
				return msgs[0].Token
			}
			return "(no message)"
		}())
	}
}

// WC3: PostDM returns an error when the server responds with ok: false.
func TestWebAPIClientPostDMReturnsErrOnSlackAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "channel_not_found"})
	}))
	defer srv.Close()

	client := NewWebAPIClient(srv.URL, "token")
	err := client.PostDM(context.Background(), "bad-channel", "hi")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// WC4: PostDM returns an error when the HTTP request fails.
func TestWebAPIClientPostDMReturnsErrOnNetworkFailure(t *testing.T) {
	t.Parallel()
	// 127.0.0.1:1 is reserved and never has a listener — immediate refused.
	client := NewWebAPIClient("http://127.0.0.1:1", "token")
	err := client.PostDM(context.Background(), "D", "hi")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// WC5: AuthStatus returns AuthAuthenticated when a token is configured.
func TestWebAPIClientAuthStatusAuthenticated(t *testing.T) {
	t.Parallel()
	client := NewWebAPIClient("https://slack.com", "xoxb-token")
	got, err := client.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("AuthStatus = %q, want %q", got, source.AuthAuthenticated)
	}
}

// WC6: AuthStatus returns AuthNotConfigured when no token is set.
func TestWebAPIClientAuthStatusNotConfigured(t *testing.T) {
	t.Parallel()
	client := NewWebAPIClient("https://slack.com", "")
	got, err := client.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Errorf("AuthStatus = %q, want %q", got, source.AuthNotConfigured)
	}
}

// WC7: FetchMentions returns ErrNotImplemented — Mention discovery uses the
// MCP-backed Client, not the Web API client.
func TestWebAPIClientFetchMentionsReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	client := NewWebAPIClient("https://slack.com", "token")
	_, err := client.FetchMentions(context.Background(), "")
	if !errors.Is(err, ErrWebAPINotImplemented) {
		t.Fatalf("FetchMentions err = %v, want ErrWebAPINotImplemented", err)
	}
}

// WC8: FetchDMs returns ErrNotImplemented — DM discovery uses the MCP-backed
// Client, not the Web API client.
func TestWebAPIClientFetchDMsReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	client := NewWebAPIClient("https://slack.com", "token")
	_, err := client.FetchDMs(context.Background(), "")
	if !errors.Is(err, ErrWebAPINotImplemented) {
		t.Fatalf("FetchDMs err = %v, want ErrWebAPINotImplemented", err)
	}
}

// WC9: WebAPIClient implements the Client interface so it is assignable
// to production injection points.
func TestWebAPIClientImplementsClientInterface(t *testing.T) {
	t.Parallel()
	var _ Client = NewWebAPIClient("", "")
}

// WC10: Setup reads MARUNAGE_SLACK_TOKEN (not SLACK_TOKEN) when nonInteractive=true.
// Only MARUNAGE_SLACK_TOKEN is set; SLACK_TOKEN is explicitly absent.
// Current bug: code reads SLACK_TOKEN so token stays empty → test fails.
func TestWebAPIClientSetupReadsMARUNAGESlackToken(t *testing.T) {
	t.Setenv("MARUNAGE_SLACK_TOKEN", "xoxb-marunage-env")
	// SLACK_TOKEN intentionally not set (t.Setenv with empty would override to "")

	srv := newSlackhogServer(t)
	client := NewWebAPIClient(srv.Server.URL, "")
	if err := client.Setup(context.Background(), true); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := client.PostDM(context.Background(), "D", "ping"); err != nil {
		t.Fatalf("PostDM: %v", err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("captured %d message(s), want 1", len(msgs))
	}
	if msgs[0].Token != "xoxb-marunage-env" {
		t.Errorf("Authorization token = %q, want xoxb-marunage-env (from MARUNAGE_SLACK_TOKEN)", msgs[0].Token)
	}
}

// WC11: Setup does not overwrite an already-configured token.
func TestWebAPIClientSetupDoesNotOverwriteExistingToken(t *testing.T) {
	t.Setenv("MARUNAGE_SLACK_TOKEN", "xoxb-env-override")

	srv := newSlackhogServer(t)
	client := NewWebAPIClient(srv.Server.URL, "xoxb-original")
	if err := client.Setup(context.Background(), true); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := client.PostDM(context.Background(), "D", "hi"); err != nil {
		t.Fatalf("PostDM: %v", err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 || msgs[0].Token != "xoxb-original" {
		t.Errorf("token after Setup = %q, want xoxb-original (existing token must not be overwritten)",
			func() string {
				if len(msgs) > 0 {
					return msgs[0].Token
				}
				return "(no message)"
			}())
	}
}

// WC12: NewWebAPIClient must not share http.DefaultClient; it must use a
// dedicated client so that a misbehaving server cannot block other callers.
func TestWebAPIClientDoesNotShareDefaultHTTPClient(t *testing.T) {
	t.Parallel()
	c1 := NewWebAPIClient("https://slack.com", "tok1")
	c2 := NewWebAPIClient("https://slack.com", "tok2")
	// Each instance should have its own *http.Client, not the global default.
	if c1.httpClient == http.DefaultClient {
		t.Error("NewWebAPIClient returned a client sharing http.DefaultClient; want a dedicated instance")
	}
	if c2.httpClient == http.DefaultClient {
		t.Error("second NewWebAPIClient returned a client sharing http.DefaultClient")
	}
	if c1.httpClient == c2.httpClient {
		t.Error("two NewWebAPIClient calls share the same *http.Client instance")
	}
}
