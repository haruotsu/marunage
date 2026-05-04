# PR-81 calendar source — t_wada test list

## C. Plugin (calendar.go)

- [x] C1. `New()` constructs a Plugin; `Plugin.Name()` returns "calendar".
- [x] C2. `Plugin.List` with no client returns `ErrClientRequired`.
- [x] C3. `Plugin.List` calls `client.ListEvents` with `timeMin = startOfDay(now)` and `timeMax = startOfDay(now)+24h`.
- [x] C4. `Plugin.List` uses local timezone for boundary (`now.Location()`), not UTC.
- [x] C5. `Plugin.List` rolls over at midnight: `now = today 23:59 → window = [today 00:00, tomorrow 00:00)`; `now = tomorrow 00:00:01 → window = [tomorrow 00:00, day-after 00:00)`.
- [x] C6. `Plugin.List` filters out events whose AttendeeStatus is `"declined"`.
- [x] C7. `Plugin.List` keeps regular events (`AllDay=false`) and all-day events (`AllDay=true`) verbatim, in client-provided order.
- [x] C8. `Plugin.List` returns empty (non-nil error) when client returns empty slice.
- [x] C9. `Plugin.List` propagates client error wrapped in `calendar: list events: ...`.
- [x] C10. `Plugin.Setup` with no client returns `ErrClientRequired`.
- [x] C11. `Plugin.Setup` forwards opts to `client.Setup`; propagates error.
- [x] C12. `Plugin.AuthStatus` with no client returns `source.AuthNotConfigured` and nil error.
- [x] C13. `Plugin.AuthStatus` delegates to `client.Status`.
- [x] C14. `Plugin.List` filters out events whose `Status == "cancelled"` (singleEvents=true exception items must not become queue rows).
- [x] C15. `Plugin.List` boundary survives DST spring-forward: on a 23-wall-hour day the window must still end at *next local midnight*, not at `timeMin + 24h`.

## A. Adapter (adapter.go)

- [x] A1. `Adapter.Name()` returns "calendar".
- [x] A2. `Adapter.List` converts a regular event to `source.Task` with Source="calendar", ExternalID=ev.ID, Title=ev.Summary, Body=ev.Description, SourcePath=ev.HTMLLink, Done=false.
- [x] A3. `Adapter.List` regular event RawMetadata carries `all_day=false`, `attendee_status`, `start` (RFC3339), `end` (RFC3339).
- [x] A4. `Adapter.List` all-day event RawMetadata carries `all_day=true`, `start_date` (YYYY-MM-DD), `end_date` (YYYY-MM-DD).
- [x] A5. `Adapter.List` populates `RawMetadata.location` only when `ev.Location != ""`.
- [x] A6. `Adapter.Setup` delegates to inner.
- [x] A7. `Adapter.AuthStatus` delegates to inner.
- [x] A8. `*Adapter` does NOT satisfy Adder, Completer, Deleter, or Sincer (compile-time guard).

## B. Built-in registration (builtin.go)

- [x] B1. `Manifest()` returns name="calendar", version="0.1.0", sync_mode="read-only", capabilities=[list, setup, auth-status].
- [x] B2. `RegisterBuiltin(r, WithClient(fake))` registers the adapter and survives `ValidateAgainstManifest`.
- [x] B3. `RegisterBuiltin` twice returns `ErrPluginAlreadyRegistered`.
- [x] B4. `RegisterBuiltin(nil, ...)` returns a non-nil error.

## G. GWS-backed real client (gws_client.go)

- [x] G1. `(*GWSClient).ListEvents` shells out to `gws calendar events list --params {...}` with `singleEvents=true`, `orderBy=startTime`, `timeMin`/`timeMax` set to RFC3339, calendarId="primary".
- [x] G2. `(*GWSClient).ListEvents` parses the standard Google Calendar v3 response (`items[]`) into Events: regular event → AllDay=false with parsed StartDateTime/EndDateTime; all-day event (date-only) → AllDay=true with AllDayStart/AllDayEnd YYYY-MM-DD.
- [x] G3. `(*GWSClient).ListEvents` sets AttendeeStatus to the `responseStatus` of the attendee whose `self=true`; events without a self-attendee leave AttendeeStatus="".
- [x] G4. `(*GWSClient).ListEvents` returns wrapped error when the runner fails (non-zero exit).
- [x] G5. `(*GWSClient).Status` runs `gws calendar calendarList list --params {"maxResults":1}` and returns AuthAuthenticated on success, AuthNotConfigured on failure.
- [x] G6. `(*GWSClient).Setup(NonInteractive=true)` returns an error explaining gws auth must already be configured.
- [x] G7. `(*GWSClient).ListEvents` parses `status` per item and surfaces it on `Event.Status` (does not pre-filter; that is the Plugin's job).
- [x] G8. `(*GWSClient).Setup(NonInteractive=false)` runs the calendarList smoke test directly and surfaces the runner error verbatim — Status's "downgrade I/O failure to AuthNotConfigured" must NOT mask binary-missing / network errors here.

## Cross-cutting

- [x] `make test` (with -race) green across the whole repo.
- [x] `make lint` green.
