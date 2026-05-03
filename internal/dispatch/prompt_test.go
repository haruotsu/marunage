package dispatch_test

import (
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/store"
)

// PR-42 prompt builder test list (t_wada TDD; ticked off as the matching
// test below goes green):
//
//   A1. BuildPrompt orders sections base -> source -> task body, in that
//       fixed order so the dispatched session always reads the high-level
//       guidance before the source-specific guidance before the task
//       payload (docs/requirement.md execution dispatcher step 2.d).
//   A2. BuildPrompt collapses cleanly when source-specific guidance is
//       empty: the base + task body are still concatenated with one
//       separator each, no doubled blank lines, no trailing separator
//       leftover from the missing section.
//   A3. BuildPrompt's task-body section names id / source / title / body
//       so the receiving Claude session can see the same metadata the
//       CLI shows for `marunage show <id>`.

// A1: full ordering + delimiter shape.
func TestBuildPromptOrdersSections(t *testing.T) {
	got := dispatch.BuildPrompt(dispatch.PromptInputs{
		Base:           "BASE-SKILL",
		SourceSpecific: "SOURCE-SKILL",
		Task: store.Task{
			ID:     7,
			Source: "markdown",
			Title:  "Buy milk",
			Body:   "from the corner store",
		},
	})

	baseAt := strings.Index(got, "BASE-SKILL")
	srcAt := strings.Index(got, "SOURCE-SKILL")
	titleAt := strings.Index(got, "Buy milk")

	if baseAt < 0 || srcAt < 0 || titleAt < 0 {
		t.Fatalf("missing section in prompt:\n%s", got)
	}
	if baseAt >= srcAt || srcAt >= titleAt {
		t.Errorf("section order wrong: base=%d src=%d title=%d in:\n%s",
			baseAt, srcAt, titleAt, got)
	}
}

// A2: empty source-specific section collapses without double separators.
func TestBuildPromptHandlesEmptySourceSection(t *testing.T) {
	got := dispatch.BuildPrompt(dispatch.PromptInputs{
		Base:           "BASE-SKILL",
		SourceSpecific: "",
		Task: store.Task{
			ID:     1,
			Source: "manual",
			Title:  "t",
			Body:   "b",
		},
	})

	if !strings.Contains(got, "BASE-SKILL") || !strings.Contains(got, "t") {
		t.Fatalf("base or title missing:\n%s", got)
	}
	// Doubled blank-line separator means we left a hole where the empty
	// source-specific section used to be. The dispatcher's own concatenation
	// joins with "\n\n"; a missing middle section must not produce "\n\n\n\n".
	if strings.Contains(got, "\n\n\n\n") {
		t.Errorf("doubled separator found around empty source section:\n%s", got)
	}
}

// A3: id / source / title / body all visible in the rendered task block.
func TestBuildPromptIncludesTaskMetadata(t *testing.T) {
	got := dispatch.BuildPrompt(dispatch.PromptInputs{
		Base: "BASE",
		Task: store.Task{
			ID:     42,
			Source: "github_issue",
			Title:  "Fix flaky test",
			Body:   "Reproduces only in CI on darwin-arm64.",
		},
	})

	for _, want := range []string{"42", "github_issue", "Fix flaky test", "Reproduces only in CI on darwin-arm64."} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q in:\n%s", want, got)
		}
	}
}
