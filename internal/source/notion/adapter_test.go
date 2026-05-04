package notion

import (
	"context"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestAdapterImplementsPluginInterface is the compile-time witness that the
// adapter satisfies the mandatory contract. The var declaration is the
// assertion; if a method goes missing this file stops compiling.
func TestAdapterImplementsPluginInterface(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	var _ source.Plugin = a
	if a.Name() != pluginName {
		t.Fatalf("Name() = %q, want %q", a.Name(), pluginName)
	}
}

// TestAdapterImplementsOptionalCapabilities asserts the adapter opts into the
// optional sub-interfaces declared in plugin.toml. The registry validator
// (source.ValidateAgainstManifest) checks both directions; if this assertion
// breaks, RegisterBuiltin fails at startup.
func TestAdapterImplementsOptionalCapabilities(t *testing.T) {
	t.Parallel()

	a := NewAdapter(New())
	if _, ok := any(a).(source.Sincer); !ok {
		t.Errorf("adapter must implement source.Sincer")
	}
	if _, ok := any(a).(source.Adder); !ok {
		t.Errorf("adapter must implement source.Adder")
	}
	if _, ok := any(a).(source.Completer); !ok {
		t.Errorf("adapter must implement source.Completer")
	}
	if _, ok := any(a).(source.Deleter); !ok {
		t.Errorf("adapter must implement source.Deleter")
	}
}

// TestAdapterListLiftsTaskFields ensures the markdown.Task → source.Task
// conversion preserves the fields downstream materialisation needs:
// ExternalID, Title, Done, SourcePath, and the RawMetadata bag carrying
// last_edited_time + database_id provenance.
func TestAdapterListLiftsTaskFields(t *testing.T) {
	t.Parallel()

	c := &fakeClient{pages: []Page{
		{ID: "uuid-1", Title: "alpha", URL: "https://notion.so/alpha", LastEditedTime: "2025-01-02T00:00:00.000Z"},
		{ID: "uuid-2", Title: "beta", Archived: true},
	}}
	a := NewAdapter(New(WithClient(c), WithDatabaseID("db-1")))

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	for i, tk := range got {
		if tk.Source != pluginName {
			t.Errorf("task[%d].Source = %q", i, tk.Source)
		}
	}
	if got[0].ExternalID != "uuid-1" || got[0].Title != "alpha" {
		t.Errorf("task[0] = %+v", got[0])
	}
	if got[0].SourcePath != "https://notion.so/alpha" {
		t.Errorf("task[0].SourcePath = %q", got[0].SourcePath)
	}
	if got[0].RawMetadata["last_edited_time"] != "2025-01-02T00:00:00.000Z" {
		t.Errorf("task[0].RawMetadata last_edited_time = %v", got[0].RawMetadata)
	}
	if got[0].RawMetadata["database_id"] != "db-1" {
		t.Errorf("task[0].RawMetadata database_id = %v", got[0].RawMetadata)
	}
	if !got[1].Done {
		t.Errorf("task[1].Done = false (archived page should be done)")
	}
}

// TestAdapterAuthStatusForwardsToInner — the adapter is a thin shim and must
// not invent AuthStatus values of its own. This checks the forwarding for
// the "configured + smoke ok" branch; the inner-side test exercises the
// other branches.
func TestAdapterAuthStatusForwardsToInner(t *testing.T) {
	t.Parallel()

	s := newMemSecrets()
	_ = s.Set(defaultSecretName, "tok")
	a := NewAdapter(New(WithClient(&fakeClient{}), WithDatabaseID("db"), WithSecrets(s)))

	got, err := a.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("got %q, want %q", got, source.AuthAuthenticated)
	}
}

// TestAdapterSetupForwardsOptions — opts.NonInteractive must reach the inner
// Plugin's Setup so a future TokenProvider that branches on the field sees
// the same value the CLI passed. We stand a recording provider behind the
// adapter and check the captured opts.
func TestAdapterSetupForwardsOptions(t *testing.T) {
	t.Parallel()

	var captured SetupOpts
	provider := func(_ context.Context, opts SetupOpts) (string, error) {
		captured = opts
		return "tok", nil
	}
	s := newMemSecrets()
	a := NewAdapter(New(
		WithClient(&fakeClient{}),
		WithDatabaseID("db"),
		WithSecrets(s),
		WithTokenProvider(provider),
	))
	if err := a.Setup(context.Background(), source.SetupOptions{NonInteractive: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !captured.NonInteractive {
		t.Errorf("NonInteractive not forwarded: %+v", captured)
	}
}

// TestAdapterSinceForwardsToInner — the source.Sincer signature has a
// checkpoint string argument the inner Plugin does not need (the inner
// Plugin manages its own per-database checkpoint key in Checkpointer, just
// like markdown's adapter). Verify the forwarding produces a list shaped
// like List would.
func TestAdapterSinceForwardsToInner(t *testing.T) {
	t.Parallel()

	c := &fakeClient{pages: []Page{
		{ID: "uuid-1", Title: "alpha"},
	}}
	a := NewAdapter(New(WithClient(c), WithDatabaseID("db")))
	got, err := a.Since(context.Background(), "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 1 || got[0].Title != "alpha" {
		t.Fatalf("Since = %+v", got)
	}
}

// TestAdapterAddCompleteDeleteRoundTrip — drives the optional bidirectional
// path through the adapter so callers see a coherent source.Task as the
// return value of Add and the archive-side effect of Complete/Delete on the
// fake client.
func TestAdapterAddCompleteDeleteRoundTrip(t *testing.T) {
	t.Parallel()

	c := &fakeClient{}
	a := NewAdapter(New(WithClient(c), WithDatabaseID("db")))
	ctx := context.Background()

	added, err := a.Add(ctx, "first", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added.ExternalID == "" || added.Title != "first" || added.Source != pluginName {
		t.Fatalf("Add = %+v", added)
	}
	if len(c.createCalls) != 1 || c.createCalls[0].title != "first" {
		t.Errorf("createCalls = %+v", c.createCalls)
	}

	if err := a.Complete(ctx, added.ExternalID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(c.updateCalls) != 1 || !c.updateCalls[0].archived || c.updateCalls[0].pageID != added.ExternalID {
		t.Errorf("updateCalls = %+v", c.updateCalls)
	}

	if err := a.Delete(ctx, added.ExternalID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(c.updateCalls) != 2 || !c.updateCalls[1].archived {
		t.Errorf("Delete did not archive: %+v", c.updateCalls)
	}
}
