package notion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/haruotsu/marunage/internal/source"
)

// Typed sentinel errors. Callers branch on errors.Is rather than parsing
// strings; the CLI binding (PR-71 onwards) maps these to documented exit
// codes.
var (
	// ErrDatabaseIDRequired is returned by methods that need a target
	// database id when none was supplied via WithDatabaseID. Mirrors
	// markdown.ErrNoFilesConfigured: "the user did not point us at a
	// data source, so refuse loudly rather than return an empty list".
	ErrDatabaseIDRequired = errors.New("notion: database id is required")

	// ErrClientRequired is returned by methods that need a Client when
	// none was supplied via WithClient. Construction without a Client
	// is only useful for tests; production code paths must wire one.
	ErrClientRequired = errors.New("notion: client is required")

	// ErrNoSecretsConfigured is returned by Setup / AuthStatus when no
	// Secrets store is wired. Without a place to persist the integration
	// token there is nothing for these subcommands to do.
	ErrNoSecretsConfigured = errors.New("notion: secrets store is required for setup / auth-status")

	// ErrTokenRequired is returned by Setup when the configured
	// TokenProvider returns an empty string (the user did not supply a
	// token). Distinguished from ErrNoSecretsConfigured so the CLI can
	// prompt the user precisely.
	ErrTokenRequired = errors.New("notion: token must not be empty")
)

// Task is the source-side view of one Notion page. The struct is
// intentionally distinct from internal/store.Task and from source.Task: this
// is the inner / package-local shape, and the Adapter (adapter.go) lifts
// it into source.Task. The pattern matches markdown.Task / source.Task so a
// reviewer who already understands the markdown source recognises the
// architecture instantly.
type Task struct {
	ExternalID     string
	Title          string
	Done           bool
	SourcePath     string
	LastEditedTime string
	DatabaseID     string
}

// Checkpointer is the minimal key/value store the notion plugin needs to
// drive Since's last_edited_time gate. The interface is local so this
// package can build without depending on the SQLite-backed
// internal/store.KVStateRepo at compile time; PR-71 wires the real repo
// behind this seam exactly the way markdown.Plugin does.
type Checkpointer interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// SecretsStore is the small subset of internal/secrets.Store the plugin
// reaches for during Setup / AuthStatus. Defined locally so the package
// can be tested with an in-memory fake without standing up a real backend
// (file / age / keyring). The shape matches secrets.Store exactly so the
// production wiring is `notion.WithSecrets(secretsStore)` — no adapter.
type SecretsStore interface {
	Get(name string) (value string, ok bool, err error)
	Set(name, value string) error
}

// TokenProvider is the function Setup calls to obtain the user's Notion
// integration token. The default implementation returns the
// MARUNAGE_NOTION_TOKEN environment variable; tests inject a fixed string
// so they never depend on process state, and a future interactive CLI can
// supply a stdin-prompting variant without changing the Plugin contract.
type TokenProvider func(ctx context.Context, opts SetupOpts) (string, error)

// SetupOpts mirrors source.SetupOptions but is package-local so the inner
// Plugin does not have to import the source package. The Adapter performs
// the trivial value-copy.
type SetupOpts struct {
	NonInteractive bool
}

// Plugin is the entry point for the Notion source. Construct with New and
// reuse: today the struct is stateless (all I/O goes through Client /
// Checkpointer / SecretsStore), but a future caching layer would live here.
type Plugin struct {
	client        Client
	databaseID    string
	checkpointer  Checkpointer
	secrets       SecretsStore
	secretName    string
	tokenProvider TokenProvider
}

// Option is the functional-option shape New accepts. Same pattern as
// markdown.Option / store.Option so callers see consistent ergonomics.
type Option func(*Plugin)

// WithClient injects the Notion API client. Production callers pass in a
// *HTTPClient (http_client.go); tests pass in a fakeClient.
func WithClient(c Client) Option { return func(p *Plugin) { p.client = c } }

// WithDatabaseID pins the Notion database the plugin queries. PR-201
// scope is one database per Plugin instance; supporting multiple
// databases is a separate concern (a future PR could either configure a
// list here or instantiate multiple Plugins).
func WithDatabaseID(id string) Option { return func(p *Plugin) { p.databaseID = id } }

// WithCheckpointer wires the Since-gate persistence. Optional: when no
// Checkpointer is supplied, Since degrades to "behave like List" (no state
// is remembered between calls), which is the right behaviour for one-shot
// CLI invocations but useless for a long-running daemon.
func WithCheckpointer(c Checkpointer) Option { return func(p *Plugin) { p.checkpointer = c } }

// WithSecrets wires the credential store Setup writes to and AuthStatus
// reads from. Optional at the type level so test fixtures that only
// exercise List/Since do not have to plumb a fake.
func WithSecrets(s SecretsStore) Option { return func(p *Plugin) { p.secrets = s } }

