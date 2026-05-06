# api.md 設計レビュー結果

## 📋 対象ドキュメント
- パス: `docs/api.md`
- タイトル: marunage Web API Reference
- 対象 version: v0.0.x（CHANGELOG 最新）
- 影響レイヤー: go / markdown
- 関連領域: security / pluggability / http-api / daemon-runtime / data-model / test-strategy

---

## 🔴 重大な指摘（必須対応）

### 1. `--remote` 時に全エンドポイントが認証なしで公開される（OpenClaw §11.1 違反）

**問題点**: `cli/web.go` のコード内に "authentication itself lands in a later PR" と記されている通り、`--remote` フラグで `0.0.0.0` バインドに切り替えた際に Bearer トークン等の認証ゲートが存在しない。特に以下の 3 エンドポイントは影響が大きい:
- `GET /events`: タスク状態変化・discovery イベントが外部に漏洩
- `GET /api/tasks/{id}/stream`: ターミナル出力が丸見えになる
- `POST /api/tasks/{id}/send`: 任意テキストを実行中エージェントに注入でき、実質的な RCE に近いリスク

**該当箇所**:
> `POST /api/tasks/{id}/send` — Sends text input to the task's cmux workspace. Requires CSRF token.

**改善提案**: `api.md` の Overview セクションに「`--remote` 使用時は Bearer トークン認証が強制される（未設定なら起動拒否）」を明記する。実装側は `web.Remote = true && web.AuthToken == ""` の場合に起動エラーにする設計を追加する。

**根拠**: OpenClaw §11.1「0.0.0.0 公開時はトークン認証強制（未設定なら起動拒否）」、review-guidelines §3 セキュリティ既定条件。

---

### 2. `GET /api/skills/registry` が SSRF ベクターになりえる

**問題点**: このエンドポイントはサーバーサイドで `RegistryURL` に HTTP リクエストを送るプロキシとして機能する。`RegistryURL` の domain allowlist 検証や内部アドレス（RFC1918 / loopback）ブロックが実装・ドキュメントどちらにも記述されていない。設定ファイルに内部アドレスを書き込まれた場合にローカルネットワークへの探索経路になる。加えて Non-Goal 定義「中央集権的 Skill レジストリは v0.3 まで非ゴール」に対し、v0.0.x の時点でこのエンドポイントが有効化されていることの設計上の位置づけが未記述。

**該当箇所**:
> `GET /api/skills/registry` — Proxies the upstream skill registry catalog through the Web UI ...

**改善提案**:
1. `RegistryURL` に起動時バリデーション + ドメイン allowlist + 内部アドレスブロックを追加する。
2. `api.md` の Conditional Endpoint Availability テーブルに `GET /api/skills/registry` を追加し、`RegistryURL` 設定かつ v0.3 以降の条件を明示する。
3. 署名検証・許可制インストール・typosquat 検知が未実装の間は「実験的機能」として注記する。

**根拠**: OWASP A10 SSRF、review-guidelines §3「アウトバウンド HTTP の domain allowlist 必須」、Non-Goal §ルール6。

---

### 3. `DELETE /api/tasks/{id}` が `running` タスクの削除をガードしていない

**問題点**: ドキュメントには "Deletes a task regardless of its current status." と記載されているが、`running` 状態のタスクを削除するとデーモン側のサブプロセス（cmux ワークスペース）が孤立する可能性がある。クリーンアップ手順がドキュメントにも実装にも存在しない。

**該当箇所**:
> `DELETE /api/tasks/{id}` — Deletes a task regardless of its current status.

**改善提案**: `running` 状態のタスクに対して `DELETE` を実行した場合は `409 Conflict` を返し、代わりに `POST /api/tasks/{id}/cancel`（ワークスペース停止 + ロールバックマーク付き）を新設する。あるいは削除前に自動的にワークスペースを停止するフローをドキュメントに記述する。

**根拠**: review-guidelines §9.1「進行中タスクをロールバック対象としてマーク」。

---

### 4. タスクステータス列挙値が REQUIREMENTS.md と不一致

**問題点**: `api.md` は `done` / `skipped` / `failed` / `pending` / `running` を使用しているが、REQUIREMENTS.md の frontmatter スキーマ定義では `completed` / `cancelled` が用いられている。どちらが SSOT か明記されていない。

