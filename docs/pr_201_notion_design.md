# PR-201 Notion Discovery Source Plugin — Design

> Status: implementation landed on `feat/pr-201-notion`. This document captures
> the design decisions for review and future maintenance.

## Goal

Implement the Notion Discovery source plugin promised in
`docs/pr_split_plan.md` PR-201. The plugin queries one Notion database per
construction and exposes its pages through the generic `source.Plugin`
contract from PR-70.

Minimum scope:

- `List` — query database pages
- `Since` — last_edited_time checkpoint, server-side `on_or_after` filter
- `Setup` / `AuthStatus` — Internal Integration token persisted via
  `internal/secrets`
- `Add` / `Complete` / `Delete` — bidirectional (archive doubles as the
  user-visible delete; Notion has no permanent delete)
- ExternalID = Notion page id (UUID)

The plugin lives under `internal/source/notion/` and follows
`internal/source/markdown/` as the reference pattern.

## Architecture

```
internal/source/notion/
  client.go        Client interface + Page / QueryOptions value types + fakeClient
  http_client.go   production HTTPClient against api.notion.com
  notion.go        Plugin core (List / Since / Setup / AuthStatus / Add / Complete / Delete)
  adapter.go       source.Plugin + source.Sincer + Adder + Completer + Deleter
  builtin.go       go:embed plugin.toml + RegisterBuiltin
  plugin.toml      capabilities: list / setup / auth-status / since / add / complete / delete
```

### Client interface seam

Notion API access is hidden behind a small `Client` interface so unit tests
can use a `fakeClient` and never touch real HTTP. The seam is justified by
the brief: `Notion API 通信は interface 抽象化 + fake で単体テスト`.

```go
type Client interface {
    QueryDatabase(ctx, databaseID, opts) ([]Page, error)
    UsersMe(ctx) error
    CreatePage(ctx, databaseID, title) (Page, error)
    UpdatePage(ctx, pageID, archived bool) error
}
```

### Plugin construction (functional options)

```go
notion.New(
    notion.WithClient(httpClient),         // required for real I/O
    notion.WithDatabaseID("..."),          // required
    notion.WithCheckpointer(kvStateRepo),  // optional — Since degrades to List without it
    notion.WithSecrets(secretsStore),      // required for Setup / AuthStatus
    notion.WithSecretName("notion:token"), // optional override
    notion.WithTokenProvider(provider),    // injects how Setup obtains the token
)
```

Mirrors `markdown.New(...)` so a reviewer who already understands one source
recognises the other immediately.

### `last_edited_time` checkpoint

`Since` reads the stored checkpoint from `Checkpointer` (kv key
`notion:last_edited_time:<database_id>`) and asks Notion for pages whose
`last_edited_time` is `>=` that value. The new max observed in the response
becomes the next checkpoint. Empty results leave the checkpoint untouched
(no regression to zero); older results never overwrite a later checkpoint
(monotone advance).

Comparison is **lexicographic** on the fixed-width ISO-8601 string Notion
returns, so we never lose precision through `time.Parse` round-trips. This
matches the way Notion's own filter compares timestamps server-side.

### AuthStatus mapping

```
no token in secrets       -> AuthNotConfigured
token + smoke probe ok    -> AuthAuthenticated
token + ErrUnauthorized   -> AuthRevoked
token + ErrTokenExpired   -> AuthExpired
network / 5xx error       -> ordinary error (caller retries)
```

The smoke probe is `GET /v1/users/me`. Notion's documented error `code`
field distinguishes `expired_token` (OAuth refresh path) from
`unauthorized` (revoked / re-grant path).

### Title extraction

Notion stores a database row's title under whichever property the user
labelled as the title column ("Name" / "Title" / "Task" / ...). The
`extractTitle` helper iterates `properties` looking for the entry whose
type is `"title"` and concatenates its rich-text segments. Untitled pages
return `""` rather than erroring — an untitled page is real upstream
state, not a programmer bug.

### Bidirectional path (Add / Complete / Delete)

Notion has no permanent delete; archived pages are recoverable from the
trash by the user. Both `Complete` and `Delete` therefore call
`UpdatePage(id, archived=true)`. A future PR that adds
`WithStatusProperty` would change `Complete` to flip a status column
instead, leaving `Delete` responsible for archive.

`Add` posts to `/v1/pages` with a `parent.database_id` and a single title
property. The default property name `"Name"` matches Notion's UI default;
a future `WithTitleProperty` option would let callers override.

### Pagination

`HTTPClient.QueryDatabase` walks the documented `has_more` /
`next_cursor` pagination internally so callers see one flat slice. A
future streaming variant could be added without breaking the interface.

## Tradeoffs

### One database per Plugin instance

PR-201 scope is one database per `notion.Plugin`. Supporting multiple
databases per Plugin would push the per-database checkpoint key into
every method signature; a cleaner answer is to instantiate multiple
Plugins (one per database). A future PR could add a multi-database
adapter on top if needed.

### `archived` as both Done and Delete

