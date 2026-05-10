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
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
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

// slackhogAPIMessage is the shape returned by slackhog's /_api/messages.
type slackhogAPIMessage struct {
	ID          string         `json:"id"`
	Channel     string         `json:"channel"`
	Text        string         `json:"text"`
	ReceivedAt  time.Time      `json:"received_at"`
	RawPayload  map[string]any `json:"raw_payload"`
}

// slackhogClient implements the slack.Client interface using slackhog's
// internal /_api/messages endpoint for FetchMentions / FetchDMs, and
// the standard chat.postMessage for PostDM. This is a test-only client
// that makes the full OODA loop verifiable without a real Slack workspace.
type slackhogClient struct {
	webAPI *WebAPIClient
}

func newSlackhogOODAClient(baseURL string) *slackhogClient {
	return &slackhogClient{webAPI: NewWebAPIClient(baseURL, "xoxb-slackhog-test")}
}

func (c *slackhogClient) fetchAll(ctx context.Context) ([]slackhogAPIMessage, error) {
	resp, err := http.Get(slackhogURL() + "/_api/messages")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Messages []slackhogAPIMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Messages, nil
}

// receivedAtToTS converts a time.Time to a Slack ts string (Unix.microseconds).
func receivedAtToTS(t time.Time) string {
	return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000)
}

func (c *slackhogClient) FetchMentions(ctx context.Context, sinceTS string) ([]Message, error) {
	all, err := c.fetchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("slackhogClient: FetchMentions: %w", err)
	}
	var out []Message
	for _, m := range all {
		ts := receivedAtToTS(m.ReceivedAt)
		if sinceTS != "" && compareTS(ts, sinceTS) <= 0 {
			continue
		}
		// Treat all non-DM channel messages as potential mentions.
		if strings.HasPrefix(m.Channel, "D") {
			continue
		}
		out = append(out, Message{
			ChannelID:   m.Channel,
			ChannelType: "channel",
			TS:          ts,
			Text:        m.Text,
		})
	}
	return out, nil
}

func (c *slackhogClient) FetchDMs(ctx context.Context, sinceTS string) ([]Message, error) {
	all, err := c.fetchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("slackhogClient: FetchDMs: %w", err)
	}
	var out []Message
	for _, m := range all {
		ts := receivedAtToTS(m.ReceivedAt)
		if sinceTS != "" && compareTS(ts, sinceTS) <= 0 {
			continue
		}
		if !strings.HasPrefix(m.Channel, "D") {
			continue
		}
		out = append(out, Message{
			ChannelID:   m.Channel,
			ChannelType: "im",
			TS:          ts,
			Text:        m.Text,
		})
	}
	return out, nil
}

func (c *slackhogClient) PostDM(ctx context.Context, channelID, text string) error {
	return c.webAPI.PostDM(ctx, channelID, text)
}

func (c *slackhogClient) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	return source.AuthAuthenticated, nil
}

func (c *slackhogClient) Setup(ctx context.Context, nonInteractive bool) error {
	return nil
}