**該当箇所**:
> `POST /api/tasks/{id}/reopen` — Transitions a task from `done` or `failed` → `pending`.
> `GET /api/review/skipped` — Returns a JSON array of tasks with `skipped` status.

**改善提案**: タスクステータスの完全な列挙（`pending | running | done | failed | skipped`）を `api.md` の冒頭に状態遷移図（Mermaid）として掲載し、REQUIREMENTS.md 側の `completed`/`cancelled` との対応関係を明記するか、REQUIREMENTS.md を実装に合わせて改訂する。

**根拠**: review-guidelines §12「用語の一貫性」、§5 Markdown SSOT。

---

### 5. デフォルトバインドアドレス・ポートの記述不整合

**問題点**: `api.md` の Overview では `default: http://localhost:8080` と記述しているが、README の Quickstart では `http://127.0.0.1:7777` と記述されている。`localhost` は環境によって `0.0.0.0` に解決されうるため、セキュリティ要件「既定は 127.0.0.1」を満たさない表記になっている。

**改善提案**: `api.md` の Base URL を `http://127.0.0.1:7777`（デフォルト）に修正し、README と統一する。

**根拠**: review-guidelines §3.10「Web UI / Gateway の 0.0.0.0 バインド = 禁止」。

---

### 6. `internal/web/skills.go` がエラー詳細をそのまま HTTP レスポンスに露出

**問題点**: `newSkillsHandler` / `newInstalledSkillsAPIHandler` が `fmt.Sprintf("skills: %v", err)` を `http.Error` に直接渡しており、ファイルパス・内部状態などが外部に漏洩する可能性がある。`handler.go` の `dashboardLoadFailedMessage` パターン（詳細はログのみ）と一貫性がない。

**改善提案**: skills ハンドラのエラーレスポンスを `"skills unavailable. See daemon.log for details."` 等の固定文言に変更し、詳細はアクセスログのみに残す。

**根拠**: review-guidelines §3 セキュリティ。

---

## 🟡 中程度の指摘（推奨対応）

### 7. 認証・認可の全体仕様が Overview に欠落

`api.md` の Overview に CSRF の説明はあるが、リモート公開時の Bearer トークン、`401 Unauthorized` のレスポンス定義がない。外部開発者がリモートモードを利用した際の安全な実装判断ができない。

**改善提案**: Overview に「ローカルモード（`127.0.0.1` バインド）と `--remote` モードの違い」「`--remote` 時に必須となる認証ヘッダ（Bearer）」「未認証時の `401` レスポンス」を追加する。

---

### 8. エラーレスポンス形式が HTML エンドポイントと JSON API エンドポイントで不統一

JSON API エンドポイントは `{"error": "..."}` 形式だが、一部の HTML 系エンドポイントは平文テキストを返す。`GET /api/tasks/{id}/stream` の 404 も平文 `workspace not found` になっている。

**改善提案**: JSON API エンドポイント（`/api/...`）は統一エラー形式 `{"error": "..."}` を使用すること、HTML エンドポイントは平文テキストを使用することを `api.md` に明記する。RFC 7807 `application/problem+json` の採用も選択肢として検討する。

---

### 9. `DELETE /api/tasks/{id}` の成功ステータスが `200` （RFC 的には `204`）

削除成功後にリソース表現を返す必要がなければ `204 No Content` が標準的。

**改善提案**: `DELETE /api/tasks/{id}` の成功ステータスを `204 No Content` に変更するか、`200` を返す設計上の根拠（`id` フィールドの返却が必要）を明記する。

---

### 10. `GET /api/tasks/{id}/stream` に `Last-Event-ID` による再接続仕様がない

長時間実行タスクの途中で reconnect した際に出力が欠落する。`Retry-After` 等の再接続ガイダンスも未定義。

**改善提案**: `Last-Event-ID` ヘッダのサポート方針（対応 or 非対応の明示）を SSE エンドポイントのドキュメントに追記する。

---

### 11. `/readyz` エンドポイントが存在しない

`GET /healthz` は liveness probe として定義されているが、依存サービス（`TaskOpsStore` / `ReviewProvider` 等）の稼働確認を行う readiness probe がない。launchd / systemd-user は liveness と readiness を区別する。

**改善提案**: `GET /readyz` を追加し、依存サービスの疎通結果を構造化 JSON で返す（例: `{"status":"ok","checks":{"store":"ok"}}`）。

---

