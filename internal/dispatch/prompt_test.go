package dispatch_test

import (
	"path/filepath"
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

// PR-43 sentinel-instruction test list:
//
//   P1. WorkspaceDir set -> the prompt tail tells Claude to write
//       .exit_code under that absolute path (so the watcher polling
//       the same dir can detect completion).
//   P2. The instruction shows the documented atomic command pair
//       (`echo $? > .exit_code.tmp && mv .exit_code.tmp .exit_code`),
//       so a reader observing the dir mid-write never consumes a
//       half-written byte.
//   P3. WorkspaceDir empty -> sentinel section is omitted entirely,
//       preserving PR-42's prompt shape for callers that have not yet
//       wired completion.
//   P4. The instruction also tells Claude to write `.result_summary`
//       BEFORE renaming the sentinel into place, so the watcher reads
//       both files atomically (sentinel acts as the publish barrier).

// P1 + P2: sentinel instruction with absolute path + atomic rename.
func TestBuildPromptIncludesSentinelInstructionWhenWorkspaceDirSet(t *testing.T) {
	const dir = "/home/me/.marunage/workspaces/7"
	got := dispatch.BuildPrompt(dispatch.PromptInputs{
		Base: "BASE",
		Task: store.Task{
			ID: 7, Source: "manual", Title: "with sentinel", Body: "b",
		},
		WorkspaceDir: dir,
	})

	wantParts := []string{
		filepath.Join(dir, ".exit_code"),      // P1: absolute path visible
		".exit_code.tmp",                      // P2: tmp half of atomic write
		"mv",                                  // P2: rename verb
		filepath.Join(dir, ".result_summary"), // P4: summary path visible
	}
	for _, want := range wantParts {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q in:\n%s", want, got)
		}
	}
}

// P3: empty WorkspaceDir keeps PR-42 wire-format intact.
func TestBuildPromptOmitsSentinelInstructionWhenWorkspaceDirEmpty(t *testing.T) {
	got := dispatch.BuildPrompt(dispatch.PromptInputs{
		Base: "BASE",
		Task: store.Task{
			ID: 1, Source: "manual", Title: "no sentinel", Body: "b",
		},
		// WorkspaceDir intentionally left empty.
	})
	for _, banned := range []string{".exit_code", ".result_summary"} {
		if strings.Contains(got, banned) {
			t.Errorf("prompt unexpectedly contains %q (sentinel section should be off):\n%s", banned, got)
		}
	}
}

// P4: result_summary write precedes the sentinel rename, so the
// publish barrier (.exit_code) is observed only after the summary file
// is already on disk. We pin the order by string position.
func TestBuildPromptOrdersResultSummaryBeforeSentinelRename(t *testing.T) {
	const dir = "/home/me/.marunage/workspaces/9"
	got := dispatch.BuildPrompt(dispatch.PromptInputs{
		Base: "BASE",
		Task: store.Task{
			ID: 9, Source: "manual", Title: "ordering", Body: "b",
		},
		WorkspaceDir: dir,
	})
	summaryAt := strings.Index(got, ".result_summary")
	renameAt := strings.LastIndex(got, "mv ")
	if summaryAt < 0 || renameAt < 0 {
		t.Fatalf("sentinel sections missing:\n%s", got)
	}
	if summaryAt >= renameAt {
		t.Errorf("expected .result_summary write to precede the sentinel mv (so watcher reads summary atomically); summary=%d rename=%d in:\n%s",
			summaryAt, renameAt, got)
	}
}
