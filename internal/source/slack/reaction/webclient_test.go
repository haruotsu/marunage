package reaction

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// reactionAPIServer mocks the Slack Web API endpoints the reaction WebAPIClient
// calls. Compatible with the real endpoints so the same assertions hold against
// a live Slack / slackhog instance by swapping the base URL.
type reactionAPIServer struct {
	Server   *httptest.Server
	posted   []map[string]string
	openedAs string
}

func newReactionAPIServer(t *testing.T) *reactionAPIServer {
	t.Helper()
	s := &reactionAPIServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/conversations.list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"ok": true,
			"channels": []map[string]any{
				{"id": "C1", "is_im": false},
				{"id": "D9", "is_im": true}, // DM: must be skipped
			},
		})
	})
	mux.HandleFunc("/api/conversations.history", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("channel") != "C1" {
			writeJSON(w, map[string]any{"ok": true, "messages": []any{}})
			return
		}
		writeJSON(w, map[string]any{
			"ok": true,
			"messages": []map[string]any{
				{
					"text": "ship the release",
					"ts":   "1700000001.0001",
					"reactions": []map[string]any{
						{"name": "todo", "users": []string{"U1", "U2"}, "count": 2},
						{"name": "eyes", "users": []string{"U3"}, "count": 1},
					},
				},
				{
					"text":      "no trigger here",
					"ts":        "1700000002.0002",
					"reactions": []map[string]any{{"name": "wave", "users": []string{"U4"}, "count": 1}},
				},
			},
		})
	})
	mux.HandleFunc("/api/chat.getPermalink", func(w http.ResponseWriter, r *http.Request) {
		ts := r.URL.Query().Get("message_ts")
		writeJSON(w, map[string]any{"ok": true, "permalink": "https://acme.slack.com/archives/C1/p" + ts})
	})
	mux.HandleFunc("/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.posted = append(s.posted, body)
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/conversations.open", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.openedAs = body["users"]
		writeJSON(w, map[string]any{"ok": true, "channel": map[string]any{"id": "D-" + body["users"]}})
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestWebAPIFetchReactionEventsFiltersAndExpandsUsers(t *testing.T) {
	s := newReactionAPIServer(t)
	c := NewWebAPIClient(s.Server.URL, "xoxb-test")

	events, err := c.FetchReactionEvents(context.Background(), []string{"todo"}, "")
	if err != nil {
		t.Fatalf("FetchReactionEvents: %v", err)
	}
	// One "todo"-reacted message with two reacting users -> two events. "eyes"
	// and "wave" are not configured and the DM channel is skipped.
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	byUser := map[string]ReactionEvent{}
	for _, e := range events {
		byUser[e.UserID] = e
		if e.Reaction != "todo" || e.ChannelID != "C1" || e.TS != "1700000001.0001" || e.Text != "ship the release" {
			t.Errorf("event = %+v", e)
		}
		if e.Permalink != "https://acme.slack.com/archives/C1/p1700000001.0001" {
			t.Errorf("permalink = %q", e.Permalink)
		}
	}
	if _, ok := byUser["U1"]; !ok {
		t.Errorf("missing event for U1: %+v", events)
	}
	if _, ok := byUser["U2"]; !ok {
		t.Errorf("missing event for U2: %+v", events)
	}
}

func TestWebAPIFetchReactionEventsRequiresToken(t *testing.T) {
	c := NewWebAPIClient("https://slack.com", "")
	_, err := c.FetchReactionEvents(context.Background(), []string{"todo"}, "")
	if !errors.Is(err, ErrWebAPINotImplemented) {
		t.Fatalf("err = %v, want ErrWebAPINotImplemented", err)
	}
}

func TestWebAPIPostDM(t *testing.T) {
	s := newReactionAPIServer(t)
	c := NewWebAPIClient(s.Server.URL, "xoxb-test")
	if err := c.PostDM(context.Background(), "D1", "task #1 done"); err != nil {
		t.Fatalf("PostDM: %v", err)
	}
	if len(s.posted) != 1 || s.posted[0]["channel"] != "D1" || s.posted[0]["text"] != "task #1 done" {
		t.Fatalf("posted = %+v", s.posted)
	}
}

func TestWebAPIOpenDM(t *testing.T) {
	s := newReactionAPIServer(t)
	c := NewWebAPIClient(s.Server.URL, "xoxb-test")
	ch, err := c.OpenDM(context.Background(), "U7")
	if err != nil {
		t.Fatalf("OpenDM: %v", err)
	}
	if ch != "D-U7" || s.openedAs != "U7" {
		t.Fatalf("ch=%q openedAs=%q", ch, s.openedAs)
	}
}

func TestWebAPIAuthStatus(t *testing.T) {
	if got, _ := NewWebAPIClient("", "tok").AuthStatus(context.Background()); got != source.AuthAuthenticated {
		t.Errorf("with token = %q; want authenticated", got)
	}
	if got, _ := NewWebAPIClient("", "").AuthStatus(context.Background()); got != source.AuthNotConfigured {
		t.Errorf("no token = %q; want not_configured", got)
	}
}

var _ Client = (*WebAPIClient)(nil)
