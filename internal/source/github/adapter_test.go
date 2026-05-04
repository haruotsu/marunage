package github

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestAdapterImplementsPluginAndOptionalCapabilities is the compile-time
// witness that the adapter satisfies the mandatory contract plus the two
// optional sub-interfaces (Sincer, Completer) PR-83 promises. The var
// declaration is the assertion; if a method goes missing this file stops
// compiling.
func TestAdapterImplementsPluginAndOptionalCapabilities(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	var _ source.Plugin = a
	if _, ok := any(a).(source.Sincer); !ok {
		t.Errorf("adapter must implement source.Sincer")
	}
	if _, ok := any(a).(source.Completer); !ok {
		t.Errorf("adapter must implement source.Completer")
	}
	if a.Name() != "github" {
		t.Fatalf("Name() = %q, want github", a.Name())
	}
}

// TestAdapterListForwardsToInner ensures the adapter's List method round-
// trips through the inner Plugin so any future change in the Plugin's
// behaviour is visible through the adapter without an additional layer of
// translation.
func TestAdapterListForwardsToInner(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: issueJSON},
		{stdout: "[]"},
	}}
	a := NewAdapter(New(WithRunner(fr)))
	tasks, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ExternalID != "owner/repo#12" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestAdapterSinceForwardsCheckpoint(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: "[]"},
		{stdout: "[]"},
	}}
	a := NewAdapter(New(WithRunner(fr)))
	if _, err := a.Since(context.Background(), "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("Since: %v", err)
	}
	for _, c := range fr.calls {
		if c.Args[2] != "is:open assignee:@me updated:>=2026-01-01T00:00:00Z" {
			t.Errorf("query = %q", c.Args[2])
		}
	}
}

func TestAdapterCompleteForwards(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{{}}}
	a := NewAdapter(New(WithRunner(fr)))
	if err := a.Complete(context.Background(), "owner/repo#9"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("calls = %+v", fr.calls)
	}
}

func TestAdapterCompleteSurfacesInvalidID(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	a := NewAdapter(New(WithRunner(fr)))
	err := a.Complete(context.Background(), "garbage")
	if !errors.Is(err, ErrInvalidExternalID) {
		t.Fatalf("err = %v, want ErrInvalidExternalID", err)
	}
}
