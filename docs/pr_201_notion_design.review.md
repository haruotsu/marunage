# pr_201_notion_design.md 設計レビュー結果

## 📋 対象ドキュメント
- パス: `docs/pr_201_notion_design.md`
- タイトル: PR-201 Notion Discovery Source Plugin — Design
- 対象 version: 不明（v0.1〜v0.2 相当の Source プラグイン拡張）
- 影響レイヤー: go
- 関連領域: security / pluggability / http-api / daemon-runtime / data-model / test-strategy / non-goal-consistency

## 🔴 重大な指摘（必須対応）

### 1. HTTP クライアントのタイムアウト未設定
**問題点**: `NewHTTPClient(nil, ...)` で `http.DefaultClient`（Timeout=0）が採用される。Notion 側 hang で goroutine 滞留 → 常駐運用で致命。
**該当箇所**: `internal/source/notion/http_client.go` の `NewHTTPClient`
**改善提案**: nil 時に `&http.Client{Timeout: 30s}` を明示的に組み立て、Doc にも宣言する。
**根拠**: review-guidelines §6（常駐運用 / Daemon Runtime）

### 2. 429 / Retry-After / 5xx のリトライ未対応
**問題点**: Notion API は `429 rate_limited` + `Retry-After` を返すが、`decodeErrorResponse` は generic error に落ち、`ErrRateLimited` 型も指数バックオフも無い。Discovery 周回で容易に全滅。
**改善提案**:
- `ErrRateLimited` 型を追加し、`Retry-After` を尊重した自動再送（冪等メソッドのみ、最大 5 回 + jitter）。
- 5xx も同じ経路で再送、最大バックオフは 5 分。
**根拠**: review-guidelines §3.6（常駐運用）/ Notion API 仕様（3 req/s）

### 3. TLS / baseURL のセキュリティ要件未明記
**問題点**: 設計に「TLS 必須」「`InsecureSkipVerify` 禁止」「prod では `https://api.notion.com` 固定」「token を URL / log / error message に絶対に出さない」の不変条件が明記されていない。
**改善提案**: 設計 doc に `## Security / Threat Model` 節を新設し OpenClaw §11.1 と 1:1 で対比。
**根拠**: OpenClaw §11.1-1,2 / OWASP A2 / A10

### 4. ctx キャンセル伝播がページネーションループ途中で未確認
**問題点**: `QueryDatabase` が `has_more` を辿るループ内で `ctx.Err()` を確認していない。daemon stop 時に長時間ブロック。
**改善提案**: ループ先頭で `if err := ctx.Err(); err != nil { return nil, err }`。
**根拠**: review-guidelines §6

### 5. 設計 doc に Phase / 撤退条件 / Mermaid 図 / Test strategy 節が欠落
**問題点**: review-guidelines §1 / §8 / §9 / §10 必須項目（Phase 位置づけ・ロールバック条件・図表・テスト戦略要約）の不足。
**改善提案**: それぞれ独立節を追加、`.test-list-notion.md` の構造を要約引用、Mermaid シーケンス図（List / Since）を追加。
**根拠**: review-guidelines §1 §8 §9 §10

## 🟡 中程度の指摘（推奨対応）

### A. Security 節の不在
- secrets backend の既定（macOS Keychain / DPAPI / libsecret）と平文 fallback 禁止を明記
- `kv_state` には決して token を置かないこと、`secrets:Set("notion:token", ...)` のみが正規経路
- error / log に token 文字列が混入しないことの不変条件と golden test
**根拠**: OpenClaw §11.1-1 / review-guidelines §3.10

### B. レスポンス body の無制限読み込み
- `io.ReadAll(resp.Body)` を `io.LimitReader`（1 MiB）で囲む
- 成功レスポンスの decoder にも上限を
**根拠**: 防御的コーディング（hostile / 壊れた upstream）

### C. kv key 命名規約のリポジトリ全体での揺れ
- 設計は `notion:last_edited_time:<database_id>`（コロン）
- `migrations/0001_init.sql` / `kvstate.go` 既存例は `gmail_last_id` / `slack_last_ts`（アンダースコア）
- どちらかに正規化、SSOT を `internal/store/kvstate.go` のパッケージ doc に置く
**根拠**: review-guidelines §12（用語の一貫性）

### D. RawMetadata の充実
- 現状は `last_edited_time` / `database_id` のみ
- 後段 triage / Markdown 化で再 query を避けるため `archived` / `created_time` / `notion_url` を追加推奨
**根拠**: review-guidelines §5（Markdown SSOT 派生）

### E. Pluggability — Goal に「該当拡張点 = Source Adapter (1 of 10)」を明記
**根拠**: pluggability-design-agent §1（拡張点同定の必須項目）

### F. Markdown SSOT との同期方向
- Notion `Add` / `Complete(=archive)` / `Delete(=archive)` と Markdown SSOT 思想の関係（Notion → Markdown 派生 / 逆方向は非ゴール）が doc に欠落
**根拠**: REQUIREMENTS §3.8 / non-goal §3.7

### G. ポーリング間隔・電力配慮・遡及上限
- `WithMaxLookback(30*24*time.Hour)` を予約ノートに記載（長期スリープ復帰時の暴走防止）
- 推奨間隔（API rate limit ≒ 3 req/s）の最低値を明記
**根拠**: review-guidelines §6 §10

### H. request_id を error / log に伝搬
- `x-notion-request-id` を捕捉してエラー wrap に含める（サポート問い合わせ時の追跡）
**根拠**: review-guidelines §7（観測性）