// WithSecretName overrides the secrets-store key under which the token is
// persisted. Defaults to "notion:token". Configurable so a deployment that
// already namespaces secrets a particular way (e.g. "marunage/notion")
// can match its existing layout.
func WithSecretName(name string) Option { return func(p *Plugin) { p.secretName = name } }

// WithTokenProvider injects the token-retrieval function Setup calls.
// Defaults to reading MARUNAGE_NOTION_TOKEN.
func WithTokenProvider(tp TokenProvider) Option { return func(p *Plugin) { p.tokenProvider = tp } }

// defaultSecretName is the documented kv key under which the integration
// token lives. Centralised so Setup and AuthStatus cannot drift.
const defaultSecretName = "notion:token"

// defaultTokenEnv is the env var the default TokenProvider consults. The
// CLI-glue layer (a future PR) is expected to override this with a
// stdin-prompting provider when running interactively; the env var is the
// documented headless / CI path.
const defaultTokenEnv = "MARUNAGE_NOTION_TOKEN"

// envTokenProvider is the headless default the package wires when the
// caller does not pass WithTokenProvider. Returning the empty string is
// not an error here: Setup will surface ErrTokenRequired so the caller
// knows the env var was missing rather than silently writing "".
func envTokenProvider(_ context.Context, _ SetupOpts) (string, error) {
	return os.Getenv(defaultTokenEnv), nil
}

// New constructs a Plugin with the given options. Defaults are filled in
// here so options can override them before any method runs.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		secretName:    defaultSecretName,
		tokenProvider: envTokenProvider,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// List returns every page in the configured database. ExternalID is the
// Notion page id (UUID); Title comes from the page's title property; Done
// reflects the page's archived flag.
func (p *Plugin) List(ctx context.Context) ([]Task, error) {
	if p.databaseID == "" {
		return nil, ErrDatabaseIDRequired
	}
	if p.client == nil {
		return nil, ErrClientRequired
	}
	pages, err := p.client.QueryDatabase(ctx, p.databaseID, QueryOptions{})
	if err != nil {
		return nil, fmt.Errorf("notion: query database %s: %w", p.databaseID, err)
	}
	return p.toTasks(pages), nil
}

// Since returns pages whose last_edited_time is at or after the stored
// checkpoint. The checkpoint advances to the maximum last_edited_time
// observed in the new page set; if the new set is empty, the checkpoint
// stays put (so a transient empty result does not reset the gate).
//
// When no Checkpointer is configured Since degrades to List — there is no
// place to remember state, so returning everything is the only safe
// answer. This mirrors markdown.Plugin's behaviour exactly.
func (p *Plugin) Since(ctx context.Context) ([]Task, error) {
	if p.databaseID == "" {
		return nil, ErrDatabaseIDRequired
	}
	if p.client == nil {
		return nil, ErrClientRequired
	}
	if p.checkpointer == nil {
		return p.List(ctx)
	}

	stored, err := p.checkpointer.Get(ctx, checkpointKey(p.databaseID))
	if err != nil {
		return nil, fmt.Errorf("notion: read checkpoint: %w", err)
	}

	pages, err := p.client.QueryDatabase(ctx, p.databaseID, QueryOptions{OnOrAfter: stored})
	if err != nil {
		return nil, fmt.Errorf("notion: query database %s: %w", p.databaseID, err)
	}

	// Advance the checkpoint to the new max only if it is strictly later
	// than the stored value. Lex compare is sound for fixed-width ISO-8601
	// timestamps with millisecond precision (Notion's documented format).
	newMax := stored
	for _, pg := range pages {
		if strings.Compare(pg.LastEditedTime, newMax) > 0 {
			newMax = pg.LastEditedTime
		}
	}
	if newMax != "" && newMax != stored {
		if err := p.checkpointer.Set(ctx, checkpointKey(p.databaseID), newMax); err != nil {
			return nil, fmt.Errorf("notion: write checkpoint: %w", err)
		}
	}
	return p.toTasks(pages), nil
}

// toTasks lifts a slice of Page (the Client's view) into the package's
// Task type. Pulled out so List and Since share one definition; otherwise
// a future field addition would need two identical edits.
func (p *Plugin) toTasks(pages []Page) []Task {
	out := make([]Task, len(pages))
	for i, pg := range pages {
		out[i] = Task{
			ExternalID:     pg.ID,
			Title:          pg.Title,
			Done:           pg.Archived,
			SourcePath:     pg.URL,
			LastEditedTime: pg.LastEditedTime,
			DatabaseID:     p.databaseID,
		}
	}
	return out
}

// checkpointKey returns the kv_state key used to remember a database's
// last-seen last_edited_time. Namespaced with `notion:` so it cannot
// collide with future sources, and keyed off database id so swapping
// databases starts a fresh checkpoint.
func checkpointKey(databaseID string) string {
	return "notion:last_edited_time:" + databaseID
}

