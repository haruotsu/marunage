package gmail

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestAdapterImplementsMandatoryInterface is the compile-time witness
// that the adapter satisfies source.Plugin. The var declaration is the
// assertion; if a method goes missing this file stops compiling.
func TestAdapterImplementsMandatoryInterface(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	var _ source.Plugin = a
	if a.Name() != "gmail" {
		t.Fatalf("Name() = %q, want gmail", a.Name())
	}
}

// TestAdapterImplementsExpectedOptionalCapabilities — the brief lists
// since + complete as the two optional capabilities; the adapter must
// satisfy exactly those two interfaces.
func TestAdapterImplementsExpectedOptionalCapabilities(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	if _, ok := any(a).(source.Sincer); !ok {
		t.Errorf("adapter must implement source.Sincer")
	}
	if _, ok := any(a).(source.Completer); !ok {
		t.Errorf("adapter must implement source.Completer")
	}
}

// TestAdapterDoesNotImplementAdderOrDeleter pins the read-mostly nature
// of the Gmail source: declaring those would invite the queue into
// territory where deletions are irrecoverable. If a future refactor
// accidentally adds an Add / Delete method, this test catches the drift.
func TestAdapterDoesNotImplementAdderOrDeleter(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	if _, ok := any(a).(source.Adder); ok {
		t.Errorf("adapter must NOT implement source.Adder")
	}
	if _, ok := any(a).(source.Deleter); ok {
		t.Errorf("adapter must NOT implement source.Deleter")
	}
}

// TestAdapterListConvertsThroughInner ensures the conversion from
// gmail.Message to source.Task is lossless for the fields downstream
// triage cares about. Without this the adapter could quietly drop, say,
// SourcePath and the web UI would lose its deep link.
func TestAdapterListConvertsThroughInner(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{
		{ID: "m1", ThreadID: "t1", Subject: "Hello", Snippet: "world", Labels: []string{"INBOX"}, From: "x@y"},
	}}
	a := NewAdapter(New(WithClient(fc)))

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Source != "gmail" || got[0].ExternalID != "m1" || got[0].Title != "Hello" || got[0].Body != "world" {
		t.Errorf("task = %+v", got[0])
	}
	if got[0].SourcePath == "" {
		t.Errorf("SourcePath empty")
	}
}

func TestAdapterAuthStatusForwards(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{authStatusFn: func(context.Context) (source.AuthStatus, error) {
		return "", ErrClientCredentialsExpired
	}}
	a := NewAdapter(New(WithClient(fc)))
	got, err := a.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthExpired {
		t.Errorf("got = %q", got)
	}
}

func TestAdapterSetupForwardsOptions(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{}
	a := NewAdapter(New(WithClient(fc)))
	if err := a.Setup(context.Background(), source.SetupOptions{NonInteractive: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if len(fc.authCalls) != 1 || !fc.authCalls[0].NonInteractive {
		t.Errorf("authCalls = %+v", fc.authCalls)
	}
}

func TestAdapterSinceForwards(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{{ID: "m1", Subject: "x"}}}
	a := NewAdapter(New(WithClient(fc)))
	got, err := a.Since(context.Background(), "ignored-by-inner")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 1 || got[0].Title != "x" {
		t.Fatalf("Since = %+v", got)
	}
}

// TestAdapterSinceDiscardsCheckpointArg pins the contract that the
// inbound checkpoint string from source.Sincer is dropped by the
// adapter — gmail.Plugin reads its own checkpoint key out of the
// injected Checkpointer instead. Without this, a future PR-71 caller
// supplying its own per-source checkpoint could silently bypass the
// plugin's stored state and re-process every message.
func TestAdapterSinceDiscardsCheckpointArg(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{
		{ID: "m3", Subject: "newest"},
		{ID: "m2", Subject: "older"},
	}}
	cp := newFakeCheckpointer()
	cp.values[DefaultCheckpointKey] = "m2" // anchor at m2; only m3 should come back
	a := NewAdapter(New(WithClient(fc), WithCheckpointer(cp)))

	got, err := a.Since(context.Background(), "this-string-should-be-ignored")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	// If the adapter had used the inbound argument as the checkpoint, no
	// message id matches "this-string-should-be-ignored" and the plugin
	// would return both m3 and m2. The stored "m2" value is what truly
	// governs the cutoff.
	if len(got) != 1 || got[0].ExternalID != "m3" {
		t.Errorf("Since = %+v; want only m3 (checkpoint should come from store, not argument)", got)
	}
}

func TestAdapterCompleteForwardsByExternalID(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{messages: []Message{{ID: "m1"}}}
	a := NewAdapter(New(WithClient(fc)))
	if err := a.Complete(context.Background(), "m1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(fc.modifyCalls) != 1 || fc.modifyCalls[0].ID != "m1" {
		t.Errorf("modifyCalls = %+v", fc.modifyCalls)
	}
}

func TestAdapterCompleteUnknownIDIsErrTaskNotFound(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{}
	a := NewAdapter(New(WithClient(fc)))
	if err := a.Complete(context.Background(), "ghost"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("err = %v", err)
	}
}
