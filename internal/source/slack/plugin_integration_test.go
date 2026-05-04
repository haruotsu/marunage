package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// PI1 + PI2: Plugin.Complete wired through WebAPIClient reaches the
// slackhog-compatible mock and delivers the expected notification text.
func TestPluginCompleteViaWebAPIClientReachesSlackhog(t *testing.T) {
	t.Parallel()
	srv := newSlackhogServer(t)
	client := NewWebAPIClient(srv.Server.URL, "xoxb-test")
	p := New(WithClient(client), WithNotifyChannelID("D-target"))

	if err := p.Complete(context.Background(), "7"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("slackhog captured %d message(s), want 1", len(msgs))
	}
	if msgs[0].Channel != "D-target" {
		t.Errorf("channel = %q, want D-target", msgs[0].Channel)
	}
	// PI2: notification text follows the documented notifyMessageFormat.
	if !strings.Contains(msgs[0].Text, "#7") || !strings.Contains(msgs[0].Text, "done") {
		t.Errorf("notification text = %q, want contains #7 and done", msgs[0].Text)
	}
}

// PI3: Plugin.Complete propagates the error when the slackhog-compatible
// server responds with ok:false (e.g. invalid_auth, channel_not_found).
func TestPluginCompleteViaWebAPIClientPropagatesServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
	}))
	defer srv.Close()

	client := NewWebAPIClient(srv.URL, "bad-token")
	p := New(WithClient(client), WithNotifyChannelID("D"))

	err := p.Complete(context.Background(), "1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// PI4: Adapter.Complete (the source.Completer surface) follows the same
// path through WebAPIClient and reaches the slackhog-compatible mock.
func TestAdapterCompleteViaWebAPIClientReachesSlackhog(t *testing.T) {
	t.Parallel()
	srv := newSlackhogServer(t)
	client := NewWebAPIClient(srv.Server.URL, "xoxb-test")
	a := NewAdapter(New(WithClient(client), WithNotifyChannelID("D-adapter")))

	if err := a.Complete(context.Background(), "99"); err != nil {
		t.Fatalf("Adapter.Complete: %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("slackhog captured %d message(s), want 1", len(msgs))
	}
	if msgs[0].Channel != "D-adapter" {
		t.Errorf("channel = %q, want D-adapter", msgs[0].Channel)
	}
	if !strings.Contains(msgs[0].Text, "#99") {
		t.Errorf("text = %q, want contains #99", msgs[0].Text)
	}
}

// PI5: Plugin.Complete with an empty externalID does not reach the mock
// at all — ErrInvalidTaskID is returned before any HTTP call is made.
func TestPluginCompleteEmptyIDNeverCallsSlackhog(t *testing.T) {
	t.Parallel()
	srv := newSlackhogServer(t)
	client := NewWebAPIClient(srv.Server.URL, "xoxb-test")
	p := New(WithClient(client), WithNotifyChannelID("D"))

	err := p.Complete(context.Background(), "")
	if !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("err = %v, want ErrInvalidTaskID", err)
	}
	if len(srv.Messages()) != 0 {
		t.Errorf("slackhog received a message despite empty id")
	}
}
