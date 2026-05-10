//go:build integration

package slack

// Integration tests against a real slackhog instance (docker compose up slackhog).
//
// Run with:
//
//	docker compose up -d slackhog
//	go test -tags integration -run TestSlackhog ./internal/source/slack/...
//
// SLACKHOG_URL defaults to http://localhost:4112 and can be overridden via
// the environment variable of the same name.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// slackhogURL returns the base URL of the slackhog instance to test against.
func slackhogURL() string {
	if u := os.Getenv("SLACKHOG_URL"); u != "" {
		return u
	}
	return "http://localhost:4112"
}

// slackhogMessage mirrors the shape /_api/messages returns.
type slackhogMessage struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

// listSlackhogMessages calls /_api/messages and returns all captured messages,
// optionally filtered by channel.
func listSlackhogMessages(t *testing.T, channel string) []slackhogMessage {
	t.Helper()
	url := slackhogURL() + "/_api/messages"
	if channel != "" {
		url += "?channel=" + channel
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET /_api/messages: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Messages []slackhogMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /_api/messages: %v", err)
	}
	return body.Messages
}

// clearSlackhogMessages calls DELETE /_api/messages to reset stored state.
func clearSlackhogMessages(t *testing.T) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, slackhogURL()+"/_api/messages", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /_api/messages: %v", err)
	}
	defer resp.Body.Close()
}

// waitSlackhog blocks until the slackhog server responds to a GET on
// /_api/messages, retrying for up to 5 seconds. Fails the test if the server
// never becomes available, giving a clear error instead of a confusing
// "connection refused" in the test body.
func waitSlackhog(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(slackhogURL() + "/_api/messages")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("slackhog at %s did not become available within 5s; run: docker compose up -d slackhog", slackhogURL())
}

// IT1: Plugin.Complete posts the documented notification text to the
// slackhog server and the message can be retrieved via /_api/messages.
func TestSlackhogPluginCompleteDeliversNotification(t *testing.T) {
	waitSlackhog(t)
	clearSlackhogMessages(t)

	channel := fmt.Sprintf("D-it1-%d", time.Now().UnixNano())
	client := NewWebAPIClient(slackhogURL(), "xoxb-integration-test")
	p := New(WithClient(client), WithNotifyChannelID(channel))

	if err := p.Complete(context.Background(), "42"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs := listSlackhogMessages(t, channel)
	if len(msgs) != 1 {
		t.Fatalf("slackhog stored %d message(s) for channel %q, want 1", len(msgs), channel)
	}
	want := fmt.Sprintf(notifyMessageFormat, "42")
	if msgs[0].Text != want {
		t.Errorf("text = %q, want %q", msgs[0].Text, want)
	}
	if msgs[0].Channel != channel {
		t.Errorf("channel = %q, want %q", msgs[0].Channel, channel)
	}
}

// IT2: WebAPIClient.PostDM delivers a raw message that slackhog stores
// verbatim — verifies the HTTP path end-to-end without Plugin indirection.
func TestSlackhogWebAPIClientPostDM(t *testing.T) {
	waitSlackhog(t)
	clearSlackhogMessages(t)

	channel := fmt.Sprintf("D-it2-%d", time.Now().UnixNano())
	client := NewWebAPIClient(slackhogURL(), "xoxb-raw")

	if err := client.PostDM(context.Background(), channel, "raw integration test"); err != nil {
		t.Fatalf("PostDM: %v", err)
	}

	msgs := listSlackhogMessages(t, channel)
	if len(msgs) != 1 {
		t.Fatalf("slackhog stored %d message(s), want 1", len(msgs))
	}
	if msgs[0].Text != "raw integration test" {
		t.Errorf("text = %q, want %q", msgs[0].Text, "raw integration test")
	}
}

// IT3: Multiple Complete calls each deliver exactly one message, with no
// cross-channel bleed. Verifies that channel isolation holds under the real
// HTTP server.
func TestSlackhogPluginCompleteChannelIsolation(t *testing.T) {
	waitSlackhog(t)
	clearSlackhogMessages(t)

	base := fmt.Sprintf("%d", time.Now().UnixNano())
	chA := "D-chA-" + base
	chB := "D-chB-" + base

	client := NewWebAPIClient(slackhogURL(), "xoxb-isolation")
	pA := New(WithClient(client), WithNotifyChannelID(chA))
	pB := New(WithClient(client), WithNotifyChannelID(chB))

	if err := pA.Complete(context.Background(), "1"); err != nil {
		t.Fatalf("pA.Complete: %v", err)
	}
	if err := pB.Complete(context.Background(), "2"); err != nil {
		t.Fatalf("pB.Complete: %v", err)
	}

	msgsA := listSlackhogMessages(t, chA)
	msgsB := listSlackhogMessages(t, chB)

	if len(msgsA) != 1 {
		t.Errorf("channel A: got %d message(s), want 1", len(msgsA))
	}
	if len(msgsB) != 1 {
		t.Errorf("channel B: got %d message(s), want 1", len(msgsB))
	}
	if len(msgsA) > 0 && msgsA[0].Channel != chA {
		t.Errorf("channel A message has channel=%q, want %q", msgsA[0].Channel, chA)
	}
	if len(msgsB) > 0 && msgsB[0].Channel != chB {
		t.Errorf("channel B message has channel=%q, want %q", msgsB[0].Channel, chB)
	}
}
