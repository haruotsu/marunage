# PR-81 calendar source — t_wada test list

## C. Plugin (calendar.go)

- [ ] C1. `New()` constructs a Plugin; `Plugin.Name()` returns "calendar".
- [ ] C2. `Plugin.List` with no client returns `ErrClientRequired`.
- [ ] C3. `Plugin.List` calls `client.ListEvents` with `timeMin = startOfDay(now)` and `timeMax = startOfDay(now)+24h`.
- [ ] C4. `Plugin.List` uses local timezone for boundary (`now.Location()`), not UTC.
- [ ] C5. `Plugin.List` rolls over at midnight: `now = today 23:59 → window = [today 00:00, tomorrow 00:00)`; `now = tomorrow 00:00:01 → window = [tomorrow 00:00, day-after 00:00)`.
- [ ] C6. `Plugin.List` filters out events whose AttendeeStatus is `"declined"`.
- [ ] C7. `Plugin.List` keeps regular events (`AllDay=false`) and all-day events (`AllDay=true`) verbatim, in client-provided order.
- [ ] C8. `Plugin.List` returns empty (non-nil error) when client returns empty slice.
- [ ] C9. `Plugin.List` propagates client error wrapped in `calendar: list events: ...`.
- [ ] C10. `Plugin.Setup` with no client returns `ErrClientRequired`.
- [ ] C11. `Plugin.Setup` forwards opts to `client.Setup`; propagates error.
- [ ] C12. `Plugin.AuthStatus` with no client returns `source.AuthNotConfigured` and nil error.
- [ ] C13. `Plugin.AuthStatus` delegates to `client.Status`.

## A. Adapter (adapter.go)

- [ ] A1. `Adapter.Name()` returns "calendar".
- [ ] A2. `Adapter.List` converts a regular event to `source.Task` with Source="calendar", ExternalID=ev.ID, Title=ev.Summary, Body=ev.Description, SourcePath=ev.HTMLLink, Done=false.
- [ ] A3. `Adapter.List` regular event RawMetadata carries `all_day=false`, `attendee_status`, `start` (RFC3339), `end` (RFC3339).
- [ ] A4. `Adapter.List` all-day event RawMetadata carries `all_day=true`, `start_date` (YYYY-MM-DD), `end_date` (YYYY-MM-DD).
- [ ] A5. `Adapter.List` populates `RawMetadata.location` only when `ev.Location != ""`.
- [ ] A6. `Adapter.Setup` delegates to inner.
- [ ] A7. `Adapter.AuthStatus` delegates to inner.
- [ ] A8. `*Adapter` does NOT satisfy Adder, Completer, Deleter, or Sincer (compile-time guard).

## B. Built-in registration (builtin.go)

- [ ] B1. `Manifest()` returns name="calendar", version="0.1.0", sync_mode="read-only", capabilities=[list, setup, auth-status].
- [ ] B2. `RegisterBuiltin(r, WithClient(fake))` registers the adapter and survives `ValidateAgainstManifest`.
- [ ] B3. `RegisterBuiltin` twice returns `ErrPluginAlreadyRegistered`.
- [ ] B4. `RegisterBuiltin(nil, ...)` returns a non-nil error.

## G. GWS-backed real client (gws_client.go)

- [ ] G1. `(*GWSClient).ListEvents` shells out to `gws calendar events list --params {...}` with `singleEvents=true`, `orderBy=startTime`, `timeMin`/`timeMax` set to RFC3339, calendarId="primary".
- [ ] G2. `(*GWSClient).ListEvents` parses the standard Google Calendar v3 response (`items[]`) into Events: regular event → AllDay=false with parsed StartDateTime/EndDateTime; all-day event (date-only) → AllDay=true with AllDayStart/AllDayEnd YYYY-MM-DD.
- [ ] G3. `(*GWSClient).ListEvents` sets AttendeeStatus to the `responseStatus` of the attendee whose `self=true`; events without a self-attendee leave AttendeeStatus="".
- [ ] G4. `(*GWSClient).ListEvents` returns wrapped error when the runner fails (non-zero exit).
- [ ] G5. `(*GWSClient).Status` runs `gws calendar calendarList list --params {"maxResults":1}` and returns AuthAuthenticated on success, AuthNotConfigured on failure.
- [ ] G6. `(*GWSClient).Setup(NonInteractive=true)` returns an error explaining gws auth must already be configured (we do not invoke a browser flow from a non-interactive caller).
