// Package github implements the GitHub Discovery source plugin promised
// in docs/pr_split_plan.md PR-83. The plugin surfaces issues and pull
// requests assigned to the current user (`is:open assignee:@me`) through
// the generic source.Plugin contract, using the user's `gh` CLI for both
// authentication and IO so PR-83 inherits gh's auth handling rather than
// re-implementing OAuth.
//
// Why shell out to `gh` rather than talk to the REST/GraphQL API directly:
//
//   - gh already has a robust credential flow (`gh auth login`, OAuth refresh,
//     keyring integration) that the daemon would otherwise have to duplicate.
//   - Users already have gh installed for marunage doctor's GitHub source
//     check, so there is no extra dependency.
//   - The Runner abstraction (runner.go) keeps the dependency injection
//     story identical to internal/cmux/runner.go, so a future PR that wants
//     to swap in the API client can do so without touching every call site.
//
// Concurrency: the Plugin holds no mutable state — every method receives a
// context and forwards it into the injected Runner — so callers may share a
// single instance across goroutines without external locking.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/haruotsu/marunage/internal/source"
)

// pluginName is the canonical identifier registered with source.Registry and
// stamped on every emitted Task. Defined as a const so the manifest, the
// adapter, and the package agree at compile time.
const pluginName = "github"

// defaultQuery is the GitHub search query the brief mandates: open items
// assigned to the authenticated user. Kept as a package-level constant so
// the test suite and any future override path see the same source of truth.
const defaultQuery = "is:open assignee:@me"

// jsonFields is the set of fields requested from `gh search ...`. Listed
// explicitly (rather than `gh ... --json all`) so a future gh release that
// adds a noisy field cannot bloat the response or destabilise the parser.
const jsonFields = "number,title,body,updatedAt,url,repository"

// Typed sentinel errors. Callers branch on errors.Is rather than parsing
// strings; the CLI / daemon layer maps these to documented exit codes.
var (
	// ErrInvalidResponse is returned when `gh` produces output that does not
	// match the expected JSON shape. The package wraps the underlying parser
	// error so a debugging session can still see what gh sent back.
	ErrInvalidResponse = errors.New("github: invalid gh response")

	// ErrInvalidExternalID is returned by Complete when the supplied id does
	// not match the documented `<owner>/<repo>#<number>` shape. We surface
	// this before invoking the runner so a malformed id cannot silently
	// trigger a `gh issue close` against an unrelated repo.
	ErrInvalidExternalID = errors.New("github: invalid external id")

	// ErrInteractiveSetupRequired is returned by Setup when authentication
	// is missing. The Runner abstraction cannot drive `gh auth login` (which
	// requires a TTY), so the right answer is to defer to the user.
	ErrInteractiveSetupRequired = errors.New("github: interactive setup required (run `gh auth login`)")
)

// Plugin is the GitHub Discovery source. Construct one with New and reuse
// across goroutines: the struct holds only its dependencies.
type Plugin struct {
	runner            Runner
	completionComment string
}

// Option is the functional-option shape New accepts. Mirrors the pattern in
// internal/source/markdown so callers see a consistent style across the
// codebase.
type Option func(*Plugin)

// WithRunner injects a custom Runner. Tests pass a fake; production callers
// leave this unset and get the real ExecRunner.
func WithRunner(r Runner) Option {
	return func(p *Plugin) { p.runner = r }
}

// WithCompletionComment configures Complete to post `comment` to the issue
// before closing it. Empty string (the default) skips the comment step.
func WithCompletionComment(comment string) Option {
	return func(p *Plugin) { p.completionComment = comment }
}

// New constructs a Plugin with the given options. Defaults (Runner =
// ExecRunner) are filled in here so options can override them.
func New(opts ...Option) *Plugin {
	p := &Plugin{runner: ExecRunner{}}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name reports the canonical plugin identifier.
func (p *Plugin) Name() string { return pluginName }

// rawItem mirrors the subset of gh's JSON output we consume. The struct
// tags follow gh's documented field names so a `gh ... --json` invocation
// drops directly into this shape.
type rawItem struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	UpdatedAt  string `json:"updatedAt"`
	URL        string `json:"url"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// List returns every open item (issue or PR) currently assigned to the
// authenticated user. Internally List runs `gh search issues` and `gh
// search prs` back-to-back; we deliberately do not parallelise the two
// because the daemon caller already runs sources concurrently and a stable
// ordering keeps Task slices diffable across runs.
func (p *Plugin) List(ctx context.Context) ([]source.Task, error) {
	return p.search(ctx, defaultQuery)
}

// Since returns items updated at or after checkpoint. An empty checkpoint
// degrades to List behaviour so first-run callers do not need a special
// case. The checkpoint format is the RFC3339 string gh's `updatedAt` field
// emits, so the daemon can store the max(updatedAt) it observed last
// iteration verbatim.
func (p *Plugin) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	if checkpoint == "" {
		return p.List(ctx)
	}
	q := defaultQuery + " updated:>=" + checkpoint
	return p.search(ctx, q)
}