## 🟢 軽微な指摘 / 提案

- `decodeErrorResponse` の `io.ReadAll` エラーを `_` で握りつぶしている → `%w` で wrap
- `Setup` のデフォルト `TokenProvider` 不在時に `MARUNAGE_NOTION_TOKEN` env を読む実装にし、doc と実装を揃える
- ExternalID = Notion UUID は安定だが Duplicate 時に新 UUID → 別タスク扱いになる旨を Tradeoffs に追記
- `.test-list-notion.md` E3 の "UPDATE:" 残置メモを解消
- 対話モード Setup（NonInteractive=false）のテスト未整備
- `extractTitle` の fuzz テスト追加（rich-text の null/空 array/不正型混入で panic しないこと）

## ✅ 良い点

- Client interface seam + fakeClient + functional options が markdown source と完全に揃っており、レビュー摩擦が極小
- 単調進行 / 空結果ガード / lex compare on ISO-8601 の判断と根拠が明文化
- `Notion-Version` ヘッダ固定でサーバ変動による silent break を防止
- 401 を `code` (`expired_token` / `unauthorized`) で typed error に分岐する設計が AuthStatus と整合
- has_more / next_cursor pagination を内部で walk して呼び出し側に flat slice を返す抽象化が一貫
- `go:embed plugin.toml` + `ValidateAgainstManifest` 起動時検証で capability ↔ interface drift を捕捉
- TDD テストリスト `.test-list-notion.md` が完了状態でトレーサブル

---

## レイヤー特化レビュー

### Go 実装
- functional options + Client seam の徹底は評価
- 不足: HTTP タイムアウト、Phase / ロールバック / Mermaid、撤退条件
- `decodeErrorResponse` の `_` エラー握りつぶしは要修正
- `Setup` のデフォルト tokenProvider 動作と doc の整合

### Rust 実装
- 非該当（Go プラグイン）

### TypeScript / フロントエンド
- 非該当（バックエンド）

### Markdown / Vault 互換
- 非該当（API 連携）

---

## 機能横断レビュー

### セキュリティ
- 🔴 TLS / baseURL pin / token-not-in-log 不変条件の doc 化必須
- secrets backend 既定値 / 平文 fallback 禁止の明記
- error message のサニタイズ（長さ制限・改行除去）

### プラガビリティ規約
- 既存パターンと完全整合、capability drift を起動時検証する設計が規約準拠
- Goal セクションに拡張点同定 1 行追加推奨

### HTTP API 設計
- 🔴 Timeout / 429 / Retry-After / 5xx 再送 / body 上限 / request_id すべて未対応
- Tradeoffs にリトライ方針を明記、テストリストに 429 シナリオを追加

### 常駐運用 / Daemon Runtime
- 🔴 ctx 伝播 / バックオフ / rate limit / 長期スリープからの復帰
- `WithMaxLookback` を予約

### データモデル / SQLite
- kv key 命名規約をリポジトリ全体で正規化
- `RawMetadata` に `archived` / `created_time` を追加
- token を kv_state に置かない明文化

### テスト戦略（t_wada TDD）
- 🔴 設計に Test strategy 節追加、`.test-list-notion.md` を要約引用
- fuzz / 429 / ctx-cancel / 大量 pagination のテストリスト追加
- token が log / error に出ない golden test

### 非ゴール / 要件整合性
- Notion を SSOT 化していないこと、Markdown SSOT との同期方向を明記
- 外部書き込み（archive）は CLI 経由の明示操作前提を明記

---

## 📝 総合チェックリスト

### 全体
- [ ] ドキュメント構造の完全性（Phase / Mermaid / Test strategy / Security 節欠落）
- [x] REQUIREMENTS.md との整合性（Source 拡張点として正しい）
- [ ] セキュリティ既定条件（TLS / baseURL / token-not-in-log の doc 化）
- [x] プラガビリティ規約（manifest / capability / interface 整合）
- [ ] Markdown SSOT 整合（Notion archive と Markdown `[x]` の同期方向）
- [ ] 常駐運用 / 静粛性（タイムアウト / リトライ / バックオフ）
- [ ] 観測性（request_id / 構造化ログ）
- [ ] 図表（Mermaid なし）
- [ ] Phase / 実装計画（節なし）
- [ ] テスト方針（doc 内要約なし）
- [x] OSS 衛生（manifest 自己宣言、SDK 依存なし）
- [ ] 用語の一貫性（kv key 命名揺れ）

## 🧭 次アクション

- [ ] 必須対応（🔴）の修正: 1〜5 を本 PR で fix
- [ ] 推奨対応（🟡）のうち security / 429 / kv 命名は本 PR、残りは follow-up Issue 化
- [ ] Open Question 追記: Markdown SSOT との同期方向 / OAuth(PKCE) 移行時の refresh token

## 総評

設計と実装は markdown source の参照パターンに忠実で、TDD トレーサビリティも確保。一方、設計 doc 自体には Phase / Mermaid / Test strategy / Security 節が欠落しており review-guidelines 必須項目を満たさない。コード側でも常駐運用前提（タイムアウト・429・ctx 伝播）と OpenClaw §11.1 セキュリティ要件（TLS pin / token-not-in-log）が抜けており、本 PR で最低限は対処すべき。プラガビリティ規約・データモデル名前空間・SSOT 思想との整合性は概ね保たれており、致命的な再設計は不要。
