# PR-71 `loop` / `daemon` design doc

> Status: implemented in `feat/pr-71-loop` (PR #40).
> Scope: orchestrate **discover → dispatch → render** as one tick (`marunage loop`)
> and manage that tick as a long-running background process (`marunage daemon`).

## Goals

1. Provide a single command that walks every Discovery plugin, materialises
   their tasks into `tasks`, fires `internal/dispatch.Dispatcher`, and refreshes
   `~/.marunage/view.md` — i.e. one closed OODA iteration.
2. Provide a `daemon` control surface that runs the same iteration on a
   timer in the background, with `start` / `stop` / `status` honouring a
   pidfile under `~/.marunage/daemon.pid`.
3. Honour the existing `lock_key` / `kv_state` concurrency contract so two
   loops (or a daemon racing a manual invocation) cannot dispatch the same
   row twice.
4. Stay safe under failure: a single broken plugin must not abort
   discovery; a `RunOnce` error must not stop the ticker; a panic in the
   dispatcher must not strand the kv_state lock.

## Non-goals (explicitly out of scope)

- LaunchAgent / systemd / cron unit-file generation. The pidfile +
  `setsid`-detached subprocess covers cross-platform ergonomics; native
  init-system integration is a follow-up PR.
- Triage. The loop emits raw `source.Task` records into the queue; PR-72's
  triage skill is the next layer that decides priority / waiting_human.
- Per-source rate limiting. Discovery plugins are the right place for
  upstream-specific quota handling.

## Architecture

```
                ┌──────────────────────┐
                │     marunage CLI     │
                │  (loop / daemon)     │
                └─────────┬────────────┘
                          │
                          ▼
                ┌──────────────────────┐
                │   internal/loop      │
                │   Loop.RunOnce       │
                └────┬───────┬────┬────┘
                     │       │    │
        ┌────────────┘       │    └──────────────┐
        ▼                    ▼                   ▼
┌───────────────┐   ┌────────────────┐   ┌────────────────┐
│  Discovery    │   │   Dispatch     │   │    Render      │
│  walk reg.,   │   │  dispatch.     │   │  internal/     │
│  upsert tasks │   │  Dispatcher.   │   │  render +      │
│               │   │  Run()         │   │  view.md       │
└───────────────┘   └────────────────┘   └────────────────┘
        ▲                    ▲                    ▲
        └─── source.Registry ┘────── audit.log ───┘
        (kv_state checkpoints)
```

### Phases (per `RunOnce`)

1. **Lock acquire (optional).** When `WithLockKey` is set, attempt a
   kv_state `CompareAndSwap` from absent → "held". Lose the race → return
   `ErrLockBusy` so the caller (CLI / next tick) skips this iteration.
2. **Audit `loop.start`.**
3. **Discover.** For each registered plugin name:
   - Resolve plugin from registry.
   - If plugin satisfies `source.Sincer`, call `Since(ctx, checkpoint)`
     where checkpoint is `kv_state["loop.checkpoint.<name>"]` (empty on
     first run).
   - Else call `List(ctx)`.
   - Per task: `repo.Insert(...)`, swallow `ErrDuplicateExternalID` for
     idempotent re-discovery; record `loop.discover.fail` for any other
     insert error and abort the plugin (not the whole tick).
   - On success: stamp `kv_state["loop.checkpoint.<name>"] = now`.
   - Per-plugin error → audit `loop.discover.fail` and continue with the
     next plugin.
4. **Dispatch.** `dispatcher.Run(ctx, RunOptions{MaxParallel: cfg})`.
   Returns the dispatcher's typed error; loop wraps with
   `loop.dispatch.fail` audit + `fmt.Errorf` so the caller (CLI / Run
   ticker) can decide.
5. **Render.** `Render(ctx)` writes `~/.marunage/view.md` atomically via
   tmpfile + rename.
6. **Audit `loop.end`.** Released defer, value carries the error string
   (or "") so a forensic reader sees pass/fail per tick.
7. **Lock release.** Deferred at top of `RunOnce` so even a panic returns
   the kv_state row to "absent". Best-effort; failure is audited as
   `loop.lock.release.fail`.

### Run (interval ticker)

`Loop.Run(ctx, interval)`:
- `interval ≤ 0` → `ErrInvalidInterval` immediately.
- Tick once on entry, then every `interval` until `ctx.Done()`.
- A `RunOnce` error is recorded as `loop.tick.fail` and **swallowed** —
  the ticker keeps going. Only a `ctx.Canceled` / `ctx.DeadlineExceeded`
  inside `RunOnce` returns the loop cleanly.

### Daemon control

`fileBackedDaemon` (`internal/cli/daemon.go`):
- pidfile at `~/.marunage/daemon.pid` (atomic tmp + rename).
- `Start(args)`:
  - Status check; refuse if already-live; clear stale pidfile.
  - `os.OpenFile(daemon.log, APPEND|CREATE|WRONLY, 0o600)`.
  - `exec.Command(self, "--config", cfg, "loop", args...)` with
    stdout/stderr → log file, `SysProcAttr.Setsid = true` (POSIX) to
    detach from the controlling terminal.
  - `cmd.Start()` + `cmd.Process.Release()`; write the pid.
- `Stop(timeout)`:
  - SIGTERM, poll every 50 ms until the kernel reports ESRCH or the
    timeout elapses; on timeout escalate to SIGKILL.
- `Status()`:
  - Read pidfile, `Signal(0)` probe. EPERM means alive (foreign UID),
    ESRCH means dead, no pidfile means never started.

## Concurrency model

- The kv_state-backed `WithLockKey` gives the loop process-level
  exclusion. The dispatcher's `ClaimWorkspace` + `AcquireLock` give
  per-row exclusion. The two compose: only one loop is running at a
  time, and within that loop the dispatcher already serialises on
  `lock_key` for tasks that share one.
- `RunOnce` is **not** safe for concurrent invocation on the same
  `*Loop` instance without `WithLockKey`. Callers without a lock_key
  must serialise externally.
- The `Run` ticker uses `time.NewTicker`; a pause longer than `interval`
  (e.g. system sleep) drops missed ticks rather than firing a burst —
  matching the spec's "OODA on a cadence" semantics.

## Failure isolation matrix

| Failure                                    | Effect on tick      | Audit action              |
|--------------------------------------------|---------------------|---------------------------|
| Plugin `Get` returns ErrPluginNotFound     | skip plugin         | loop.discover.fail        |
| Plugin `List` / `Since` returns error      | skip plugin         | loop.discover.fail        |
| Insert returns ErrDuplicateExternalID      | skip task, continue | (none — idempotency)      |
| Insert returns other error                 | abort plugin        | loop.discover.fail        |
| Checkpoint `Set` fails                     | continue            | loop.checkpoint.fail      |
| Dispatcher `Run` returns error             | abort tick          | loop.dispatch.fail + tick |
| Render returns error                       | abort tick          | loop.render.fail + tick   |
| Lock acquire fails (DB error)              | return error        | (raw error)               |
| Lock release fails                         | continue            | loop.lock.release.fail    |
| Panic anywhere inside RunOnce              | propagate           | lock release deferred     |

## Test strategy

- `internal/loop/loop_test.go` covers construction guards (N1-N5),
  RunOnce orchestration (O1-O8), Run interval semantics (R1, R3, R4),
  kv_state lock-key concurrency (L1-L4). Race detector clean.
- `internal/cli/loop_test.go` and `daemon_test.go` cover the CLI surface
  via the `loopFactoryHook` / `daemonControlHook` test seams; tests do
  not spawn real subprocesses.

## Open questions

- Should daemon `start` write a structured marker (last-start timestamp,
  argv used) into kv_state so `status` can report it? Today the only
  source of truth is the pidfile.
- Should `loop --once` flush any in-flight dispatch awaits before
  returning? Today it returns as soon as `Dispatcher.Run` returns; the
  dispatched cmux workspaces continue running independently — which is
  the same contract `marunage dispatch` already has.
- LaunchAgent / systemd unit generation is left for a future PR; the
  spec mentions both. The current pidfile approach is portable and
  enough for "I want this to keep running while I work", but a native
  unit gives the operator native restart-on-boot semantics.