### 12. `validateBoardURL` が内部アドレスをブロックしていない

`http`/`https` スキームのみ許可しているが、`http://127.0.0.1:内部ポート/` のようなリクエストで内部サービスをプローブできる（partial SSRF）。

**改善提案**: `validateBoardURL` に RFC1918 / loopback アドレスの拒否チェックを追加する。`api.md` にも scheme 制限以外の制約（内部アドレス不可）を明記する。

---

### 13. CSRF クッキーの `HttpOnly: false` がセキュリティドキュメントに未記述

XSS が存在した場合にクッキー値を JS で読み取りリプレイ攻撃が可能になる。意図的な設計判断（HTMX/fetch 対応）だが、外部開発者への説明がない。

**改善提案**: `api.md` の CSRF セクションに「クッキーは JS から読み取り可能（`HttpOnly: false`）のため、XSS 対策として localhost のみのバインドが前提」と明記する。

---

### 14. `ParseSinceWindow` の境界値テストと CI race detector が不足

- `?since=0d`、`?since=-1h`、`?since=36501d`、`?since=abc` 等の異常系テストが存在しない。
- CI の `go test` に `-race` フラグがなく、SSE/Hub/LiveStream のゴルーチン並行バグを自動検出できない。
- `?since=invalid_format` のサイレントフォールバック（エラーを無視して全件返す）が仕様として明記・テストされていない。

**改善提案**:
1. `ParseSinceWindow` の単体テストを t_wada TDD スタイルで追加する（テストリスト: `0d`、`-1h`、`36501d`、`abc`、空文字）。
2. `Makefile` の `test` ターゲットを `go test -race ./...` に変更し CI に組み込む。
3. サイレントフォールバックの挙動を `api.md` の `since` パラメータ説明に明記する。

---

### 15. `POST /api/tasks/{id}/send` の `text` バリデーションが `\n` のみのコマンドで誤動作する可能性

`strings.TrimSpace(req.Text) == ""` のチェックにより、`\n`（改行のみ）が空と判定されてしまう。端末に Enter を送信するユースケースが壊れる。

**改善提案**: バリデーションを `len(req.Text) == 0` に変更するか、`\n` 単体を有効な入力として許可する旨を `api.md` に明記する。

---

### 16. "workspace" 用語が cmux session と Git リポジトリで混在

`api.md` では "workspace" が cmux ターミナルセッションを指しているが、要件書では "Workspace = Git リポジトリ = Obsidian Vault" と定義されている。

**改善提案**: `api.md` 内の cmux セッションを指す "workspace" を "cmux workspace" または "task session" に改める。

---

## 🟢 軽微な指摘 / 提案

- `GET /events` のアプリケーションイベント（`task_status_changed` 等）のペイロードスキーマが未定義。外部クライアントが型付けできない。
- `X-Request-Id` レスポンスヘッダの発行仕様がない。デバッグ・トレーシングのために追加を検討。
- 状態遷移図（Mermaid）がない。`pending→running→done/failed`、`skipped→pending` の遷移は図があると理解しやすい。
- `GET /api/metrics` の `daily_counts` が過去何日分を返すかが未定義。`GET /api/review/skipped` との非対称性がある。
- `GET /api/review/skipped` / `GET /api/journal` にページング仕様がない。
- `PATCH /api/tasks/{id}/priority` の `priority` 値域（負数・極値の扱い）が未定義。
- `Cache-Control: no-store` ヘッダが多くのエンドポイントで付与されているが、`api.md` のレスポンスヘッダ表に記載されていない。
- `GET /metrics` が `Accept: text/plain` かつ `*/*` 除外の複雑な Content Negotiation を使用しているが、この挙動（`fetch()` デフォルトでは HTML が返る）が補足されていない。

---

## ✅ 良い点

- CSRF double-submit cookie パターンが `crypto/rand` 32バイト + `subtle.ConstantTimeCompare` で正しく実装されており、全ミューテーション操作に適用されている。
- セキュリティヘッダ（`X-Content-Type-Options`、`X-Frame-Options`、`Referrer-Policy`、CSP）が全レスポンスに付与され、ドキュメントにも明記されている。
- SSE エンドポイント（`/events`、`/api/tasks/{id}/stream`）で `ping` ハートビートと `X-Accel-Buffering: no` が設計されており、リバースプロキシ対策が明示されている。
- `WriteTimeout` を意図的に省略し SSE 永続接続に対応した設計判断がコメントと整合している。
- 各エンドポイントのリクエスト・レスポンス・エラーケースが網羅的に記述されており、API リファレンスとしての実用性が高い。
- Conditional Endpoint Availability テーブルで `TaskOpsStore` / `ReviewProvider` の wiring 条件を明示しており、サーバー設定と機能可用性の関係が明確。
- `Provider` 抽象化（noop フォールバック付き）により、依存サービスなしでも最低限動作する設計になっている。

