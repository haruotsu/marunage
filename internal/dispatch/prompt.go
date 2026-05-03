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
}

// promptSeparator is the delimiter between adjacent prompt sections.
// A blank line is enough to keep the sections visually distinct in the
// cmux scrollback without inflating the byte count cmux send carries.
const promptSeparator = "\n\n"

// fenceEscapeReplacement breaks the literal "<<" sequence so a
// user-derived field cannot forge a fence open/close. We insert a
// zero-width-looking marker that is harmless to a human reader (still
// visible verbatim) but makes the escaped substring lexically distinct
// from any of the labelled fence tokens this package emits. We use
// "<\\<" (a backslash between the two angle brackets) because (a) it is
// reviewer-readable in the rendered prompt, (b) it does not collide
// with any markdown construct cmux's text relay would treat specially,
// and (c) it cannot accidentally re-form a fence after another pass
// (the backslash means no further "<<" / "</" remains).
const fenceEscapeReplacement = `<\<`

// fenceEscape rewrites every "<<" inside a user-derived value so the
// downstream Claude session cannot be tricked into treating attacker
// content as a fence boundary. Trusted sections (Base / SourceSpecific
// from skills/) skip this pass — they are not under attacker control.
func fenceEscape(s string) string {
	return strings.ReplaceAll(s, "<<", fenceEscapeReplacement)
}

// fenced wraps the (already-escaped) value in <<label>>...<</label>>.
// Empty values still produce a fence pair so a downstream parser can
// distinguish "absent field" (fence empty) from "no fence at all"
// (field never rendered). The label is required to be a literal
// ASCII identifier under this package's control — we never interpolate
// untrusted data into it.
func fenced(label, value string) string {
	return fmt.Sprintf("<<%s>>\n%s\n<</%s>>", label, value, label)
}

// BuildPrompt concatenates the (Base, SourceSpecific, Task) sections in
// that fixed order. Empty sections drop out cleanly so a source without
// a dedicated skill produces "Base + Task" with one separator, not two.
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

	parts := make([]string, 0, 3)
	if s := strings.TrimSpace(in.Base); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(in.SourceSpecific); s != "" {
		parts = append(parts, s)
	}
	parts = append(parts, taskBlock)
	return strings.Join(parts, promptSeparator)
}
