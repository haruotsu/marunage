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

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

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

// Task is the source-side view of one checklist item. The struct is
// intentionally distinct from internal/store.Task: keeping these
// disjoint lets PR-50 ship before the queue schema stabilises and
// forces the queue integration layer (PR-70) to make explicit choices
// about which fields map where.
type Task struct {
	// ExternalID is the stable identifier persisted into the file as
	// `<!-- marunage:id=... -->`. List generates one on first sight of
	// a marker-less line; subsequent List calls observe the same value.
	ExternalID string
	Title      string
	Notes      string // reserved for future use (indented sub-text capture); empty in PR-50
	Done       bool
	SourcePath string
	LineNumber int // 1-based; debug aid
}

// Checkpointer is the minimal key/value store the Markdown plugin needs
// to drive Since's mtime gate. The interface is local so PR-50 can
// build without depending on internal/store.KVStateRepo, which is
// being landed by a parallel PR (PR-12). PR-70 (Discovery IF) wires
// the real repo behind this seam.
type Checkpointer interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// Plugin is the entry point for the Markdown source. Construct one
// with New and reuse it: the struct holds an internal mutex that
// serialises file mutations so concurrent Add / Complete / Delete on
// the same Plugin instance do not interleave half-written lines.
type Plugin struct {
	files        []string
	checkpointer Checkpointer
	now          func() time.Time
	idGen        func() (string, error)

	// mu serialises mutating operations (Add / Complete / Delete and
	// the marker-injection branch of List). Read-only paths take it
	// too because they may upgrade to a write when injecting markers.
	mu sync.Mutex
}

// Option is the functional-option shape New accepts. Mirrors the
// pattern used in internal/store.NewTaskRepo / internal/secrets.Open
// so callers see a consistent style across the codebase.
type Option func(*Plugin)

// WithFiles sets the list of Markdown files this Plugin owns. Order is
// preserved and surfaces in List output, so callers can pin a stable
// ordering by passing files in the order they want.
func WithFiles(paths ...string) Option {
	return func(p *Plugin) {
		// Defensive copy: callers sometimes pass a slice they keep
		// mutating elsewhere.
		p.files = append(p.files[:0:0], paths...)
	}
}

// WithCheckpointer wires the Since-gate persistence. Optional: when no
// Checkpointer is supplied, Since degrades to "behave like List" (no
// state is remembered between calls), which is the right behaviour
// for one-shot CLI invocations but useless for a long-running daemon.
func WithCheckpointer(c Checkpointer) Option {
	return func(p *Plugin) { p.checkpointer = c }
}

// WithClock injects a deterministic time source. Defaults to time.Now.
// Tests use this to pin checkpoint values without sleeping.
func WithClock(now func() time.Time) Option {
	return func(p *Plugin) { p.now = now }
}

// withIDGen overrides the ExternalID generator. Lower-cased because
// only tests need it; production code always uses the crypto/rand
// default. Renaming to capital W if a future caller needs it.
func withIDGen(gen func() (string, error)) Option {
	return func(p *Plugin) { p.idGen = gen }
}

// New constructs a Plugin with the given options. Defaults (clock,
// ExternalID generator) are filled in here so options can override
// them before any method runs.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		now:   time.Now,
		idGen: defaultIDGen,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// defaultIDGen produces 12-hex-character (6 random bytes) identifiers.
// 6 bytes ≈ 2^48 ≈ 281 trillion possibilities, far above the realistic
// per-file task count, so a within-file collision check would be pure
// ceremony. The hex encoding keeps the marker URL-safe and easy to
// eyeball-diff. crypto/rand is mandatory: a timestamp- or counter-
// based id would let a user with two concurrent Plugin processes (e.g.
// CLI + daemon) collide on the same id.
func defaultIDGen() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// List returns every checklist line across all configured files, in
// (file, line) order. The first time List sees a marker-less line it
// generates an ExternalID and rewrites the file in-place to embed the
// marker — that mutation happens through the same atomicWriteFile path
// the explicit mutating methods use, so a crash mid-list cannot leave
// a half-written file.
func (p *Plugin) List(ctx context.Context) ([]Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listLocked(ctx, p.files)
}

