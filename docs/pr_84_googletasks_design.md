# PR-84 Google Tasks Discovery source — 設計メモ

> 本書は PR-84 (Google Tasks bidirectional source plugin) の **設計判断の記録**。実装は `feat/pr-84-gtasks` ブランチ、PR #36。`docs/pr_split_plan.md` の "PR-84 Google Tasks source（並列）" と PR-70 (`internal/source.Plugin` 契約) が正本。

## 目的

ユーザーが日常的に使う Google Tasks を marunage の Discovery 経路に乗せ、PR-50 markdown と並ぶ **bidirectional source** として `marunage discover` 経由で取り込む。鏡像化 (marunage 側で done → Google Tasks 側 completed) まで PR-84 のスコープに含める。

## スコープ (本 PR)

1. `internal/source/googletasks/` 新規パッケージ。
2. `*Plugin` が以下を実装:
   - `source.Plugin` (Name / List / Setup / AuthStatus) — 必須
   - `source.Adder` (Add) — marunage タスクを Google Tasks に書き戻し
   - `source.Completer` (Complete) — marunage done → Google `status="completed"` の鏡像化
   - `source.Deleter` (Delete) — Google Tasks 側を更新
3. 上流 API は `google.golang.org/api/tasks/v1` を **狭い `Client` interface** で抽象化、ユニットテストは fake で実行。
4. `plugin.toml` を `go:embed` でバンドル、`source.LoadManifestFromBytes` で検証。
5. `RegisterBuiltin` で `*source.Registry` に登録 + `source.ValidateAgainstManifest` で capability ↔ interface 整合チェック。

## スコープ外 (本 PR)

- OAuth セットアップ (`Setup` は `ErrNotConfigured` を返す stub) — 後続 PR で `internal/secrets/` 連携と合わせて実装。
- `Sincer` (delta 取得) — Google Tasks API には効率的な delta endpoint が無いため意図的に未実装。"Since == 全件 List" の偽実装は dispatcher を欺くので避ける。
- 本物 API を叩く統合テスト — OAuth fixture が必要なため `Client` seam に閉じ、後続 PR で別ファイルに追加可能な構造のみ用意。
- daemon 周期実行は PR-71 (Discovery loop) の責務。

## アーキテクチャ

```
internal/source/googletasks/
  ├─ googletasks.go      Plugin (source.Plugin / Adder / Completer / Deleter)
  ├─ client.go           Client interface + GTask / GTaskList / 定数
  ├─ google_client.go    *tasks.Service バックエンドの本番 Client
  ├─ builtin.go          go:embed plugin.toml + Manifest() + RegisterBuiltin()
  ├─ plugin.toml         capabilities = list/setup/auth-status/add/complete/delete
  ├─ googletasks_test.go Plugin behaviour (fake client driven)
  ├─ fake_client_test.go in-memory Client double
  ├─ google_client_test.go translateError / NewGoogleClient 境界テスト
  └─ builtin_test.go     manifest 整合 + RegisterBuiltin
```

### 依存追加

| パッケージ | 追加 | 理由 |
|---|---|---|
| `google.golang.org/api/tasks/v1` | 直接依存 | 上流 API SDK。`Client` 経由のみ参照 |
| `google.golang.org/api/option` | 直接依存 | `option.WithHTTPClient` で OAuth 済み `*http.Client` を注入 |
| `google.golang.org/api/googleapi` | 直接依存 | 401/403 を `googleapi.Error` 経由で検出して `ErrUnauthorized` に翻訳 |
| `golang.org/x/oauth2` | 推移依存 | tasks SDK 経由 |

## 設計上の判断ログ

### A. `Client` interface は 6 メソッドに絞る

- **問題**: 生成 SDK `*tasks.Service` は REST 全表面を持つ。Plugin が触るのは ListTaskLists / ListTasks / Insert / Patch / Delete / Ping のみ。
- **選択**: パッケージ内で `Client` を定義し、本番は `*GoogleClient` で wrap、テストは `fakeClient`。
- **理由**: ユニットテストが OAuth・ネットワークを触らずに済む。後日 transport 差し替え (gRPC, mock HTTP) が seam に閉じる。
- **代替検討**: `*tasks.Service` を直接 Plugin に渡す案 → 却下 (テストが SDK 全体を mock する負債を負う)。

