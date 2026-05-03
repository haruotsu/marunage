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
listed in `marunage --help`. The leaves are stubs that print
`not yet implemented (see docs/pr_split_plan.md)` and exit non-zero — actual
behavior lands in later PRs along the
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
internal/           private packages (version, future: store/exec/source/...)
pkg/                public library packages (reserved for future phases)
web/                Web UI assets (reserved for PR-62 onwards)
```

## License

MIT — see [LICENSE](./LICENSE).