// listLocked is the locked core shared by List and Since. The caller
// passes the subset of files to scan; the rest of the algorithm is
// identical.
func (p *Plugin) listLocked(ctx context.Context, files []string) ([]Task, error) {
	var out []Task
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Per package contract: missing files are not fatal
				// for read paths. This matches the way `marunage list`
				// keeps working after a user renames a file.
				continue
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		parsed, err := parse(path, body)
		if err != nil {
			return nil, err
		}
		// Inject markers for any line that arrived without one. We
		// build the rewrite eagerly so a single file write covers
		// every new marker rather than one rewrite per line.
		mutated := false
		newBody := body
		for i := range parsed {
			if parsed[i].Marker.Present && parsed[i].Marker.ID != "" {
				continue
			}
			id, err := p.idGen()
			if err != nil {
				return nil, fmt.Errorf("generate id: %w", err)
			}
			// Preserve any existing partial marker fields (source /
			// external_id / Extra). Without this merge, a hand-edited
			// "<!-- marunage:source=upstream -->" would lose the
			// source attribution the moment List runs over it.
			mk := parsed[i].Marker
			mk.Present = true
			mk.ID = id
			if mk.Source == "" {
				mk.Source = "markdown"
			}
			if mk.Extra == nil {
				mk.Extra = map[string]string{}
			}
			parsed[i].Marker = mk
			newBody = injectMarker(newBody, parsed[i].LineNumber, mk)
			mutated = true
		}
		if mutated {
			if err := atomicWriteFile(path, newBody, 0o600); err != nil {
				return nil, fmt.Errorf("persist markers in %s: %w", path, err)
			}
		}
		for _, pt := range parsed {
			out = append(out, Task{
				ExternalID: pt.Marker.ID,
				Title:      pt.Title,
				Done:       pt.Done,
				SourcePath: path,
				LineNumber: pt.LineNumber,
			})
		}
	}
	return out, nil
}

// Since returns tasks from files whose mtime is strictly greater than
// the checkpoint persisted last time Since ran. It is the optional
// `since <checkpoint>` subcommand from requirement.md lines 102-114
// recast as a Go method: the per-file checkpoint state lives in the
// injected Checkpointer (PR-12's KVStateRepo at runtime, an in-memory
// fake in tests).
//
// On first call (no checkpoint stored) Since behaves like List and
// then writes the current mtime of every visited file. Files that
// have been modified since are returned; files that have not are
// skipped. Missing files are skipped, mirroring List.
//
// When no Checkpointer was supplied (single-shot CLI invocations),
// Since degrades to List — there is no place to remember state, so
// returning everything is the only safe answer.
func (p *Plugin) Since(ctx context.Context) ([]Task, error) {
	if p.checkpointer == nil {
		return p.List(ctx)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var todo []string
	mtimes := map[string]time.Time{}
	for _, path := range p.files {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		raw, err := p.checkpointer.Get(ctx, checkpointKey(path))
		if err != nil {
			return nil, fmt.Errorf("checkpoint get %s: %w", path, err)
		}
		// Empty value means "no checkpoint yet"; treat as the zero
		// time so every file is returned on first call.
		var prev time.Time
		if raw != "" {
			t, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return nil, fmt.Errorf("parse checkpoint for %s: %w", path, err)
			}
			prev = t
		}
		mtime := info.ModTime()
		mtimes[path] = mtime
		if mtime.After(prev) {
			todo = append(todo, path)
		}
	}

	tasks, err := p.listLocked(ctx, todo)
	if err != nil {
		return nil, err
	}

	// Persist the new checkpoint AFTER listLocked, because listLocked
	// may have rewritten the file to inject markers (which bumps mtime
	// again). Re-stat so the stored value reflects the post-rewrite
	// mtime; otherwise the next Since call would re-include the same
	// file forever.
	for _, path := range todo {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("re-stat %s: %w", path, err)
		}
		mtimes[path] = info.ModTime()
	}
	for path, mtime := range mtimes {
		if err := p.checkpointer.Set(ctx, checkpointKey(path), mtime.UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, fmt.Errorf("checkpoint set %s: %w", path, err)
		}
	}
	return tasks, nil
}

// checkpointKey returns the kv_state key used to remember a file's last
// scanned mtime. The key is namespaced with `markdown:` so it cannot
// collide with future sources, and uses the absolute file path so
// renaming a file invalidates the checkpoint (the renamed file is, for
// our purposes, a brand new file).
func checkpointKey(path string) string {
	return "markdown:mtime:" + path
}

// Add appends a new checklist line to the first configured file and
// returns the resulting Task. notes is currently ignored (reserved for
// a follow-up that captures sub-indented detail lines); a non-empty
// value is accepted today so the API does not need to change later.
//
// Why "first configured file" rather than "all of them": Add cannot
// know which file the user thinks of as the active one when several
// are configured, so the documented rule is "the head of WithFiles".
// Callers that want different behaviour can construct a per-file
// Plugin.
func (p *Plugin) Add(ctx context.Context, title, _ string) (Task, error) {
	if len(p.files) == 0 {
		return Task{}, ErrNoFilesConfigured
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	path := p.files[0]
	id, err := p.idGen()
	if err != nil {
		return Task{}, fmt.Errorf("generate id: %w", err)
	}
	mk := marker{Present: true, ID: id, Source: "markdown", Extra: map[string]string{}}
	tl := taskLine{Title: title, Done: false, Marker: mk}
	line := renderTaskLine(tl)

	// Read current body (empty if missing) and append. A missing file
	// is fine here — Setup is not strictly required before Add, and
	// the user's intent is "make this task exist".
	body, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Task{}, fmt.Errorf("read %s: %w", path, err)
	}
	newBody := appendLine(body, line)
	if err := atomicWriteFile(path, newBody, 0o600); err != nil {
		return Task{}, fmt.Errorf("write %s: %w", path, err)
	}
	// Re-parse so the returned LineNumber matches the on-disk file.
	parsed, err := parse(path, newBody)
	if err != nil {
		return Task{}, err
	}
	for _, pt := range parsed {
		if pt.Marker.ID == id {
			return Task{
				ExternalID: id,
				Title:      pt.Title,
				Done:       pt.Done,
				SourcePath: path,
				LineNumber: pt.LineNumber,
			}, nil
		}
	}
	// Should not happen unless the parser disagrees with the renderer.
	return Task{}, fmt.Errorf("markdown: appended line not found by parser")
}

