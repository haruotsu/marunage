package slack

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

type fakeCmdRunner struct {
	out      []byte
	err      error
	gotArgv  []string
	gotStdin string
}

func (f *fakeCmdRunner) run(_ context.Context, stdin string, argv []string) ([]byte, error) {
	f.gotArgv = argv
	f.gotStdin = stdin
	return f.out, f.err
}

func TestCommandClientFetchMentionsParsesAndPassesArgs(t *testing.T) {
	fr := &fakeCmdRunner{out: []byte(`[
	  {"channel_id":"C1","channel_type":"channel","ts":"170.1","user_id":"U1","text":"hi @me","permalink":"https://slack/x"}
	]`)}
	c := NewCommandClient([]string{"my-bridge", "--flag"}, withCmdRunner(fr))

	msgs, err := c.FetchMentions(context.Background(), "169.0")
	if err != nil {
		t.Fatalf("FetchMentions: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ChannelID != "C1" || msgs[0].Text != "hi @me" || msgs[0].Permalink != "https://slack/x" {
		t.Fatalf("msgs = %+v", msgs)
	}
	want := []string{"my-bridge", "--flag", "mentions", "169.0"}
	if !equalArgs(fr.gotArgv, want) {
		t.Errorf("argv = %v, want %v", fr.gotArgv, want)
	}
}

func TestCommandClientDMsOmitsEmptySince(t *testing.T) {
	fr := &fakeCmdRunner{out: []byte(`[]`)}
	c := NewCommandClient([]string{"bridge"}, withCmdRunner(fr))
	if _, err := c.FetchDMs(context.Background(), ""); err != nil {
		t.Fatalf("FetchDMs: %v", err)
	}
	want := []string{"bridge", "dms"}
	if !equalArgs(fr.gotArgv, want) {
		t.Errorf("argv = %v, want %v (no trailing empty since)", fr.gotArgv, want)
	}
}

func TestCommandClientEmptyOutputIsEmptySlice(t *testing.T) {
	fr := &fakeCmdRunner{out: []byte("   \n")}
	c := NewCommandClient([]string{"bridge"}, withCmdRunner(fr))
	msgs, err := c.FetchMentions(context.Background(), "")
	if err != nil || len(msgs) != 0 {
		t.Fatalf("msgs=%v err=%v", msgs, err)
	}
}

func TestCommandClientPostDMPipesStdin(t *testing.T) {
	fr := &fakeCmdRunner{}
	c := NewCommandClient([]string{"bridge"}, withCmdRunner(fr))
	if err := c.PostDM(context.Background(), "D9", "task #1 done"); err != nil {
		t.Fatalf("PostDM: %v", err)
	}
	if fr.gotStdin != "task #1 done" {
		t.Errorf("stdin = %q, want the message text", fr.gotStdin)
	}
	want := []string{"bridge", "post-dm", "D9"}
	if !equalArgs(fr.gotArgv, want) {
		t.Errorf("argv = %v, want %v", fr.gotArgv, want)
	}
}

func TestCommandClientAuthStatus(t *testing.T) {
	cases := map[string]source.AuthStatus{
		"authenticated":  source.AuthAuthenticated,
		"":               source.AuthAuthenticated,
		"expired":        source.AuthExpired,
		"revoked":        source.AuthRevoked,
		"not_configured": source.AuthNotConfigured,
	}
	for out, want := range cases {
		fr := &fakeCmdRunner{out: []byte(out)}
		c := NewCommandClient([]string{"bridge"}, withCmdRunner(fr))
		got, err := c.AuthStatus(context.Background())
		if err != nil || got != want {
			t.Errorf("AuthStatus(out=%q) = %q,%v; want %q", out, got, err, want)
		}
	}
}

func TestCommandClientAuthStatusRunErrorIsNotConfigured(t *testing.T) {
	fr := &fakeCmdRunner{err: errors.New("boom")}
	c := NewCommandClient([]string{"bridge"}, withCmdRunner(fr))
	got, err := c.AuthStatus(context.Background())
	if err != nil || got != source.AuthNotConfigured {
		t.Fatalf("got %q,%v; want not_configured,nil", got, err)
	}
}

func TestCommandClientEmptyArgv(t *testing.T) {
	c := NewCommandClient(nil)
	if _, err := c.FetchMentions(context.Background(), ""); !errors.Is(err, ErrCommandNotConfigured) {
		t.Fatalf("err = %v, want ErrCommandNotConfigured", err)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
