//go:build integration

package slack

import (
	"context"
	"fmt"
	"testing"
)

// TestWebAPIClientAgainstSlackhogV023 verifies that WebAPIClient.FetchMentions
// and FetchDMs work against slackhog v0.2.3's conversations.history endpoint directly.
func TestWebAPIClientAgainstSlackhogV023(t *testing.T) {
	waitSlackhog(t)
	clearSlackhogMessages(t)

	base := slackhogURL()
	token := "xoxb-webclient-test"
	ctx := context.Background()

	// Seed messages via PostDM (same path WebAPIClient uses in production)
	webAPI := NewWebAPIClient(base, token)

	channelCh := fmt.Sprintf("C-general-%d", 12345)
	channelDM := fmt.Sprintf("D-user1-%d", 12345)

	if err := webAPI.PostDM(ctx, channelCh, "please review this PR <@haruto>"); err != nil {
		t.Fatalf("PostDM mention: %v", err)
	}
	if err := webAPI.PostDM(ctx, channelDM, "can you deploy by EOD?"); err != nil {
		t.Fatalf("PostDM DM: %v", err)
	}

	// FetchMentions: conversations.list(types=public_channel,private_channel) + conversations.history
	mentions, err := webAPI.FetchMentions(ctx, "")
	if err != nil {
		t.Fatalf("FetchMentions: %v", err)
	}
	t.Logf("FetchMentions: %d messages", len(mentions))
	for _, m := range mentions {
		t.Logf("  [%s] %s: %q", m.ChannelType, m.ChannelID, m.Text)
	}
	if len(mentions) != 1 {
		t.Errorf("FetchMentions: got %d, want 1", len(mentions))
	} else if mentions[0].ChannelType != "channel" {
		t.Errorf("ChannelType = %q, want channel", mentions[0].ChannelType)
	}

	// FetchDMs: conversations.list(types=im) + conversations.history
	dms, err := webAPI.FetchDMs(ctx, "")
	if err != nil {
		t.Fatalf("FetchDMs: %v", err)
	}
	t.Logf("FetchDMs: %d messages", len(dms))
	for _, m := range dms {
		t.Logf("  [%s] %s: %q", m.ChannelType, m.ChannelID, m.Text)
	}
	if len(dms) != 1 {
		t.Errorf("FetchDMs: got %d, want 1", len(dms))
	} else if dms[0].ChannelType != "im" {
		t.Errorf("ChannelType = %q, want im", dms[0].ChannelType)
	}
}