Treating archive as the user-visible delete preserves Notion's UI
semantics but conflates "user marked complete" with "user removed". A
future `WithStatusProperty` would split them.

### Lex compare on `last_edited_time`

Sound only because Notion always returns fixed-width millisecond
ISO-8601. If Notion ever changed the format we would need to switch to
`time.Parse`. The current approach saves a parse round-trip and avoids
any precision loss.

### Manifest-declared capabilities

`plugin.toml` declares all seven capabilities. `RegisterBuiltin` runs
`source.ValidateAgainstManifest` so capability ↔ interface drift is
caught at startup, not first dispatch. The manifest is the single source
of truth for what the source can do.

## Security / Threat Model

Reviewed against `OpenClaw §11.1` and OWASP A2 / A10. Invariants the
plugin enforces (and the production glue must continue to honour):

- **TLS pin**: production callers MUST construct `NewHTTPClient` with the
  default `https://api.notion.com` baseURL; `InsecureSkipVerify` is
  forbidden. The httptest path used by unit tests is the only legitimate
  exception.
- **Token isolation**: the integration token MUST be persisted via
  `internal/secrets.Store` (OS Keychain / DPAPI / libsecret on the
  default `auto` backend); never written to `kv_state` or any plaintext
  file outside the secrets backend's ownership.
- **Token never in URL / log / error**: `Authorization: Bearer <token>`
  is the only place the token appears in the wire format. The
  `decodeErrorResponse` path passes Notion's API-supplied
  `body.Message` through but never the request URL with embedded
  credentials. Logging sites at higher layers must continue to use the
  redaction path.
- **Body size cap**: every response body is read through an
  `io.LimitReader(maxResponseBytes=1MiB)` to guard against a hostile or
  corrupted upstream forcing unbounded allocation while we parse JSON.
- **Default HTTP timeout**: 30 s on the auto-constructed `*http.Client`
  so a hung upstream cannot pin a daemon goroutine forever even when the
  caller passes `context.Background()`.

## Reliability / Daemon-runtime

- **Rate limiting (429)**: `decodeErrorResponse` surfaces a typed
  `*RateLimitedError` (wrapping `ErrRateLimited`) carrying the
  `Retry-After` delay parsed from the response header. Daemon callers
  should sleep that delay before retrying. A future PR will add the
  in-client retry loop; today the typed error is the seam.
- **Cancellation**: `QueryDatabase` checks `ctx.Err()` before each
  cursor round trip so daemon shutdown cannot be delayed by a cooperative
  upstream that keeps returning `has_more=true`.
- **Long-sleep recovery**: a future `WithMaxLookback(d)` option will
  clamp the stored checkpoint when it is older than `d`, so a laptop
  resuming from days of sleep does not stampede Notion with a
  multi-thousand-page replay.

## Test strategy (t_wada)

Two layers, both driven by `.test-list-notion.md`:

1. **Unit (fakeClient)** — deterministic, in-memory; the seam for List /
   Since / Setup / AuthStatus / Add / Complete / Delete logic. No HTTP.
2. **Integration (httptest)** — the production `HTTPClient` against
   `httptest.NewServer`; covers the request shape (auth header,
   Notion-Version, on_or_after filter), response decoding (multi-segment
   rich text, archived flag, paginated cursor walk), and the typed-error
   mapping (401 unauthorized vs expired_token, 429 rate_limited with
   Retry-After, ctx cancellation between cursor pages).

`go test -race ./internal/source/notion/...` is part of the cross-cutting
checklist; `make test` and `make lint` are green at every commit on this
branch.

## Phase / rollback

- **Phase**: lands as Source plugin #2 alongside markdown (PR-50). Both
  are registered as built-ins; either can be omitted at link time by
  not calling its `RegisterBuiltin` from the binary's plugin-list (a
  future PR adds that wiring).
- **Rollback**: revert this PR or comment out the `notion.RegisterBuiltin`
  call site. The plugin is self-contained under
  `internal/source/notion/`; nothing else in the codebase imports it
  yet.

## Open questions

- Should `Setup` accept an OAuth flow (PKCE) in a follow-up, or is
  Internal Integration token enough? Brief says either is fine.
- Do we want a per-database `WithStatusProperty` option for `Complete`?
  Useful for users who model Done as a Status column.
- How should the CLI prompt the user for the database id at first run?
  Probably a separate PR that wires the CLI; the plugin already accepts
  it via `WithDatabaseID`.

## TDD

Driven by `.test-list-notion.md` (t_wada style: Red → Green → Refactor).
Every entry is now ticked.

## Verification

- `make test` green (all packages).
- `make lint` clean (`golangci-lint run ./...` → 0 issues).
- `go test -race ./internal/source/notion/...` clean.
- httptest-driven HTTP client tests cover request shape, response
  decoding, cursor pagination, and the typed-error mapping for both 401
  codes.

## References

- `docs/pr_split_plan.md` PR-201 section
- `internal/source/markdown/` reference pattern
- `internal/secrets/` token storage
- `internal/store/kvstate.go` Checkpointer wiring at runtime