### B. `ExternalID` は **タスク ID のみ** (タスクリスト ID を含まない)

- **問題**: PR-70 の `source.Plugin.Complete(ctx, externalID)` は単一文字列を取る。Google Tasks の Patch/Delete は `(tasklistID, taskID)` を要求する。
- **選択**: Plugin 側で `findTaskList(ctx, taskID)` を実行し、タスクが属するリストを探索する。`source.Task.SourcePath = "tasklists/<list-id>"` には保持するので、UI/CLI 経由で人間が辿る経路は残す。
- **理由**: 要件「ExternalID は Google Tasks の task id」を満たす最も正直な実装。
- **代替検討**:
  1. `tasklist:taskID` 形式でエンコード → 却下 (要件に反する)
  2. メモリキャッシュで (taskID → tasklistID) を保持 → 却下 (cache invalidation の複雑さに見合わない。タスクリスト 1 件のユーザがほとんど)
  3. `marunage:source=googletasks` 風のメタデータカラム → 却下 (queue schema を増やすので別 PR の責務)

### C. `Sincer` は実装しない

- **問題**: Google Tasks API に対応する delta endpoint が無い (updatedMin パラメータはあるが、削除を取れない)。
- **選択**: manifest から `since` capability を **意図的に外す**。`source.ValidateAgainstManifest` は under-impl のみエラーにし、未宣言の interface 実装は許容するが、Plugin 側にも `Since` メソッドを生やさない。
- **理由**: dispatcher が `since` を見て「safe な cheap-poll」と判断するのを防ぐ。"全件 List で代用する Since" は dispatcher を欺く反パターン。
- **代替検討**: `Since(ctx, _) -> List(ctx)` の偽実装 → 却下。

### D. `AuthStatus` は `Client.Ping()` を呼ぶ

- **問題**: 「資格情報が生きているか」を返す cheap probe が必要。常に `AuthAuthenticated` を返すと revoked token を見逃す。
- **選択**: 専用の `Ping(ctx)` メソッドを `Client` に持ち、本番は `Tasklists.List().MaxResults(1)` で確認、fake は `pingErr` フィールドで test 駆動。
- **エラー翻訳**:
  - nil → `AuthAuthenticated`
  - `ErrUnauthorized` (= 401/403 from `googleapi.Error`) → `AuthRevoked`
  - その他 (5xx, ネットワーク) → エラーを propagate
- **理由**: AuthStatus 用に AuthExpired を使わない。Google の token refresh は `*http.Client` の transport が透過的に行うため、`Ping` が 401 を返す時点で refresh も失敗済 = revoked と扱うのが正しい。

### E. `WithDefaultTaskList` で Add の宛先を override 可能に

- **問題**: marunage 専用リストを切るユーザーは個人 default に混ぜたくない。
- **選択**: 構築オプション `WithDefaultTaskList(id string)` で固定。未指定時は `"@default"` (Google API alias)。
- **理由**: 双方向書き戻しの宛先を明示可能にしつつ、デフォルトは「最も近い動作」。

### F. `Setup` は stub (本 PR では `ErrNotConfigured`)

- **問題**: OAuth デバイスフロー / リダイレクト URL / token 永続化はそれぞれ別の意思決定が必要。
- **選択**: `Setup` は ctx 検査のみ実装し、実装は後続 PR (secrets backend 連携) に分離。
- **理由**: PR-84 を "上流 API 層" に閉じる。`Setup` まで含めると secrets backend (PR-31) と握手が必要で、PR が肥大化し並列性が落ちる。

## テスト戦略

t_wada TDD。`internal/source/googletasks/.test-list.md` で進捗管理。

- **fake client (`fakeClient`)** で振る舞いテスト: List / Add / Complete / Delete / AuthStatus 全パス + ctx cancel + upstream error。
- **`*GoogleClient` 単体**: `translateError(401/403/500/nil)` と `NewGoogleClient(nil http)` 境界のみ。実 API は seam に閉じて触らない。
- **manifest 整合**: capability ↔ interface 双方向 (Sincer を実装しないことも明示テストで pin)。
- **コンパイル時アサーション**: `var _ source.Plugin = (*Plugin)(nil)` 系で interface 実装の漏れ落ちをコンパイルエラーで防ぐ。

