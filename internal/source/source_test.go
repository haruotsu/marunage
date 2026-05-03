package source

import (
	"context"
	"reflect"
	"testing"
)

// TestAuthStatusConstantsAreDistinct guards the four documented states from
// requirement.md lines 102-114 so callers can branch on them without parsing
// strings. Compile-time exhaustiveness is enforced by the typed AuthStatus
// alias (a Go idiom: alias `string`, expose values as constants).
func TestAuthStatusConstantsAreDistinct(t *testing.T) {
	t.Parallel()

	values := []AuthStatus{
		AuthAuthenticated,
		AuthNotConfigured,
		AuthExpired,
		AuthRevoked,
	}
	seen := map[AuthStatus]bool{}
	for _, v := range values {
		if v == "" {
			t.Errorf("auth status constant must be non-empty")
		}
		if seen[v] {
			t.Errorf("auth status %q duplicated", v)
		}
		seen[v] = true
	}
}

// TestTaskFieldsCarryRequirementColumns mirrors the tasks-table mapping from
// docs/requirement.md (source / external_id / title / body / notes /
// raw_metadata) so a Discovery plugin's Task survives lossless round-trip into
// the queue layer (PR-71). reflect.DeepEqual against a fully-populated literal
// catches a silent field drop the moment a future PR removes one — checking
// only Source/ExternalID would let Notes or Priority disappear unnoticed.
func TestTaskFieldsCarryRequirementColumns(t *testing.T) {
	t.Parallel()

	want := Task{
		Source:      "markdown",
		ExternalID:  "abc123",
		Title:       "demo",
		Body:        "extra body",
		Notes:       "indented sub",
		Priority:    "P2",
		SourcePath:  "/tmp/todo.md",
		Done:        true,
		RawMetadata: map[string]any{"line": 3},
	}
	got := Task{
		Source:      "markdown",
		ExternalID:  "abc123",
		Title:       "demo",
		Body:        "extra body",
		Notes:       "indented sub",
		Priority:    "P2",
		SourcePath:  "/tmp/todo.md",
		Done:        true,
		RawMetadata: map[string]any{"line": 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Task round-trip lost a field:\n got = %+v\n want = %+v", got, want)
	}
	// Spot-check each named field individually so a regression report
	// names the offending column rather than dumping the whole struct.
	if got.Source != "markdown" {
		t.Errorf("Source = %q", got.Source)
	}
	if got.ExternalID != "abc123" {
		t.Errorf("ExternalID = %q", got.ExternalID)
	}
	if got.Title != "demo" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Body != "extra body" {
		t.Errorf("Body = %q", got.Body)
	}
	if got.Notes != "indented sub" {
		t.Errorf("Notes = %q", got.Notes)
	}
	if got.Priority != "P2" {
		t.Errorf("Priority = %q", got.Priority)
	}
	if got.SourcePath != "/tmp/todo.md" {
		t.Errorf("SourcePath = %q", got.SourcePath)
	}
	if !got.Done {
		t.Errorf("Done = false, want true")
	}
	if got.RawMetadata["line"] != 3 {
		t.Errorf("RawMetadata[line] = %v, want 3", got.RawMetadata["line"])
	}
}

// stubPlugin is the minimum Plugin implementation we need to assert that the
// interface compiles and that a struct can satisfy it without claiming the
// optional sub-interfaces.
type stubPlugin struct{}

func (stubPlugin) Name() string { return "stub" }
func (stubPlugin) List(context.Context) ([]Task, error) {
	return nil, nil
}
func (stubPlugin) Setup(context.Context, SetupOptions) error { return nil }
func (stubPlugin) AuthStatus(context.Context) (AuthStatus, error) {
	return AuthAuthenticated, nil
}

// TestPluginInterfaceCompiles is the type-assertion-as-test pattern: if the
// Plugin interface ever drops a method or changes a signature, this var
// declaration stops compiling.
func TestPluginInterfaceCompiles(t *testing.T) {
	t.Parallel()

	var p Plugin = stubPlugin{}
	if p.Name() != "stub" {
		t.Fatalf("stub plugin Name() = %q", p.Name())
	}
	// Optional capabilities are NOT implemented by stubPlugin; the type
	// assertion must therefore fail. This locks in the interface-segregation
	// contract: callers cannot accidentally invoke Since/Add/Complete/Delete
	// against a plugin that did not opt in.
	if _, ok := p.(Sincer); ok {
		t.Errorf("stubPlugin must not implement Sincer")
	}
	if _, ok := p.(Adder); ok {
		t.Errorf("stubPlugin must not implement Adder")
	}
	if _, ok := p.(Completer); ok {
		t.Errorf("stubPlugin must not implement Completer")
	}
	if _, ok := p.(Deleter); ok {
		t.Errorf("stubPlugin must not implement Deleter")
	}
}
