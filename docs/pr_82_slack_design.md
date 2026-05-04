# PR-82 Slack source plugin — 設計メモ

> 本書は PR-82 (Slack Discovery source) の **設計判断の記録**。実装は `feat/pr-82-slack` ブランチ。`docs/pr_split_plan.md` の "PR-82 Slack source" セクションが正本。

## 目的

Slack のメンション・DM を Discovery 層に取り込み、PR-70 の `source.Plugin` 契約に従って統一的に列挙できるようにする。同時に、タスク完了通知を Slack DM に投稿する `Completer` も提供し、後続 PR-71 の dispatch 経路から「タスク #N done」をユーザに送れるようにする。

## スコープ (本 PR)

1. **`internal/source/slack/` パッケージ新設**
   - `Plugin` 型 (`List` / `Since` / `Complete` / `AuthStatus` / `Setup`)
   - `Client` interface (`FetchMentions` / `FetchDMs` / `PostDM` / `AuthStatus` / `Setup`)
   - `Message` wire-shape (`ChannelID` / `ChannelType` / `TS` / `ThreadTS` / `UserID` / `Text` / `Permalink`)
   - `Checkpointer` interface (`slack:last_ts` を保存)
   - `nilClient` default で `WithClient` 未指定でも構築可
2. **`Adapter` (source.Plugin)**
   - `source.Sincer` / `source.Completer` を実装、`source.Adder` / `source.Deleter` は実装しない (read-only-with-notify)
3. **`plugin.toml` 同梱 + `RegisterBuiltin`**
   - `sync_mode=bidirectional`, capabilities=[list, setup, auth-status, since, complete]
   - `source.ValidateAgainstManifest` で interface との整合を起動時にチェック
4. **`internal/cli/discover.go`**
   - `builtins` map に `slack` を追加 (Client 未配線、List は空配列を返すが registration 自体は通る)

## スコープ外

- 実 Slack MCP / Web API クライアント実装 (PR-71+ daemon 配線時に注入)
- DM 通知送信先 channel id を config から取り出す配管 (PR-71)
- `discovery.slack.include_dm` / `include_mentions` を CLI から渡す UX (PR-71)
- リアクション・スレッド返信を含む高度な観測 (PR-83+ 候補)

## アーキテクチャ

```
internal/source/slack/
   ├─ slack.go         core Plugin + Client interface + Message + nilClient
   ├─ adapter.go       source.Plugin / Sincer / Completer wrapper
   ├─ builtin.go       go:embed plugin.toml + RegisterBuiltin
   ├─ plugin.toml      bundled manifest
   └─ *_test.go        fakeClient + memoryCheckpointer driven unit tests
```

### Discovery 契約への接続

- `source.Sincer.Since(ctx, checkpoint)` は **明示 checkpoint 引数優先**。空文字なら `Checkpointer.Get("slack:last_ts")` から復元。Fetch 後、結果が 1 件以上あれば最大 `ts` で `Set` 更新。0 件 or error 時は checkpoint 不変。
- `source.Completer.Complete(ctx, externalID)` は configured DM channel に `タスク #<id> done` を `Client.PostDM` で投稿。`WithNotifyChannelID` 未指定時は `ErrNotifyChannelRequired` 即返却。
- `RawMetadata` は `channel_id` / `channel_type` / `ts` / `thread_ts` / `user_id`。`channel_type=="im"` 時のみ `dm_id` を追加 (downstream UI のため)。

## 設計上の判断ログ

### A. Client interface を切る (MCP 直結ではなく)

- **問題**: Slack MCP は async transport で、ユニットテストから直接駆動するのは煩雑。
- **選択**: `Client` interface を slack package に置き、テストは `fakeClient`、production は PR-71 で MCP/Web API adapter を別ファイルで実装。
- **代替検討**: MCP 呼び出しを直接 Plugin に書く → 却下 (テスタビリティ・モック乱用が増える)。

### B. `slack:last_ts` 比較は数値順

- **問題**: Slack ts は `1700000000.000100` のような decimal。lex 比較は同幅時のみ正しい。将来の桁拡張に脆弱。
- **選択**: `compareTS` で `.` で split し、整数部・小数部を順に長さ → lex で比較。空文字は最小扱い (Sincer 初回の "no checkpoint yet" sentinel)。
- **代替検討**: `strconv.ParseFloat` → 却下 (浮動小数点誤差が ts 衝突を生む可能性)。

### C. `nilClient` default

- **問題**: `WithClient` 未指定で `New()` した Plugin の各メソッドが nil-receiver で panic するのは startup 体験が悪い。
- **選択**: `nilClient` を default に置き、`Fetch*` / `PostDM` / `Setup` は `ErrClientNotConfigured`、`AuthStatus` は `(AuthNotConfigured, nil)` を返す。
- **理由**: `marunage discover --once --source slack` を Client 未配線で叩いても registration は通り、`AuthStatus` で "not configured" を提示できる。

### D. `Adder` / `Deleter` は実装しない

- **問題**: Slack を出力先として「タスクを Slack 上に作る」ような双方向ユースケースは PR-82 の責務外。
- **選択**: manifest からも `add` / `delete` capability を除外し、`source.ValidateAgainstManifest` が adapter 側 interface 不在を許容することを利用。
- **代替検討**: `Adder` を「DM 投稿」に流用 → 却下 (`Add` の semantics は "create task upstream"、DM 通知は `Completer` のほうが意味的に正しい)。

### E. 通知文は `タスク #<id> done` 固定 (i18n は将来 PR)

- **問題**: 通知文の copy 変更は将来あり得る。
- **選択**: `notifyMessageFormat` 定数として package-private に持つ。i18n は導入時に `WithNotifyTemplate(string)` を追加すれば既存 API を壊さない。
- **理由**: PR-82 段階では Phase 1 用途であり、文面のテンプレ化を先に入れると YAGNI。

## テスト戦略

- `slack_test.go::fakeClient` がメソッド毎の引数を記録し、`Plugin.List/Since/Complete` の forwarding を assert。
- `memoryCheckpointer` は `slack:last_ts` の `Get` / `Set` 振る舞いを再現。
- 主要シナリオ:
  - List: 両 flag off で client 未呼び出し、片方のみ true で対応 fetch のみ呼び出し、両 true でマージ
  - List: `Message` → `source.Task` 変換が `ExternalID` / `Title` (multiline split) / `RawMetadata` を期待通り埋める
  - Since: 明示 checkpoint 優先、stored checkpoint fallback、最大 ts 更新、0 件は不変、error 時は不変
  - Complete: 通知投稿、空 id は `ErrInvalidTaskID`、未配線 channel は `ErrNotifyChannelRequired`、PostDM error 透過
  - AuthStatus / Setup: forwarding + nilClient default
  - Manifest: 期待 capability set、`RegisterBuiltin` の `ValidateAgainstManifest` 通過、二重登録は `ErrPluginAlreadyRegistered`

## 後続 PR への申し送り

- **PR-71 (daemon 配線)**: `Client` interface を満たす MCP / Web API 実装を `internal/source/slack/mcp_client.go` (仮称) で追加し、`builtins` map の `slack` registrar を `WithClient` 込みに差し替える。`config.toml` の `[discovery.slack]` から `include_mentions` / `include_dm` / `notify_channel_id` を読み出して `New` に渡す。
- **PR-12 (kvstate)**: 既に `KVStateRepo` は landed しており、`slack:last_ts` キー名は本 PR で確定 (`CheckpointKey` 定数で公開)。