---

## レイヤー特化レビュー

### Go 実装

良好な点:
- `net/http` + Go 1.22 パターンマッチングのみで構築されており、重量級フレームワークを避けている。
- `ReadHeaderTimeout` / `ReadTimeout` / `IdleTimeout` が明示的に設定されており slow-loris 対策済み。

要対応:
- `embed.go` の `fs.Sub` 失敗時に `panic()` を呼んでいる（Go 慣習上 `NewServer()` のエラーとして返すべき）。
- `skills.go` がエラー詳細をそのまま HTTP ボディに露出（→ 🔴 指摘 6）。
- `writeJSON` が `skills.go` に、`acceptsTextPlain` が `prometheus.go` に分散しており、共有ユーティリティの所在が不統一（`util.go` への集約を推奨）。

### Rust 実装

非該当: 実装言語は Go。

### TypeScript / フロントエンド

- `/partials/dashboard` のポーリング推奨間隔・バックオフ戦略が未定義。電力配慮の観点から SSE トリガー型への移行方針を記述することを推奨。
- `GET /api/tasks/{id}/stream` がフルスナップショットを毎回送信しており、大量出力時の帯域コストが高い。差分（delta）転送への移行方針または設計判断の根拠を明記すること。

### Markdown / Vault 互換

- `done`/`skipped` と `completed`/`cancelled` の乖離（→ 🔴 指摘 4）。
- `POST /api/tasks` で作成されたタスクが Markdown ファイルに書き戻されるか（双方向同期の有無）が未定義。

---

## 機能横断レビュー

### セキュリティ（OpenClaw 教訓照合）

- §11.1「4. 既定バインドはローカル」: デフォルト 127.0.0.1 は達成。ただし `--remote` 時のトークン認証強制（未設定なら起動拒否）が未実装・未記述（→ 🔴 指摘 1）。
- §11.1「5. アウトバウンド制限」: `/api/skills/registry` の domain allowlist 不在（→ 🔴 指摘 2）。
- §11.1「12. インシデント時のキルスイッチ」: API ドキュメントに全停止インターフェースへの言及なし。

### プラガビリティ規約

- `/api/tasks` の 5 動詞（add / list / dispatch / promote / delete）は §6.1 アダプタ規約と概ね整合。
- `GET /api/skills/registry` の非ゴール期間中の先行実装（→ 🔴 指摘 2）。
- `GET /api/project` が GitHub Projects URL に強く結合しており、GitLab / Linear 等の代替ボードへの拡張可能性が考慮されていない（Phase 3 限定であれば明記を）。

### HTTP API 設計

- API バージョニングなし（将来の後方互換ポリシー定義と合わせて要検討）。
- エラーレスポンス形式の非統一（→ 🟡 指摘 8）。
- `SSE data:` フォーマットが `event:` 種別ごとに未定義。

### 常駐運用 / Daemon Runtime

- `/readyz` 欠落（→ 🟡 指摘 11）。
- `GET /healthz` のレスポンスが `ok` プレーンテキストのみで、バージョン・稼働時間等の構造化情報がない。
- アクセスログのパス（`~/.marunage/logs/`）やローテーション仕様が API ドキュメントに記述されていない。

### データモデル / SQLite

- `judgment_reason` フィールドが tasks テーブルのどのカラム（専用カラム vs. metadata JSON）に対応するか不明。
- `updated_at` の自動更新がドキュメントで言及されていない。

### テスト戦略（t_wada TDD）

- `ParseSinceWindow` の境界値テストと CI race detector が不足（→ 🟡 指摘 14）。
- `?since=invalid_format` のサイレントフォールバックが仕様・テストどちらにも未記述。

### 非ゴール / 要件整合性

- `GET /api/skills/registry` が Non-Goal（v0.3 まで中央集権レジストリ禁止）を先行実装（→ 🔴 指摘 2）。
- `POST /api/tasks/{id}/send` の人間承認モード省略（外部発信の確認なしが Non-Goal に抵触する可能性）。
- "workspace" 用語の混在（→ 🟡 指摘 16）。

