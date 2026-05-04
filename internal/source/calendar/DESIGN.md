# PR-81 Google Calendar Discovery source — design

> Layer-local design doc for the read-only Google Calendar source plugin
> introduced in PR-81 (per `docs/pr_split_plan.md` lines 595–599 and
> `docs/requirement.md` row #3 of the Discovery sources table).

## Scope

`internal/source/calendar/` only. Phase 2, parallel with PR-80 / PR-82+.
This PR does NOT touch `internal/cli/discover.go`, the queue layer, or any
existing source plugin.

## Contract

The package implements the standard Discovery `Plugin` contract from
`internal/source/source.go`:

| Method        | Status   | Notes                                                         |
|---------------|----------|---------------------------------------------------------------|
| `Name()`      | required | Returns `"calendar"` (exported as `PluginName`).              |
| `List(ctx)`   | required | Today's events, declined invites filtered, in client order.   |
| `Setup(ctx)`  | required | Forwards to `Client.Setup`; smoke-test on success.            |
| `AuthStatus`  | required | Forwards to `Client.Status`; `not_configured` when no client. |
| `Add` / `Complete` / `Delete` / `Since` | **NOT implemented** | Read-only contract enforced at type level. |

## Day boundary

Per PR-81 spec: switchover at local midnight, computed every call.

```
timeMin = startOfDay(now())   // time.Date(y, m,   d,   0,0,0,0, loc)
timeMax = nextMidnight(now()) // time.Date(y, m,   d+1, 0,0,0,0, loc)
```

Both bounds go through `time.Date` rather than `Truncate(24h)` or
`startOfDay(now).Add(24*time.Hour)`. The `Truncate` form quantises in UTC
and shifts the boundary off local midnight in any non-zero offset; the
`+24h` form is wall-clock arithmetic and overshoots/undershoots local
midnight by an hour on DST spring-forward / fall-back days. Calendar
arithmetic via `time.Date` is the only form that always lands on next
local midnight regardless of DST. (See `TestPluginListBoundarySurvivesSpringForwardDST`.)

`now()` is the injected clock function. Production wires `time.Now`,
which reads `time.Local` at process start; a daemon that survives a TZ
change without restart will keep using the original `time.Local`. That
is acceptable for PR-81 — moving offices is rare enough — but is called
out in test_list as a future test case if multi-timezone daemons become
a goal.

A long-running daemon naturally rolls over because `now()` is read on
every List call; no cache, no scheduled re-init.

## Event handling

| Type            | Discriminator                             | RawMetadata fields                       |
|-----------------|-------------------------------------------|------------------------------------------|
| Regular event   | `start.dateTime` populated                | `start`, `end` (RFC3339 strings)         |
| All-day event   | `start.date` populated (`dateTime` empty) | `start_date`, `end_date` (YYYY-MM-DD)    |
| Declined event  | `attendees[self].responseStatus=declined` | **filtered out** in `Plugin.List`        |
| Cancelled event | `event.status=cancelled`                  | **filtered out** in `Plugin.List` — surfaces from `singleEvents=true` for removed exceptions of recurring series |

`AttendeeStatus` is set to the response of the attendee with `self=true`;
events without a self-attendee leave it empty (the user owns the event).

## External I/O seam

```
Client interface
  ListEvents(ctx, timeMin, timeMax) ([]Event, error)
  Status(ctx) (source.AuthStatus, error)
  Setup(ctx, opts) error
```

- Production: `GWSClient` shells out to `gws calendar events list`
  (`requirement.md` delegate-cli policy). Runner is injectable
  (`WithRunner`) so JSON parsing and command-shape are tested offline.
- Tests: in-memory `fakeClient` lives in `calendar_test.go`.
- Future swap: a google-api-go-client implementation can drop in without
  touching `Plugin` or `Adapter`.

### gws command shape

```
gws calendar events list \
  --params '{"calendarId":"primary","singleEvents":true,"orderBy":"startTime",
             "timeMin":"<RFC3339>","timeMax":"<RFC3339>"}' \
  --format json
```

Response shape: standard Google Calendar v3 `events.list`
(`{kind, items[]}`) — see `gws schema calendar.events.list`.

### Subprocess and PII policy

- `DefaultRunner` honours `ctx` via `exec.CommandContext` so a caller
  cancelling List / Setup kills the gws subprocess (PR-71 daemon will
  set per-call deadlines).
- The runner deliberately does NOT wrap captured stderr into the error
  message. gws diagnostics can carry OAuth refresh tokens, attendee
  emails, or calendar ids; bundling them into a wrapped error would
  leak that into slog / audit logs. We surface only the binary name and
  the exit code. Callers that need the raw stderr for debugging run gws
  directly.
- `Event.Description` / `Event.Location` / attendee emails are NOT
  emitted to logs by this package — only `id`, `summary`, and the time
  bounds appear in any structured log fields a future PR adds. The
  Adapter's RawMetadata mapping is the single place those fields enter
  the queue, and PR-71's audit JSONL needs to apply field-level redaction
  there; documenting the boundary here so that follow-up does not
  accidentally regress it.
- `Status` shells out to `gws calendar calendarList list` (cheap, idempotent)
  and downgrades any runner error to `AuthNotConfigured` for
  `AuthStatus` callers. Setup's interactive smoke-test path calls the
  underlying probe directly and surfaces the error verbatim, so a
  binary-missing / network failure is not masked behind the routine
  "please run `gws auth login`" hint.

### Setup policy

`Setup(NonInteractive=true)` returns a typed error pointing the user at
`gws auth login`. We deliberately do NOT spawn a browser or stdin prompt
from inside the binary — that policy lives in gws, and re-implementing it
would violate `requirement.md` lines 461 (delegate-cli).

## Manifest

`plugin.toml` (embedded via `go:embed`):

```toml
[plugin]
name = "calendar"
version = "0.1.0"
sync_mode = "read-only"
capabilities = ["list", "setup", "auth-status"]
```

`source.ValidateAgainstManifest` runs at `RegisterBuiltin` time so any
drift between the manifest and the adapter's interface set fails at
startup, not at first dispatch. Since the calendar adapter does NOT
implement `Adder` / `Completer` / `Deleter` / `Sincer`, declaring any of
those capabilities here would intentionally crash registration.

## Source.Task mapping

```
Source       = "calendar"
ExternalID   = Event.ID                    // Google Calendar event id
Title        = Event.Summary
Body         = Event.Description
SourcePath   = Event.HTMLLink               // calendar.google.com URL
Done         = false                         // calendar is read-only
RawMetadata.all_day         = bool
RawMetadata.attendee_status = string
RawMetadata.calendar_id     = string (optional)
RawMetadata.location        = string (optional)
RawMetadata.start / end                = RFC3339      (regular events)
RawMetadata.start_date / end_date      = YYYY-MM-DD   (all-day events)
```

The (source, external_id) pair is the queue's UNIQUE index — PR-71's
materialiser will dedup on it.

## Out of scope (deferred)

- Multi-calendar (only `primary` for PR-81).
- Recurring-event boundary edge cases beyond what `singleEvents=true` flattens.
- Webhook / push subscription (Phase 4).
- Triage hand-off (`judgment_reason` writing — that's PR-71's job).
- CLI wiring of `discover --source calendar` (PR-71+).

## Test list

See `test_list.md`. Every Red→Green→Refactor cycle is a single ticked
item; the file is the source of truth for "what behaviour is locked in."
