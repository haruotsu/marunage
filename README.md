# marunage

> An autonomous task-execution OSS that lets you "丸投げ" (delegate completely)
> the steady stream of tasks arriving from Slack, email, GitHub, calendars, and
> custom sources — while keeping observation, intervention, and rollback always
> within reach.

`marunage` runs an OODA loop on top of Claude Code sessions managed by
[`cmux`](https://github.com/manaflow-ai/cmux): a Discovery layer polls each
configured source, an Orient/Decide layer triages and prioritises tasks in a
local SQLite queue, and an Execution layer launches one interactive Claude
session per task in an isolated cmux workspace.

## Status

The CLI skeleton is in place: every Phase 1 subcommand from
[`docs/requirement.md`](./docs/requirement.md) (`init`, `doctor`, `setup`,
`add`, `dispatch`, `web`, `config`, `daemon`, …) is wired through cobra and
listed in `marunage --help`. Implemented commands so far:

- `marunage config get|set` — read / write `~/.marunage/config.toml`
  (override the path with `--config`) with schema validation, a
  timestamped `.bak` snapshot before each write, and rollback on
  validation failure. Each successful `set` appends `config.set` +
  `config.save` audit entries.
- `marunage init`, `doctor`, `setup`, `add`, `list`, `show`, `done`,
  `fail`, `promote`, `reopen`, `rm`, `render`, `export`, `clean`,
  `status`, `discover`, `dispatch`, `reaper`, `web` — see
  `docs/pr_split_plan.md` for the per-PR landing log.
- `marunage loop --once | --interval D` — drives one OODA tick
  (discover → dispatch → render) or a recurring ticker until
  SIGINT/SIGTERM. With no flag the interval defaults to
  `discovery.interval` from config (10m default).
- `marunage daemon start|stop|status` — pidfile-backed background loop
  control under `~/.marunage/daemon.pid`. `start` spawns a detached
  `marunage loop` and redirects stdout/stderr to
  `~/.marunage/logs/daemon.log`; `stop` sends SIGTERM and escalates to
  SIGKILL after 10s; `status` distinguishes running / stale-pidfile /
  no-pidfile.

Stubs that still print `not yet implemented` and exit non-zero:
`run-all`, `open`, `notify`, `review`. Their implementations land in
later PRs along the
[PR split plan](./docs/pr_split_plan.md).

## Build

```sh
make build              # produce ./bin/marunage
./bin/marunage --version
```

The version string is taken from `git describe --tags --always --dirty` at
build time and injected into `internal/version`.

## Development

```sh
make test         # go test ./...
make fmt-check    # gofmt -l fails on diffs
make vet          # go vet ./...
make lint         # golangci-lint run ./...
```

CI (`.github/workflows/ci.yml`) runs the same set on every push to `main`
and on every pull request.

## Layout

```
cmd/marunage/       CLI entrypoint (the marunage binary)
internal/cli/       cobra command tree
internal/config/    typed config schema, Load/Save, Get/Set primitives
internal/dispatch/  Dispatcher.Run — pending → cmux workspace + Claude session
internal/loop/      RunOnce / Run — discover → dispatch → render orchestrator
internal/logging/   JSON-Lines logger, rotating daemon.log writer, append-only audit.log
internal/version/   build-time version string
pkg/                public library packages (reserved for future phases)
web/                Web UI assets (reserved for PR-62 onwards)
```

## License

MIT — see [LICENSE](./LICENSE).
