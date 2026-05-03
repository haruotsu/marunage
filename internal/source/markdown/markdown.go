// Package markdown implements the Markdown TODO source plugin promised
// in docs/requirement.md row #1 of the standard sources table (lines
// 117-130) and detailed in PR-50 of pr_split_plan.md.
//
// The plugin owns one or more Markdown files containing GitHub-flavoured
// task list lines (`- [ ] ...` / `- [x] ...`) and exposes them through a
// stable Go API: List / Since / Add / Complete / Delete / Setup. The
// methods mirror the Discovery plugin subcommand contract from
// requirement.md lines 102-114 so a later PR (PR-70 Discovery IF) can
// forward CLI subcommands directly to this package without re-parsing
// flags.
//
// Phase 1 positioning: per requirement.md lines 146-148, the Markdown
// source bypasses Claude triage; callers that materialise these tasks
// into the queue should record `judgment_reason = "phase1: markdown
// source bypass"`. Materialisation itself is not this package's
// responsibility — the package returns Task values and lets the queue
// integration layer decide whether to insert them.
//
// Dependencies: this package intentionally does NOT import
// internal/store. The PR-50 brief and pr_split_plan.md keep the source
// plugin self-contained so it can be released independently of the
// kv_state repo (PR-12) and the queue schema. The Checkpointer
// interface is defined locally; PR-70 wires the real
// internal/store.KVStateRepo behind it.
package markdown

import "errors"

// Typed sentinel errors. Callers branch on errors.Is rather than parsing
// strings; the CLI binding (PR-70) maps these to documented exit codes.
var (
	// ErrTaskNotFound is returned by Complete / Delete when the requested
	// ExternalID is not present in any of the configured files.
	ErrTaskNotFound = errors.New("markdown: task not found")

	// ErrFileNotFound is returned when a configured file is missing and
	// the operation requires it (List / Since on a non-existent file is
	// not fatal — it returns an empty result; Add / Complete / Delete
	// against a non-existent file is, because the user clearly asked us
	// to mutate a specific path).
	ErrFileNotFound = errors.New("markdown: file not found")

	// ErrInvalidMarker is returned when a `<!-- marunage:... -->` comment
	// is malformed (e.g. a token that has no `=`). We surface this as a
	// typed error rather than silently dropping the marker so a partially
	// hand-edited file gets a loud failure instead of a silent rewrite
	// that loses the user's bookkeeping.
	ErrInvalidMarker = errors.New("markdown: invalid marunage marker")

	// ErrNoFilesConfigured is returned by methods that need at least one
	// target file when none was supplied via WithFiles.
	ErrNoFilesConfigured = errors.New("markdown: no files configured")
)