// Add creates a new page in the configured database and returns the
// resulting Task. notes is currently ignored — Notion's data model has no
// universal "notes" property, so the right mapping is a per-database
// configuration concern a future PR can address with WithNotesProperty.
// Accepting the argument today keeps the signature stable for Adapter.
func (p *Plugin) Add(ctx context.Context, title, _ string) (Task, error) {
	if p.databaseID == "" {
		return Task{}, ErrDatabaseIDRequired
	}
	if p.client == nil {
		return Task{}, ErrClientRequired
	}
	pg, err := p.client.CreatePage(ctx, p.databaseID, title)
	if err != nil {
		return Task{}, fmt.Errorf("notion: create page: %w", err)
	}
	// Page returned by the API is the source of truth: id / url / etc.
	// come back from Notion. Do not synthesise locally — a future Notion
	// schema bump would silently drift if we did.
	return Task{
		ExternalID:     pg.ID,
		Title:          pg.Title,
		Done:           pg.Archived,
		SourcePath:     pg.URL,
		LastEditedTime: pg.LastEditedTime,
		DatabaseID:     p.databaseID,
	}, nil
}

// Complete archives the page referenced by externalID. Notion has no
// page-level "done" boolean — the user-visible "complete" semantics are
// modelled either as a Status property (per database) or as archive. The
// minimum implementation here uses archive so the contract is honoured
// without requiring per-database configuration; a future PR that adds
// WithStatusProperty would change Complete to flip that property
// instead, leaving Delete responsible for archive.
func (p *Plugin) Complete(ctx context.Context, externalID string) error {
	if p.client == nil {
		return ErrClientRequired
	}
	if err := p.client.UpdatePage(ctx, externalID, true); err != nil {
		return fmt.Errorf("notion: archive (complete) %s: %w", externalID, err)
	}
	return nil
}

// Delete archives the page referenced by externalID. Notion's API does not
// expose a permanent delete — archived pages are recoverable from the
// trash by the user. Treating archive as the user-visible delete matches
// the way Notion's own UI behaves and keeps the source plugin idempotent
// (deleting an already-archived page is a no-op upstream).
func (p *Plugin) Delete(ctx context.Context, externalID string) error {
	if p.client == nil {
		return ErrClientRequired
	}
	if err := p.client.UpdatePage(ctx, externalID, true); err != nil {
		return fmt.Errorf("notion: archive (delete) %s: %w", externalID, err)
	}
	return nil
}

// Setup persists the user's Notion integration token into the configured
// SecretsStore. The TokenProvider is the seam between "how the token was
// obtained" (env var, stdin prompt, OAuth flow, ...) and "where it is
// stored". An empty provider response is rejected with ErrTokenRequired
// so the user does not silently end up with AuthNotConfigured after a
// successful-looking setup run.
func (p *Plugin) Setup(ctx context.Context, opts SetupOpts) error {
	if p.secrets == nil {
		return ErrNoSecretsConfigured
	}
	if p.tokenProvider == nil {
		// Should not happen — New defaults to envTokenProvider. The
		// branch exists for the WithTokenProvider(nil) case so a
		// hand-rolled override does not silently re-introduce the env
		// dependency we wanted to make explicit.
		return fmt.Errorf("notion: no token provider configured (wire WithTokenProvider)")
	}
	token, err := p.tokenProvider(ctx, opts)
	if err != nil {
		return fmt.Errorf("notion: token provider: %w", err)
	}
	if token == "" {
		return ErrTokenRequired
	}
	if err := p.secrets.Set(p.secretName, token); err != nil {
		return fmt.Errorf("notion: persist token: %w", err)
	}
	return nil
}

// AuthStatus reports the current credential state without performing any
// mutating I/O. The mapping is:
//
//	no token in secrets       -> AuthNotConfigured
//	token + smoke probe ok    -> AuthAuthenticated
//	token + ErrUnauthorized   -> AuthRevoked
//	token + ErrTokenExpired   -> AuthExpired
//
// Anything else (network error, 5xx) surfaces as an ordinary error so the
// caller can retry rather than misclassifying a transient failure as a
// permanent revocation.
func (p *Plugin) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	if p.secrets == nil {
		return "", ErrNoSecretsConfigured
	}
	if p.client == nil {
		return "", ErrClientRequired
	}
	_, ok, err := p.secrets.Get(p.secretName)
	if err != nil {
		return "", fmt.Errorf("notion: read secret: %w", err)
	}
	if !ok {
		return source.AuthNotConfigured, nil
	}
	switch err := p.client.UsersMe(ctx); {
	case err == nil:
		return source.AuthAuthenticated, nil
	case errors.Is(err, ErrTokenExpired):
		return source.AuthExpired, nil
	case errors.Is(err, ErrUnauthorized):
		return source.AuthRevoked, nil
	default:
		return "", fmt.Errorf("notion: smoke probe: %w", err)
	}
}
