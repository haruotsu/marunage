package slack

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// G1 / G2.
func TestAdapterImplementsExpectedInterfaces(t *testing.T) {
	t.Parallel()
	a := NewAdapter(New())
	var _ source.Plugin = a
	if a.Name() != "slack" {
		t.Fatalf("Name() = %q", a.Name())
	}
	if _, ok := any(a).(source.Sincer); !ok {
		t.Errorf("Sincer not implemented")
	}
	if _, ok := any(a).(source.Completer); !ok {
		t.Errorf("Completer not implemented")
	}
}

// G3.
func TestAdapterDoesNotImplementOptionalWriteInterfaces(t *testing.T) {
	t.Parallel()
	a := NewAdapter(New())
	if _, ok := any(a).(source.Adder); ok {
		t.Errorf("Adder should NOT be implemented for slack")
	}
	if _, ok := any(a).(source.Deleter); ok {
		t.Errorf("Deleter should NOT be implemented for slack")
	}
}

// G4.
func TestAdapterListLiftsInnerTasks(t *testing.T) {
	t.Parallel()
	c := &fakeClient{
		dms: []Message{{ChannelID: "D", ChannelType: "im", TS: "1.0", Text: "hi"}},
	}
	a := NewAdapter(New(WithClient(c), WithIncludeDM(true)))
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Source != "slack" {
		t.Fatalf("unexpected: %+v", got)
	}
}

// G5.
func TestAdapterSinceForwardsCheckpointArg(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	a := NewAdapter(New(WithClient(c), WithIncludeMentions(true)))
	if _, err := a.Since(context.Background(), "1234.0"); err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(c.gotMentionsTS) != 1 || c.gotMentionsTS[0] != "1234.0" {
		t.Fatalf("forwarded sinceTS = %v", c.gotMentionsTS)
	}
}

// G6.
func TestAdapterCompleteForwardsExternalID(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	a := NewAdapter(New(WithClient(c), WithNotifyChannelID("D")))
	if err := a.Complete(context.Background(), "99"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(c.gotPostText) != 1 || c.gotPostText[0] != "タスク #99 done" {
		t.Fatalf("PostDM text = %v", c.gotPostText)
	}
}

// G7.
func TestAdapterAuthStatusForwards(t *testing.T) {
	t.Parallel()
	c := &fakeClient{auth: source.AuthAuthenticated}
	a := NewAdapter(New(WithClient(c)))
	got, err := a.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("AuthStatus = %q", got)
	}
}

// Adapter.Setup forwards opts (regression guard for the boolean translation).
func TestAdapterSetupForwardsOpts(t *testing.T) {
	t.Parallel()
	c := &fakeClient{}
	a := NewAdapter(New(WithClient(c)))
	if err := a.Setup(context.Background(), source.SetupOptions{NonInteractive: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if len(c.setupOpts) != 1 || !c.setupOpts[0] {
		t.Fatalf("setupOpts = %v", c.setupOpts)
	}
}

// Defensive guard for an obvious typo: nil inner Plugin should panic on
// construction so a caller cannot ship a half-built adapter.
func TestNewAdapterRejectsNil(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on NewAdapter(nil)")
		}
	}()
	_ = NewAdapter(nil)
}

// Adapter Complete propagates typed errors so the daemon's
// completion-notify path can branch on errors.Is.
func TestAdapterCompleteSurfacesErrInvalidTaskID(t *testing.T) {
	t.Parallel()
	a := NewAdapter(New(WithNotifyChannelID("D")))
	err := a.Complete(context.Background(), "")
	if !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("err = %v, want ErrInvalidTaskID", err)
	}
}
