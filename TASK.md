# PR-100: Slack Reaction Trigger

## 概要
Slackのリアクション（例: `:todo:`）がつけられたメッセージを自動タスク化する機能を実装する。

## 要件（docs/requirement.md より）
- Slack MCP の reactions.added イベントを購読（または定期ポーリング）
- 設定で指定したリアクション（例: `:todo:`）が押されたメッセージをタスク化
- メッセージ内容と permalink を notes に記録
- 押した本人のDMに完了報告を返す

## 依存
- Phase2 Slack source (internal/source/slack/) が既に実装済み
- PR-82 のSlack adapter を再利用する

## 実装方針

### テストリスト（TDD: Red → Green → Refactor）
1. リアクション付きメッセージがタスク化されること
2. 設定のリアクション以外は無視されること
3. permalink と message body が notes に記録されること
4. 重複登録されないこと（idempotency: source+external_id UNIQUE）
5. タスク完了時にDM通知が送られること（stub OK）

### 実装ファイル
- `internal/source/slack/reaction/` — 新パッケージ
  - `reaction.go` — リアクションイベントのポーリング・タスク化ロジック
  - `reaction_test.go` — TDDテスト
- `internal/config/schema.go` — `[discovery.slack.reaction_trigger]` 設定追加
  - `enabled = false`
  - `reactions = [":todo:", ":inbox_tray:"]`
  - `dm_on_complete = true`
- スキル: `internal/skills/embed/marunage-source-slack-reaction/SKILL.md`（任意）

### 参照する既存実装（必ず先に読むこと）
- `internal/source/slack/slack.go` — Slack MCP 経由の取得パターン
- `internal/source/slack/adapter.go` — adapter パターン
- `internal/source/slack/builtin.go` — ビルトイン実装
- `internal/source/slack/webclient.go` — Slack web client
- `internal/source/source.go` — Plugin interface
- `internal/source/markdown/markdown.go` — 参考実装

### 設定（config.toml に追加）
```toml
[discovery.slack.reaction_trigger]
enabled = false
reactions = [":todo:", ":inbox_tray:"]
dm_on_complete = true
```

## 開発ルール
- 必ずTDDで（Red → Green → Refactor）。テストを先に書くこと
- 他の実装パターンを必ず参照してから実装すること
- 適切な粒度でコミットすること（機能単位）
- 全コードは英語で書く（コメント・識別子）
- docker環境でのcurl検証を行うこと（docker-compose.yml参照）

## 検証手順
- slackhog (https://github.com/harakeishi/slackhog) を使って検証・テストすること
- docker compose up でDBを起動してcurlで動作確認
- どのようなcurlをして、どのようなレスポンスを得たかを記録すること

## 完了条件
1. `go test ./...` が全部green（race detectorも含む）
2. design-review を実行して Critical/Warning なし（または修正済み）
3. PR を作成し、動作確認の手順をPRコメントに記載
4. `docs/pr_split_plan.md` の PR-100 に DONE を記載する
5. /review-fix-loop を実行してプッシュ
