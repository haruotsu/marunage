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
- **DM への返信を agent への指示として再受信する経路は本 PR で導入しない**。`FetchDMs` は Discovery (一方向 in) のみ、`PostDM` は Completer (一方向 out) のみで利用する
- **Slack 上での会話的対話 UI は永続的非ゴール** (REQUIREMENTS §3.7 / §5.12)。`source.Adder` を将来「DM 投稿」に流用しないこと、Slack 受信メッセージを LLM 出力としてそのまま再投稿しないことを本 PR の設計境界として固定する

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

- `source.Sincer.Since(ctx, checkpoint)` は **明示 checkpoint 引数優先**。空文字なら `Checkpointer.Get("slack:last_ts")` から復元。Fetch 後、結果が 1 件以上あり、かつ **fetched の最大 ts が effective checkpoint より厳密に大きい** ときのみ最大 `ts` で `Set` 更新。0 件 / error / fetched 全てが effective 以下なら checkpoint 不変 (単調増加)。これは upstream が `oldest` を尊重しない場合でも stale な再取り込みを防ぐ defense-in-depth。
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
- **理由**: PR-82 段階では Phase 1 用途であり、文面のテンプレ化を先に入れると YAGNI。完了通知は破壊性が低いので人間承認なしの auto 送信を許容する (REQUIREMENTS §11.1 (6) アウトバウンド人間承認モードに対する例外宣言)。

### F. `sync_mode = "bidirectional"` の選択理由

- **問題**: `internal/source/manifest.go` の `SyncMode` は現状 `"bidirectional"` / `"read-only"` の 2 値しか受け付けない。`Adder` / `Deleter` を実装しない本 plugin に `bidirectional` を当てると、上流 dispatcher が「Add 可能・Delete 可能」と誤読しうる。
- **選択**: `bidirectional` を採用しつつ、本 plugin の権威は **`capabilities` 配列のほう** とする。dispatcher は `Manifest.HasCapability(CapAdd)` / `HasCapability(CapDelete)` を読み、`sync_mode` 単独で `Adder.Add` を踏まないこと (`internal/source/registry.go` の `ValidateAgainstManifest` がすでに「manifest が capability を宣言していなければ adapter 側 interface 不在を許容」する設計)。
- **代替検討**: `read-only` を採用 → 却下 (`complete` capability があるため厳密には read-only ではない)。第三値 `notify-only` を新設 → 却下 (現 PR スコープ外。`internal/source/manifest.go` 改訂は別 PR で議論)。
- **検証**: `internal/source/slack/builtin_test.go` の `TestManifestEmbedded` で `HasCapability(CapAdd) == false` && `HasCapability(CapDelete) == false` を pin。
- **Open Question**: `sync_mode` を `Capabilities` から導出する仕様にして TOML 側 field を deprecate する案を `docs/pr_split_plan.md` 側で議論する。

### G. `CheckpointKey` の workspace scope (Phase 1 単一 workspace 前提)

- **問題**: `slack:last_ts` 単一 key は将来「複数 Slack workspace 同時購読」したときに後勝ち上書きで取りこぼす。
- **選択**: Phase 1 では **単一 workspace** を前提とし、key 名を `slack:last_ts` で固定する。
- **拡張案** (将来 PR): 複数 workspace 対応時は `slack:<workspace_id>:last_ts` 形式に拡張。`CheckpointKey` 関数化 (引数 = workspace id) で API 後方互換を保つ。
- **境界**: token 等の secret は kv_state ではなく OS Keychain / DPAPI / libsecret 側に置く。kv_state には ts のような **再生成可能なメタデータのみ** を置く (REQUIREMENTS §9.1 / OpenClaw §11.1 (1))。

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
  - `compareTS` 専用境界値テスト: 空文字最小 / 同値 / 整数部桁ズレ / 小数部桁ズレ / 整数のみ vs 小数あり / lex で逆転するケース

## 後続 PR への申し送り

- **PR-71 (daemon 配線)**: `Client` interface を満たす MCP / Web API 実装を `internal/source/slack/mcp_client.go` (仮称) で追加し、`builtins` map の `slack` registrar を `WithClient` 込みに差し替える。`config.toml` の `[discovery.slack]` から `include_mentions` / `include_dm` / `notify_channel_id` を読み出して `New` に渡す。
- **PR-71 (audit)**: `Adapter.Complete` 呼び出し点に `audit.error` (`Action="completer.slack.failed"`) の記録を追加する。Plugin 自体は audit を持たない (DI されていない) ため、daemon 層が責務を持つ (REQUIREMENTS §9 全操作 JSONL 監査)。
- **PR-71 (concurrency)**: `Sincer.Since` の並列呼び出しは daemon 側でシリアライズする。`Plugin` は `Checkpointer.Set` を mutex で守らないため、複数 dispatcher / scheduler から同時呼び出しすると interleave で max ts を取りこぼす。
- **トークン保管 (defense-in-depth)**: Slack トークンは **OS Keychain (macOS) / DPAPI (Windows) / libsecret/pass/age (Linux)** に保管し、`config.toml` には保管庫 ID のみ記述する。`config.toml` 平文に Slack token を書く設計が後続 PR に紛れ込んだら CI で grep 検出する (OpenClaw §11.1 (1) 平文クレデンシャル禁止)。
- **時間差プロンプトインジェクション**: `FetchMentions` / `FetchDMs` で取り込んだテキストは将来 LLM プロンプトに流れ込む可能性が高い。PR-71 で materialise する際、`tasks.metadata` に `origin: external/slack` タグを付与し、LLM に渡す前に Memory への書き出し時にも同タグを継承させる (OpenClaw §11.1 (8) 時間差プロンプトインジェクション防御)。
- **PR-12 (kvstate)**: 既に `KVStateRepo` は landed しており、`slack:last_ts` キー名は本 PR で確定 (`CheckpointKey` 定数で公開)。

## ロールバック / 撤退条件

- **撤退基準**: revert-only。`internal/source/slack/` を丸ごと削除 + `internal/cli/discover.go` の `slack` 行を削除すれば、他の source plugin への影響なく完全撤退できる (Phase 1 段階では daemon が `slack` を必須としていない)。
- **kv_state 後始末**: 本 PR は `slack:last_ts` を **書き込まない** (Sincer は Checkpointer 配線時のみ Set。CLI 経路では nilCheckpointer 相当)。撤退時に手動クリーンアップは不要。