## 観測性

- 本 PR では新規 audit イベントを追加しない (Plugin はソース層、audit は queue / dispatch 層の責務)。
- 上流 API エラーは `error` をそのまま返却。dispatcher 側で `Redact` 適用済 (PR-42b H6/H8)。
- `AuthStatus = AuthRevoked` を検出した場合のオペレータ通知は CLI / TUI 側 (PR-71+) の責務。

## オープン質問

- `Setup` の OAuth フロー実装 PR を分けるべきか同時に積むか → 分けて良い (このメモは「分ける」前提で書いている)。後続 PR (PR-31 secrets backend と連携) は **OS Keychain / DPAPI / libsecret / `pass` のいずれかでトークンを保存**し、`~/.marunage/` 配下への平文書き出しは禁止する (要件 §9.1 / OpenClaw §11.1)。
- `WithDefaultTaskList` の値検証 (実在するリストか) → 実装ナシ。Add 時に upstream が 404 を返せば error が伝播するので、boundary check は upstream に委ねる。`marunage doctor` 相当の起動時整合チェックを将来追加する案あり。
- Body / Notes 以外のフィールド (Due 期日、Parent / Position による階層構造) を将来露出するか → 将来 PR。`GTask` の最小化はそれを織り込んだ。
- ExternalID は本 PR では task id 単独 (要件遵守)。Google API は task id のグローバル一意性を仕様で保証していないため、`findTaskList` は **複数リストでヒットした場合に `ErrAmbiguousTaskID` で明示エラー** を返し、サイレントに first-hit を選ばない。将来 `<list-id>:<task-id>` 複合 ID への切り替えが必要になった場合は、queue schema 側の (source, external_id) UNIQUE 制約と合わせて再設計する。
- `Adder` / `Completer` / `Deleter` は外部書き戻し操作に該当するため、**人間承認ゲート (要件 §3.10) は dispatcher 層 (PR-71+) の責務** とする。Plugin 自体は承認なし状態で呼び出される前提で動作し、`--dangerously-skip-permissions` の影響範囲も dispatcher が判断する。Plugin manifest に `needs_human_approval: true` 相当のフラグを将来追加する案は別途検討。
- `source.Task.Body` (= upstream Notes) は **外部由来コンテンツ** であり、Memory に流入する経路で `origin: external/googletasks` タグの付与は **上位 (queue / Memory) レイヤの責務**。Plugin はタグ付与せず、生の文字列を返す。
- rate limit / 429 / `Retry-After` の指数バックオフは PR-71 の Discovery loop もしくは `Client` decorator (将来 PR で `RateLimitedClient` 型を `client.go` に追加) の責務とする。本 PR の Plugin 自体は同期 API として正しく振る舞い、retryable error の分類は呼び出し側に委ねる。

## 観測性 / 責務委譲

セキュリティ / 観測性関連の責務は以下のとおり明示的に他レイヤへ委譲する:

| 項目 | 責務レイヤー | 備考 |
|---|---|---|
| OAuth トークン保存 (Keychain / DPAPI / libsecret) | PR-31 secrets backend | 平文保存禁止 (§9.1) |
| 外部書き戻しの人間承認ゲート | PR-71 dispatcher | Plugin 側 capability flag は将来検討 |
| `origin: external/googletasks` タグ付与 (時間差プロンプトインジェクション対策) | queue / Memory 層 | Plugin は raw を返す |
| エラー文字列の `Redact` 適用 (PR-42b) | dispatcher / logger | Plugin は `truncateMessage` で 120 バイトに切り詰め済み (defence in depth) |
| rate limit / 429 / 指数バックオフ | PR-71 Discovery loop or future `RateLimitedClient` decorator | `translateError` は 401/403/404 のみ翻訳 |
| `AuthRevoked` 検出時の polling 停止 | dispatcher | Plugin は `AuthStatus` で都度 Ping (cache せず) |
| 監査イベント (audit log) | queue / dispatch | Plugin はソース層に閉じる |

ロールバック手順: manifest の `RegisterBuiltin` 呼び出しを起動側で外せば googletasks プラグインが無効化される。SQLite / kv_state にデータは残るが、source plugin が消えるだけで queue 側は健全に動作する。