// IT-OODA: Full Observe→Orient→Decide(queue) loop via slackhog.
//
// Flow:
//  1. POST a mention message to slackhog (Observe input)
//  2. POST a DM to slackhog (Observe input)
//  3. Plugin.List() discovers both (Observe output)
//  4. Each becomes a source.Task — ready to queue (Orient/Decide entry point)
func TestSlackhogOODADiscovery(t *testing.T) {
	waitSlackhog(t)
	clearSlackhogMessages(t)

	base := fmt.Sprintf("%d", time.Now().UnixNano())
	mentionCh := "C-general-" + base
	dmCh := "D-user1-" + base

	webAPI := NewWebAPIClient(slackhogURL(), "xoxb-ooda-test")

	// 1. Seed a mention (channel message)
	if err := webAPI.PostDM(context.Background(), mentionCh, "hey <@haruto> can you review this PR?"); err != nil {
		t.Fatalf("seed mention: %v", err)
	}
	// 2. Seed a DM
	if err := webAPI.PostDM(context.Background(), dmCh, "can you help me with the deploy?"); err != nil {
		t.Fatalf("seed DM: %v", err)
	}

	// 3. Discover via Plugin.List()
	client := newSlackhogOODAClient(slackhogURL())
	plugin := New(
		WithClient(client),
		WithIncludeMentions(true),
		WithIncludeDM(true),
	)

	tasks, err := plugin.List(context.Background())
	if err != nil {
		t.Fatalf("plugin.List: %v", err)
	}

	// 4. Assert both messages became tasks
	if len(tasks) != 2 {
		t.Fatalf("discovered %d tasks, want 2", len(tasks))
	}

	mentionTask := findTaskByChannel(tasks, mentionCh)
	dmTask := findTaskByChannel(tasks, dmCh)

	if mentionTask == nil {
		t.Errorf("mention task not found; tasks=%+v", tasks)
	} else {
		if mentionTask.Source != "slack" {
			t.Errorf("mention task Source = %q, want slack", mentionTask.Source)
		}
		if !strings.Contains(mentionTask.Title, "haruto") {
			t.Errorf("mention task Title = %q, should contain 'haruto'", mentionTask.Title)
		}
		if mentionTask.ExternalID == "" {
			t.Errorf("mention task ExternalID is empty")
		}
	}

	if dmTask == nil {
		t.Errorf("DM task not found; tasks=%+v", tasks)
	} else {
		meta, _ := dmTask.RawMetadata["channel_type"].(string)
		if meta != "im" {
			t.Errorf("DM task channel_type = %q, want im", meta)
		}
	}

	t.Logf("OODA Observe: %d tasks discovered from slackhog", len(tasks))
	for _, tk := range tasks {
		t.Logf("  source=%s external_id=%s title=%q", tk.Source, tk.ExternalID, tk.Title)
	}
}

func findTaskByChannel(tasks []source.Task, channelID string) *source.Task {
	for i := range tasks {
		if meta, ok := tasks[i].RawMetadata["channel_id"].(string); ok && meta == channelID {
			return &tasks[i]
		}
	}
	return nil
}

// IT-OODA-Queue: Slack messages → plugin.List() → store.Insert → DB confirmed.
// This is the full Observe phase: slackhog acts as the Slack source, the
// slackhogClient discovers messages, and they are persisted to a real SQLite
// queue — exactly what marunage loop does in production.
func TestSlackhogOODAFullQueue(t *testing.T) {
	waitSlackhog(t)
	clearSlackhogMessages(t)

	base := fmt.Sprintf("%d", time.Now().UnixNano())
	mentionCh := "C-eng-" + base
	dmCh := "D-pm-" + base

	webAPI := NewWebAPIClient(slackhogURL(), "xoxb-queue-test")
	if err := webAPI.PostDM(context.Background(), mentionCh, "please review PR #99 <@haruto>"); err != nil {
		t.Fatalf("seed mention: %v", err)
	}
	if err := webAPI.PostDM(context.Background(), dmCh, "can you deploy by EOD?"); err != nil {
		t.Fatalf("seed DM: %v", err)
	}

	// Discover via slackhogClient (uses /_api/messages, same logic as loop).
	client := newSlackhogOODAClient(slackhogURL())
	plugin := New(
		WithClient(client),
		WithIncludeMentions(true),
		WithIncludeDM(true),
	)
	tasks, err := plugin.List(context.Background())
	if err != nil {
		t.Fatalf("plugin.List: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("discovered %d tasks, want 2", len(tasks))
	}

	// Insert into a real SQLite queue (in-memory for isolation).
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	repo := store.NewTaskRepo(db)

	for _, tk := range tasks {
		notes := ""
		if len(tk.RawMetadata) > 0 {
			b, _ := json.Marshal(tk.RawMetadata)
			notes = string(b)
		}
		row := store.Task{
			Source:     tk.Source,
			ExternalID: tk.ExternalID,
			Title:      tk.Title,
			Body:       tk.Body,
			Notes:      notes,
		}
		if _, err := repo.Insert(context.Background(), row); err != nil {
			t.Fatalf("repo.Insert %q: %v", tk.ExternalID, err)
		}
	}

	// Verify tasks are in the DB as pending.
	rows, err := repo.List(context.Background(), store.ListFilter{
		Statuses: []string{"pending"},
	})
	if err != nil {
		t.Fatalf("repo.List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("DB has %d pending tasks, want 2", len(rows))
	}

	t.Logf("OODA full queue: %d tasks inserted into DB as pending", len(rows))
	for _, row := range rows {
		t.Logf("  #%d [%s] source=%s external_id=%s title=%q",
			row.ID, row.Status, row.Source, row.ExternalID, row.Title)
	}
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
