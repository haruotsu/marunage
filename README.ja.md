# marunage

English version: [README.md](./README.md)

> 丸投げする、でも手放さない。Slack の通知、GitHub の issue、カレンダー、
> メールを Claude Code の自律セッションに委譲しつつ、観察・介入・巻き戻しを
> 常にワンアクションで可能にしておきます。

[![CI](https://github.com/haruotsu/marunage/actions/workflows/ci.yml/badge.svg)](https://github.com/haruotsu/marunage/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/haruotsu/marunage.svg)](https://pkg.go.dev/github.com/haruotsu/marunage)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)

`marunage`（「丸投げ」由来）は、[Claude Code](https://www.anthropic.com/claude-code)
のための単一バイナリ OSS OODA ループ実行基盤です。Gmail / Calendar / Slack /
GitHub / Google Tasks / Notion / Markdown TODO を巡回し、カスタマイズ可能な
スキルで判定して、生き残ったタスクを分離された対話型
[`cmux`](https://github.com/manaflow-ai/cmux) ワークスペースへ流し込みます。
1 タスク = 1 Claude セッションで、完了後もセッションは残るので、いつでも
介入できます。

## 不変条件

| 不変条件         | 意味                                                                                  |
| ---------------- | ------------------------------------------------------------------------------------- |
| No silent loss   | 発見したアイテムは必ず SQLite に保存。skip されても `promote` するまで残る。          |
| No silent run    | すべての dispatch は `audit.log` と `judgment_reason` に記録される。                  |
| Reversibility    | すべての状態遷移は可逆（`done` → `pending`、`skipped` → `pending`、…）。              |
| Idempotency      | discovery を何度走らせても重複登録しない: `(source, external_id)` は UNIQUE。         |
| Crash safety     | SQLite WAL + atomic sentinel による完了検知。                                         |

## How it works

```mermaid
flowchart LR
    D["Discovery (Observe)"]
    Q["Queue (Orient + Decide)<br/>SQLite · triage"]
    E["Execution (Act)<br/>cmux + Claude"]
    O["Observation<br/>Web UI · audit.log"]

    D --> Q --> E --> O
    O -.->|promote · reopen · stop| Q
```

1 task = 1 cmux ワークスペース = 1 対話型 Claude セッション。`claude -p` の
ワンショットは使わないので、完了後に attach して会話を続けられます。

## Quickstart

```sh
go install github.com/haruotsu/marunage/cmd/marunage@latest

marunage init              # ~/.marunage/ 初期化、SQLite、permission mode 選択
marunage doctor            # claude / cmux / sqlite3 / gh / gws / jq の確認
marunage setup             # skills 導入、source 認証
marunage loop              # discover → dispatch → render を定期実行
marunage web               # http://127.0.0.1:7777
```

デーモン運用:

```sh
marunage daemon install    # LaunchAgent (macOS) / systemd-user unit (Linux)
marunage daemon start
marunage daemon logs -f
```

## Configuration

`~/.marunage/config.toml` が正本です。手編集、
`marunage config set | edit | wizard`、Web UI から編集でき、すべてスキーマ
検証 + atomic swap されます。

```toml
[core]
max_parallel = 3
default_cwd = "~/works"

[secrets]
backend = "auto"   # keyring → pass → age → 0600 file → env

[discovery]
interval = "10m"
sources_enabled = ["markdown", "github"]

[execution]
permission_mode = "bypass"   # bypass | default | acceptEdits | plan | custom
allowed_cwd_prefixes = ["~/works", "~/src"]
```

シークレットは `config.toml` には一切書きません。

## Development

必要なもの: Go 1.25+、`make`、
[`golangci-lint`](https://golangci-lint.run/welcome/install/)。

```sh
git clone https://github.com/haruotsu/marunage
cd marunage

make build      # ./bin/marunage
make test       # go test ./...
make lint       # golangci-lint run ./...
make fmt-check  # gofmt 差分があれば fail
```

CI は push / PR ごとに `make fmt-check vet lint test build` 相当を実行します。

## Community

- セキュリティ報告 → [SECURITY.md](./SECURITY.md)（公開 issue は避ける）
- 行動 → [Code of Conduct](./CODE_OF_CONDUCT.md)
- バグ報告・機能要望 → [issue テンプレート](./.github/ISSUE_TEMPLATE)
- リリース履歴 → [CHANGELOG.md](./CHANGELOG.md)

## License

[MIT](./LICENSE) © Haruto Yokoyama and contributors.