// Complete flips the checkbox of the line with the given ExternalID
// from `[ ]` to `[x]`. ExternalID is matched across every configured
// file so callers do not need to remember which file a task came from.
func (p *Plugin) Complete(ctx context.Context, externalID string) error {
	return p.mutateLine(ctx, externalID, func(parsed []parsedTask, idx int, body []byte) ([]byte, error) {
		mk := parsed[idx].Marker
		tl := taskLine{
			Title:  parsed[idx].Title,
			Done:   true,
			Marker: mk,
		}
		// Preserve the original indent by re-reading it from the line.
		eol := detectEOL(body)
		lines := splitLines(body)
		i := parsed[idx].LineNumber - 1
		if m := checkboxLine.FindStringSubmatch(lines[i]); m != nil {
			tl.Indent = m[1]
		}
		lines[i] = renderTaskLine(tl)
		out := joinLines(lines, eol)
		if hadTrailingNewline(body) {
			out = append(out, eol...)
		}
		return out, nil
	})
}

// Delete removes the line carrying externalID. Surrounding lines and
// prose are preserved verbatim; only the one matching line goes away.
func (p *Plugin) Delete(ctx context.Context, externalID string) error {
	return p.mutateLine(ctx, externalID, func(parsed []parsedTask, idx int, body []byte) ([]byte, error) {
		eol := detectEOL(body)
		lines := splitLines(body)
		i := parsed[idx].LineNumber - 1
		lines = append(lines[:i], lines[i+1:]...)
		out := joinLines(lines, eol)
		if hadTrailingNewline(body) && (len(out) == 0 || out[len(out)-1] != '\n') {
			out = append(out, eol...)
		}
		return out, nil
	})
}

// Setup creates each configured file (and any missing parent dirs) if
// it does not yet exist. Existing files are left untouched, making the
// operation idempotent — the documented contract for the `setup`
// subcommand in requirement.md lines 102-114.
func (p *Plugin) Setup(ctx context.Context) error {
	if len(p.files) == 0 {
		return ErrNoFilesConfigured
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, path := range p.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", dir, err)
			}
		}
		if err := atomicWriteFile(path, nil, 0o600); err != nil {
			return fmt.Errorf("touch %s: %w", path, err)
		}
	}
	return nil
}

// mutateLine is the shared core for Complete and Delete: it scans every
// configured file, locates the line whose marker.ID equals externalID,
// hands the (parsed, body) pair to mutate to produce a new body, then
// atomic-writes the result. Wrapping this pattern keeps both callers
// honest about the same locking and "one matching line" invariants.
func (p *Plugin) mutateLine(
	ctx context.Context,
	externalID string,
	mutate func(parsed []parsedTask, idx int, body []byte) ([]byte, error),
) error {
	if len(p.files) == 0 {
		return ErrNoFilesConfigured
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, path := range p.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read %s: %w", path, err)
		}
		parsed, err := parse(path, body)
		if err != nil {
			return err
		}
		for i := range parsed {
			if parsed[i].Marker.ID != externalID {
				continue
			}
			newBody, err := mutate(parsed, i, body)
			if err != nil {
				return err
			}
			if err := atomicWriteFile(path, newBody, 0o600); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			return nil
		}
	}
	return ErrTaskNotFound
}

// appendLine returns body + line + eol, inserting a separating eol
// first if body did not already end with a newline. Empty body
// produces just "line<eol>" — no leading blank line for fresh files.
// eol is detected from body so a CRLF file stays CRLF after Add.
func appendLine(body []byte, line string) []byte {
	eol := detectEOL(body)
	out := make([]byte, 0, len(body)+len(line)+2*len(eol))
	out = append(out, body...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, eol...)
	}
	out = append(out, line...)
	out = append(out, eol...)
	return out
}

func hadTrailingNewline(body []byte) bool {
	return len(body) > 0 && body[len(body)-1] == '\n'
}

// stableSortTasksByPathLine keeps List output deterministic when callers
// pass overlapping files (rare, but we sort defensively rather than
// relying on map iteration order anywhere). Currently only used in
// tests; exported as an unexported helper so a future feature can reuse it.
func stableSortTasksByPathLine(tasks []Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].SourcePath != tasks[j].SourcePath {
			return tasks[i].SourcePath < tasks[j].SourcePath
		}
		return tasks[i].LineNumber < tasks[j].LineNumber
	})
}