---

## 📝 総合チェックリスト

### 全体
- [ ] ドキュメント構造の完全性（§1）: 背景・非対象・リスク・テスト戦略・ロールバック条件が欠落（API リファレンスとしての位置づけを冒頭に明記すること）
- [x] REQUIREMENTS.md との整合性（§2）: 概ね整合（status 列挙値の乖離を除く）
- [ ] セキュリティ既定条件（§3）: `--remote` 時の認証強制・SSRF 対策が未実装
- [ ] プラガビリティ規約（§4）: レジストリの非ゴール期間明示が必要
- [ ] Markdown SSOT / Vault 互換（§5）: status 列挙値の乖離、手動作成タスクと Markdown の対応関係が不明
- [ ] 常駐運用 / 静粛性（§6）: `/readyz` 欠落
- [ ] 観測性 / 介入可能性（§7）: `request_id` 未定義、audit log 記録の明示なし
- [ ] 図表の適切性（§8）: 状態遷移図・SSE シーケンス図が欠落
- [ ] Phase / 実装計画（§9）: エンドポイントの追加バージョンが未記述
- [ ] テスト方針（§10）: CI race detector 欠落、境界値テスト不足
- [x] ライセンス / OSS 衛生（§11）: 問題なし
- [ ] 用語の一貫性（§12）: "workspace" の混在、status 列挙値の乖離

### レイヤー
- [ ] Go: `embed.go` の panic、skills エラー露出、shared util の分散
- [x] Rust: 非該当
- [ ] TypeScript: ポーリング間隔未定義、フルスナップショット転送のトレードオフ未記述

### 機能横断
- [ ] Security: `--remote` 認証強制、SSRF（`/api/skills/registry`、`/api/project?board_url`）
- [ ] Pluggability: レジストリ非ゴール期間の扱い
- [ ] HTTP API: バージョニング、エラー形式統一、`Last-Event-ID`
- [ ] Daemon Runtime: `/readyz`、バインドアドレス記述
- [ ] Data Model: status 列挙値 SSOT、`running` タスク削除
- [ ] Test Strategy: race detector、境界値テスト

---

## 🧭 次アクション

- [ ] **必須対応（🔴）**: `api.md` の Overview に `--remote` 時の認証要件・デフォルトバインドアドレス（`127.0.0.1:7777`）を明記（担当: ドキュメント修正のみ、即対応可）
- [ ] **必須対応（🔴）**: `GET /api/skills/registry` を Conditional Endpoint Availability テーブルに追加し、v0.3 以降・`RegistryURL` 設定必須として明示
- [ ] **必須対応（🔴）**: `DELETE /api/tasks/{id}` に `running` 状態ガードを追加（`409 Conflict`）、ドキュメントに反映
- [ ] **必須対応（🔴）**: status 列挙値を `api.md` 冒頭の Mermaid 状態遷移図で確定し、REQUIREMENTS.md と統一
- [ ] **必須対応（🔴）**: `skills.go` のエラー詳細露出を `handler.go` パターンに統一
- [ ] **推奨対応（🟡）**: `ParseSinceWindow` の境界値テスト追加・CI に `-race` フラグ追加
- [ ] **推奨対応（🟡）**: `/readyz` エンドポイントの追加検討（次 PR）
- [ ] **推奨対応（🟡）**: `POST /api/tasks/{id}/send` の `text` バリデーション修正（`\n` 問題）

---

## 総評

`api.md` は API リファレンスとして完成度が高く、各エンドポイントのリクエスト・レスポンス・エラーケースが網羅的に記述されている。CSRF 二重送信パターン・セキュリティヘッダ・SSE ハートビートなど、実装と整合した技術的詳細も正確に反映されている。一方、`--remote` モード時の認証欠落と `/api/skills/registry` の SSRF リスクは OpenClaw §11.1 に直接抵触する重大課題であり、優先対応が必要。デフォルトバインドアドレスの記述不整合（`localhost:8080` vs `127.0.0.1:7777`）も外部公開リスクの観点で早急に修正すべきである。ローカル限定の運用前提を維持しつつ、`--remote` 時の安全な公開パスを設計ドキュメントレベルで確立することが次のマイルストーンとなる。