// search drives the two `gh search` calls and merges the results in a
// deterministic order (issues, then PRs). Pulled out so List and Since
// share one parser path and the test suite can pin both kinds of call
// against the same fixtures.
func (p *Plugin) search(ctx context.Context, query string) ([]source.Task, error) {
	issues, err := p.runSearch(ctx, "issues", query)
	if err != nil {
		return nil, err
	}
	prs, err := p.runSearch(ctx, "prs", query)
	if err != nil {
		return nil, err
	}
	out := make([]source.Task, 0, len(issues)+len(prs))
	for _, it := range issues {
		out = append(out, toTask(it, "issue"))
	}
	for _, it := range prs {
		out = append(out, toTask(it, "pr"))
	}
	return out, nil
}

// runSearch executes one `gh search <kind> <query> --json ...` and parses
// the result. kind is "issues" or "prs" — the only two values the gh CLI
// accepts for our purposes — so we do not gate it behind a typed enum.
func (p *Plugin) runSearch(ctx context.Context, kind, query string) ([]rawItem, error) {
	stdout, _, err := p.runner.Run(ctx, "gh", "search", kind, query, "--json", jsonFields)
	if err != nil {
		return nil, fmt.Errorf("gh search %s: %w", kind, err)
	}
	var items []rawItem
	if err := json.Unmarshal(stdout, &items); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	return items, nil
}

// toTask lifts a rawItem into the cross-source Task shape. RawMetadata
// carries the source-specific extras (`type`, `updated_at`) so PR-71's
// daemon can advance the per-source checkpoint without re-querying gh.
func toTask(it rawItem, kind string) source.Task {
	return source.Task{
		Source:     pluginName,
		ExternalID: fmt.Sprintf("%s#%d", it.Repository.NameWithOwner, it.Number),
		Title:      it.Title,
		Body:       it.Body,
		SourcePath: it.URL,
		Done:       false, // is:open query never returns closed items.
		RawMetadata: map[string]any{
			"type":       kind,
			"updated_at": it.UpdatedAt,
			"number":     it.Number,
			"repository": it.Repository.NameWithOwner,
		},
	}
}

// Complete closes the GitHub issue identified by externalID. When a
// completion comment is configured, it is posted BEFORE the close so the
// audit trail records "marunage closed this — here is why" rather than a
// silent state change.
//
// PRs are out of scope for Phase 1: `gh issue close` will return an error
// if externalID points at a PR, and the wrapped error surfaces unchanged
// to the caller. A future PR that wants to close PRs would extend this to
// dispatch on RawMetadata["type"] — but that requires plumbing the type
// through the call site, which is not in PR-83's scope.
func (p *Plugin) Complete(ctx context.Context, externalID string) error {
	owner, repo, number, err := parseExternalID(externalID)
	if err != nil {
		return err
	}
	repoFlag := owner + "/" + repo
	numStr := strconv.Itoa(number)

	if p.completionComment != "" {
		if _, _, err := p.runner.Run(ctx, "gh",
			"issue", "comment", numStr,
			"--repo", repoFlag,
			"--body", p.completionComment,
		); err != nil {
			return fmt.Errorf("gh issue comment %s#%d: %w", repoFlag, number, err)
		}
	}
	if _, _, err := p.runner.Run(ctx, "gh",
		"issue", "close", numStr,
		"--repo", repoFlag,
	); err != nil {
		return fmt.Errorf("gh issue close %s#%d: %w", repoFlag, number, err)
	}
	return nil
}

// AuthStatus probes `gh auth status`. A zero exit means the user is logged
// in; any other outcome (non-zero exit, gh missing) is downgraded to
// AuthNotConfigured rather than a hard error. The CLI layer renders this
// into a user-facing "run gh auth login" hint without ever surfacing a Go
// error to the caller — same mapping internal/doctor uses for missing
// optional binaries.
func (p *Plugin) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	_, _, err := p.runner.Run(ctx, "gh", "auth", "status")
	if err != nil {
		if isBinaryNotFound(err) {
			return source.AuthNotConfigured, nil
		}
		return source.AuthNotConfigured, nil
	}
	return source.AuthAuthenticated, nil
}

// Setup verifies authentication. If gh is already authenticated, Setup is
// a no-op (idempotent); otherwise it returns ErrInteractiveSetupRequired
// because the Runner abstraction cannot drive `gh auth login`'s TTY-bound
// flow.
func (p *Plugin) Setup(ctx context.Context, _ source.SetupOptions) error {
	st, err := p.AuthStatus(ctx)
	if err != nil {
		return err
	}
	if st == source.AuthAuthenticated {
		return nil
	}
	return ErrInteractiveSetupRequired
}

// parseExternalID splits "<owner>/<repo>#<number>" into its three parts.
// Returns ErrInvalidExternalID if the shape is wrong. Centralising the
// parse keeps Complete (and any future Delete / reopen helper) honest
// about the same format invariant.
func parseExternalID(id string) (owner, repo string, number int, err error) {
	hash := strings.LastIndex(id, "#")
	if hash <= 0 || hash == len(id)-1 {
		return "", "", 0, fmt.Errorf("%w: %q", ErrInvalidExternalID, id)
	}
	prefix := id[:hash]
	numStr := id[hash+1:]
	slash := strings.Index(prefix, "/")
	if slash <= 0 || slash == len(prefix)-1 {
		return "", "", 0, fmt.Errorf("%w: %q", ErrInvalidExternalID, id)
	}
	owner = prefix[:slash]
	repo = prefix[slash+1:]
	n, parseErr := strconv.Atoi(numStr)
	if parseErr != nil || n <= 0 {
		return "", "", 0, fmt.Errorf("%w: %q", ErrInvalidExternalID, id)
	}
	return owner, repo, n, nil
}
