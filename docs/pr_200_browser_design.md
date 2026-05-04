# PR-200 Browser adapter — 設計メモ

> 本書は PR-200 (Browser DOM scrape source plugin) の **設計判断の記録**。実装は `feat/pr-200-browser` ブランチ。`docs/pr_split_plan.md` の "PR-200 Browser Adapter" セクションが正本。

## 目的

API のない、もしくは API がブロックされている Web ソース (Slack saved later, GitHub bookmarks, RSS-less ニュースサイト等) を **DOM scrape** で TODO ソースとして取り込む汎用基盤を提供する。`internal/source/markdown` と同様、`source.Plugin` 契約を満たし、Discovery loop (PR-71) からプラグインとして呼び出せる。

## スコープ (本 PR)

1. **`BrowserDriver` interface** — cmux browser / Playwright を差し替え可能にする抽象化レイヤ。
2. **cmux 環境向け実装** — `cmux browser goto` + `cmux browser eval` で DOM 抽出 (CLAUDE.md 参照)。
3. **`browser.toml` config** — 1 サイト = 1 `[[site]]` テーブル。`item_selector` + フィールドごとの CSS selector / 属性指定で柔軟にルール定義。
4. **ExternalID 安定化** — `sha256(URL || \x00 || dom_key)[:16]` で再 scrape 時に同一 ID を再生成。
5. **`source.Plugin` 契約遵守** — `list` / `setup` / `auth-status` の必須 3 メソッド。read-only マニフェストで `add` / `complete` / `delete` / `since` は declare しない (PR-200 はスコープ外)。

## スコープ外

- 双方向 channel (Slack に "saved later" を新規追加する等)
- `Sincer` / 増分 scrape (将来 PR で追加予定 — `since` キャッシュは追加チェックポイント設計が必要)
- 自動ログイン / クッキー管理 (cmux browser pane の既ログイン状態に依存)
- 並列 scrape / レート制御 (Discovery loop が site 単位で逐次回す前提)

## アーキテクチャ

```
[browser.toml]
   └── LoadConfig ──> *Config (validated)
                        │
                        ▼
                   New(WithDriver, WithConfig)
                        │
                        ▼
                    *Plugin ── List(ctx) ──> []source.Task
                        ▲
                        │ scrapes via
                        ▼
                BrowserDriver (interface)
                  /              \
              CmuxDriver       fakeDriver (tests)
              (goto + eval)
```

### 主要型 (internal/source/browser/)

| ファイル | 責務 |
|---|---|
| `external_id.go` | `(URL, dom_key) -> 16 hex` 安定化ハッシュ |
| `driver.go` | `BrowserDriver` interface, `ScrapeTarget`, `ScrapedItem`, `FieldRule` |
| `config.go` | `browser.toml` パーサ + `ErrInvalidConfig` バリデーション |
| `browser.go` | `Plugin` (List/Setup/AuthStatus), `Option`, `ErrInvalidPlugin` |
| `adapter.go` | `Adapter` — `*Plugin` を `source.Plugin` に持ち上げ |
| `cmux_driver.go` | `CmuxDriver` — cmux.Runner 経由で `cmux browser goto/eval` |
| `builtin.go` | embedded `plugin.toml` 読み出し + `RegisterBuiltin(r, opts...)` |
| `plugin.toml` | `sync_mode = "read-only"`, `capabilities = [list, setup, auth-status]` |

### 設定ファイル例

```toml
[[site]]
name = "slack-saved"
url = "https://app.slack.com/saved"
item_selector = ".p-saved_messages_list_item"
key_field = "id"

[site.fields]
title = { selector = ".p-message_header__title" }
body  = { selector = ".p-message__body" }
id    = { selector = "[data-id]", attr = "data-id" }
```

## 設計上の判断ログ

### A. `BrowserDriver` interface を狭く保つ (`Scrape(ctx, target) -> []ScrapedItem` のみ)

- **問題**: Page / Element / QuerySelector まで露出する Playwright 風 API は driver 実装の負担が重い。
- **選択**: `ScrapeTarget` に「URL + item selector + field rules」を全部詰めて driver に渡し、driver 内部で 1 回の DOM walk で完結させる。
- **副次効果**: cmux driver 側で `cmux browser eval` 1 回 (= JS で document.querySelectorAll → field 抽出 → JSON.stringify) で済む。Playwright ベース実装でも同様の単一トリップで済む。

### B. ExternalID は SHA-256 (URL + `\x00` + dom_key) の 16 hex prefix

- **問題**: 「サイト A の item id `1`」と「サイト B の item id `1`」が同じ `external_id` になると queue の `UNIQUE (source, external_id)` 制約上は通っても運用混乱が起きる。
- **選択**: URL を hash 入力に含める。ヌル区切り文字で `(URL="ab", key="")` と `(URL="a", key="b")` の境界 alias も防止。
- **長さ**: 16 hex = 64 bit。birthday collision まで 2^32 件、現実の 1 sync = 数十〜数百件規模では十分。

### C. `Source` field は `"browser:<site-name>"` 形式

- **問題**: 1 plugin が複数サイトを統括するため、`Source = "browser"` 一律だと downstream UI が site 別ルーティングできない。
- **選択**: `pluginName + ":" + site.Name` (`"browser:slack-saved"` 等)。adapter の `Name()` は `"browser"` のまま (registry への登録キーは plugin 単位)。

