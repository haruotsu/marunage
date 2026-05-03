// Package dispatch is the execution-layer dispatcher (PR-42). It picks
// pending tasks off the store, claims their soft locks, spawns one
// cmux workspace per task, and sends a Claude prompt that combines the
// base execution skill, the source-specific skill (if any), and the
// task body. See docs/requirement.md "Execution（実行）— ディスパッチャ詳細".
//
// The package is intentionally split:
//   - prompt.go   — pure prompt assembly (BuildPrompt). No I/O so it can
//     be unit-tested without spinning up cmux / sqlite.
//   - lockkey.go  — notes.lock_hint -> [execution.lock_keys] resolver.
//   - dispatch.go — Run / Dispatcher: ties the cmux client + store repo
//     together with the lock-key resolver and prompt
//     builder.
package dispatch

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/haruotsu/marunage/internal/store"
)

// PromptInputs is the ingredient list BuildPrompt assembles. Keeping it
// as a struct (rather than positional args) leaves room for future
// sections (system prompt overrides, reflection-on-resume, ...) without
// breaking call sites at the dispatcher.
type PromptInputs struct {
	// Base is the contents of skills/marunage-execute/SKILL.md (or the
	// caller's substitute). It always appears first so high-level
	// guardrails are loaded into context before anything else.
	Base string
	// SourceSpecific is the contents of skills/marunage-source-<name>/SKILL.md,
	// or empty when the source has no dedicated skill. Sandwiched between
	// Base and the task block so source-specific overrides can refine the
	// base instructions.
	SourceSpecific string
	// Task is the row being dispatched. BuildPrompt reads ID / Source /
	// Title / Body and renders them into a labelled task block so the
	// receiving Claude session can quote them back deterministically.
	Task store.Task
	// WorkspaceDir is marunage's per-task control directory (typically
	// ~/.marunage/workspaces/<id>). When non-empty, BuildPrompt appends
	// a sentinel-write instruction telling Claude to publish completion
	// via `echo $? > .exit_code.tmp && mv .exit_code.tmp .exit_code`
	// inside this dir so the PR-43 completion watcher polling the same
	// path can detect exit. Empty disables the section entirely
	// (back-compat for callers that have not wired completion yet).
	WorkspaceDir string
}

// promptSeparator is the delimiter between adjacent prompt sections.
// A blank line is enough to keep the sections visually distinct in the
// cmux scrollback without inflating the byte count cmux send carries.
const promptSeparator = "\n\n"

// leftAngleRunRe matches any maximal run of "<" characters. We rewrite
// runs of length >=2 so no two "<" can ever appear adjacent in the
// escaped output — that is what guarantees an attacker cannot forge a
// "<<label>>" fence-open or "<</label>>" fence-close. A naive
// `strings.ReplaceAll("<<", "<\<")` is NOT idempotent: "<<<<" replaces
// left-to-right and leaves "<<" at the seams.
var leftAngleRunRe = regexp.MustCompile(`<+`)

// fenceEscape rewrites every multi-"<" run inside a user-derived value
// so an attacker cannot forge a fence boundary. Idempotent under
// repeated application. Trusted sections (Base / SourceSpecific) skip
// this pass.
func fenceEscape(s string) string {
	return leftAngleRunRe.ReplaceAllStringFunc(s, func(run string) string {
		if len(run) < 2 {
			return run
		}
		return strings.Repeat(`<\`, len(run))
	})
}

// fenced wraps the (already-escaped) value in <<label>>...<</label>>.
func fenced(label, value string) string {
	return fmt.Sprintf("<<%s>>\n%s\n<</%s>>", label, value, label)
}

// BuildPrompt concatenates (Base, SourceSpecific, Task, Sentinel) in
// that fixed order. Empty sections drop out cleanly.
//
// User-derived fields (Source, ExternalID, ExternalURL, Title, Body) go
// through fenceEscape so a malicious payload cannot splice a forged
// fence-close + override into the prompt. requirement.md L29 invariant
// #2 ("No silent execution") is the upstream policy this satisfies:
// the receiving Claude session can refuse to follow instructions that
// originate from inside a `<<body>>` / `<<title>>` fence.
//
// The Send wrapper in internal/cmux collapses any embedded \r\n run into
// a single space before handing the payload to cmux; preserving the
// original line breaks here keeps the prompt readable when the caller
// inspects it via `marunage show <id>` or the Web UI.
func BuildPrompt(in PromptInputs) string {
	taskHeader := fmt.Sprintf("## Task #%d", in.Task.ID)
	taskBlock := strings.Join([]string{
		taskHeader,
		fenced("source", fenceEscape(in.Task.Source)),
		fenced("external_id", fenceEscape(in.Task.ExternalID)),
		fenced("origin", fenceEscape(in.Task.ExternalURL)),
		fenced("title", fenceEscape(in.Task.Title)),
		fenced("body", fenceEscape(in.Task.Body)),
	}, "\n\n")

	parts := make([]string, 0, 4)
	if s := strings.TrimSpace(in.Base); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(in.SourceSpecific); s != "" {
		parts = append(parts, s)
	}
	parts = append(parts, taskBlock)
	if s := sentinelInstruction(in.WorkspaceDir); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, promptSeparator)
}

// sentinelInstruction renders the closing block that tells Claude how to
// publish completion atomically. Three-step contract:
//
//  1. Capture the task's $? IMMEDIATELY into $EC, before anything else
//     mutates it (printf in step 2 would otherwise overwrite $? with
//     printf's own — almost always 0 — exit code, silently turning
//     every failed task into a "success").
//  2. Write `<dir>/.result_summary` with the trimmed final summary.
//  3. Write `<dir>/.exit_code.tmp` (carrying $EC) then `mv` it to
//     `<dir>/.exit_code`.
//
// The mv is the publish barrier: same-FS rename is atomic on POSIX, so
// the PR-43 completion watcher polling `<dir>/.exit_code` either sees
// the final byte or no file at all — never a half-written sentinel.
// Writing `.result_summary` before the rename guarantees the watcher
// reading both files after the rename always finds a complete summary.
//
// Empty workspaceDir returns "" so the section is omitted entirely
// (back-compat for callers that have not wired completion yet).
func sentinelInstruction(workspaceDir string) string {
	if workspaceDir == "" {
		return ""
	}
	exitPath := filepath.Join(workspaceDir, ".exit_code")
	tmpPath := filepath.Join(workspaceDir, ".exit_code.tmp")
	summaryPath := filepath.Join(workspaceDir, ".result_summary")
	return fmt.Sprintf(
		"## Completion sentinel\n\n"+
			"When this task is complete (success OR failure), publish the outcome by running, "+
			"in this exact order:\n\n"+
			"  EC=$?\n"+
			"  printf '%%s' \"<one-line summary>\" > %s\n"+
			"  echo \"$EC\" > %s && mv %s %s\n\n"+
			"The EC=$? capture MUST come first so the sentinel records the task's exit code, "+
			"not printf's (which is almost always 0 and would silently mask every failure).\n"+
			"The mv is the publish barrier — the marunage completion watcher polls %s "+
			"and treats its presence as the signal that this task has exited. Do not write %s "+
			"directly; always go through the .tmp + mv so the reader never sees a half-written file.",
		summaryPath, tmpPath, tmpPath, exitPath, exitPath, exitPath,
	)
}