### D. Manifest は read-only / list+setup+auth-status のみ

- **問題**: 将来 `Sincer` を追加する余地はあるが、PR-200 では DOM 状態の「前回との差分」検出機構を持たないため declare しない。
- **選択**: `sync_mode = "read-only"`, capabilities は 3 つだけ。`source.ValidateAgainstManifest` が adapter の interface 実装と cross check し、drift を起動時に検出。
- **テスト**: `TestAdapterDoesNotImplementOptionalCapabilities` で「`Adder` / `Sincer` 等を実装していない」ことを compile-time で pin。

### E. CmuxDriver は cmux.Runner を共有

- **問題**: `cmux browser` の shell-out を独自に実装すると `internal/cmux` 配下の Runner / fake パターンと重複し、テスト戦略が分裂する。
- **選択**: `cmux.Runner` interface (既存) を再利用。production は `cmux.ExecRunner`、test は scripted runner を inject。
- **副次効果**: 将来 `cmux` パッケージで Runner に metric / retry を被せれば browser driver にも自動で適用される。

### F. eval JS の生成は `encoding/json` で値をクオート

- **セキュリティ問題**: `browser.toml` の selector / attr が悪意ある JS に展開されると `cmux browser eval` 経由で任意コード実行になる。
- **選択**: `json.Marshal(s)` で JS 文字列リテラルにエンコード (JSON 文字列リテラルは JS 文字列リテラルと互換)。`"`, `\`, 制御文字を確実にエスケープする。
- **テスト**: `TestCmuxDriverScrapeBuildsExtractionJS` で生成 JS に各 selector が含まれることを assert。

### G. key_field 不在の item は **失敗ではなく skip**

- **問題**: 1 件だけ DOM が崩れていた場合に `List` 全体を失敗にすると Discovery loop が止まる。
- **選択**: `Fields[KeyField] == ""` の item は黙って drop。task 数が減るので site 全体が壊れていれば気付ける (= 完全に 0 件返ったら異常とわかる)。
- **代替検討**: warning log を出す案 → PR-200 ではログ層に依存しないため見送り。Discovery loop で metric 化する責務に回す。

### H. 1 サイトのドライバエラーは **List 全体を fail**

- **問題**: site A が失敗しても site B の結果は返したい / 返したくない の判断。
- **選択**: 失敗。**理由**: 「昨日より少ない task 数」は silent data loss であり、operator が気付けない。loud failure のほうが運用上安全。
- **テスト**: `TestListDriverErrorPropagates` で pin。

### I. context cancel は List 入口と各 site iteration で check

- **問題**: 長時間 scrape (site 多数 / 重い JS) 中の cancel が反映されない。
- **選択**: `ctx.Err()` を List 入口と for ループ毎の先頭で check。driver 内部の cancel 伝播は cmux.Runner が担う。
- **テスト**: `TestContextCancellation` で pre-cancelled ctx → driver 未呼び出し + `context.Canceled` 返却を pin。

## テスト戦略 (t_wada TDD)

| 領域 | テストファイル | カバー範囲 |
|---|---|---|
| ExternalID | `external_id_test.go` | 安定性 / URL 区別 / key 区別 / 境界 alias 防止 |
| Config | `config_test.go` | 必須フィールド漏れ毎にエラーメッセージ assert / 重複 site name 拒否 / 空ファイル拒否 |
| Plugin | `browser_test.go` | New 引数バリデーション / List 順序 / source 名 / RawMetadata / cancel / driver error 伝播 / key 不在 skip |
| Adapter | `adapter_test.go` | source.Plugin 実装 / forwarding / 不要 capability 非実装 |
| CmuxDriver | `cmux_driver_test.go` | goto → eval 順序 / JSON parse / error 伝播 / typed parse error / JS 生成内容 / runner inject |
| Manifest | `builtin_test.go` | embedded TOML validate / RegisterBuiltin / 重複拒否 / nil registry |

実装は **fakeDriver / scriptedRunner** で完全に外界遮断、`go test -race` 緑。

## 既存処理への影響

- `internal/source` パッケージ自体は変更なし (新規 sub-package のみ追加)
- `internal/cmux` の Runner interface を import するだけで、cmux パッケージの公開 API は無改変
- `make test && make lint` 全緑、PR 内の他テスト regression なし

## 今後の拡張余地 (本 PR の対象外)

1. **`Sincer` 実装** — `(URL, dom_key, content_hash)` を kv_state に保存し、変更があった item だけ返す
2. **Playwright driver** — cmux に依存しない実行環境 (CI / Docker) 向け
3. **`marunage browser doctor`** — `browser.toml` を validate するだけの subcommand
4. **selector 言語の表現力** — XPath サポート, 子孫複数取得, 正規表現抽出
5. **rate limit / retry** — driver 層に被せる decorator パターン

## 関連ドキュメント

- `docs/pr_split_plan.md` — PR-200 Browser Adapter セクション
- `docs/requirement.md` — Discovery plugin 契約 (lines 102-114)
- `internal/source/source.go` — `Plugin` / `Sincer` / `Adder` interface 定義
- `internal/source/markdown/` — 先行プラグイン実装パターン
- `CLAUDE.md` — `cmux browser` サブコマンド一覧
