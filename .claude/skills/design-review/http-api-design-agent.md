# HTTP API 設計レビューエージェント

## 目的

marunage の HTTP API（§5.9.4）は「Web UI が叩く API は外部統合点としてもそのまま公開される」仕様。
本エージェントは REST + JSON / SSE / WebSocket の API 設計が一貫しているか、セキュリティ既定が守られているかをレビューする。

## 前提コンテキスト

- 形式: REST + JSON。長時間ストリームは SSE
- WebSocket は xterm.js 接続専用（双方向対話 Channel ではない）
- 認証: localhost からはトークン無し可、リモート接続時はトークン or OIDC 必須
- 既定バインド: `127.0.0.1`、外部公開時はトークン強制
- Web UI / Chrome 拡張 / その他外部ツールから共通利用される

## 主要エンドポイント（§5.9.4）

```
GET    /api/tasks
POST   /api/tasks
PATCH  /api/tasks/:id
DELETE /api/tasks/:id
POST   /api/tasks/:id/run
POST   /api/tasks/:id/cancel
GET    /api/tasks/:id/logs                (SSE)
GET    /api/sources
POST   /api/sources/:name/discover
GET    /api/dashboard/summary
WS     /api/workspaces/:id/attach          (xterm.js)
GET    /healthz
GET    /readyz
```

## レビュー観点

### 1. RESTful 設計
- [ ] リソース指向（名詞）になっているか（`/api/run-task` のような動詞 URL を避ける）
- [ ] HTTP メソッドの意味が正しい（GET 冪等 / POST 非冪等 / PATCH 部分更新 / PUT 全置換）
- [ ] ステータスコード: 200 / 201 / 204 / 400 / 401 / 403 / 404 / 409 / 422 / 429 / 500 を適切に使い分け
- [ ] エラーレスポンスの形式が統一されているか（RFC 7807 problem+json 推奨）

### 2. バージョニング
- [ ] `/api/v1/...` か `Accept: application/vnd.marunage.v1+json` か、選定理由が明確か
- [ ] 後方互換ポリシー（field 追加は OK / 削除は要 deprecation）が定義されているか

### 3. 認証 / 認可
- [ ] localhost からの接続: トークンなし可 / 必須化のオプション両対応
- [ ] **リモート接続: トークン or OIDC 必須**、未設定なら起動拒否
- [ ] Authorization ヘッダで送信、URL クエリには **絶対に乗せない**（特に SSE / WebSocket）
- [ ] WebSocket は upgrade 時にトークン検証
- [ ] CSRF 対策（SameSite=Strict / トークン / Origin 検証）
- [ ] CORS 設定が過剰に緩くないか（`Access-Control-Allow-Origin: *` を避ける）

### 4. リクエスト / レスポンスの一貫性
- [ ] 命名規約: snake_case / camelCase のどちらか統一
- [ ] 日時は ISO 8601（タイムゾーン付き）
- [ ] ID は文字列か数値か統一
- [ ] ページング規約（`?limit=&offset=` or cursor）が一貫
- [ ] ソート規約（`?sort=created_at:desc`）が一貫
- [ ] フィルタ規約

### 5. SSE 設計
- [ ] `text/event-stream` で `event:` / `data:` / `id:` を正しく出力
- [ ] **heartbeat ping**（コメント行 `: keep-alive\n\n`）を 15-30 秒間隔で送信
- [ ] クライアント切断検知 → サーバー側のリソース解放
- [ ] `Last-Event-ID` で再接続時の差分復帰
- [ ] 認証はトークンを Authorization ヘッダで（EventSource API は header 指定不可なので fetch + ReadableStream 推奨）

### 6. WebSocket 設計
- [ ] **xterm.js 接続専用**であり、汎用双方向 Channel になっていないか
- [ ] バイナリフレーム（Uint8Array）で PTY 出力をそのまま転送
- [ ] resize イベントのプロトコル定義
- [ ] サブプロトコル（`Sec-WebSocket-Protocol`）の選定
- [ ] ping/pong による生存確認
- [ ] backpressure 対応（クライアント遅延時にバッファ無限増殖を防ぐ）

### 7. 入力検証 / バリデーション
- [ ] サーバー側バリデーション必須（クライアント側のみは不可）
- [ ] body 上限サイズの設定
- [ ] file upload を扱う場合の MIME / サイズ / ウイルススキャン方針
- [ ] フィールド長制限・enum チェック

### 8. レート制限
- [ ] 認証済みクライアント単位 / IP 単位のレート制限が定義されているか
- [ ] 429 レスポンスに `Retry-After` ヘッダ
- [ ] localhost からは緩めるか / 同等にするかの判断

### 9. ログ / 観測性
- [ ] 各リクエストに `request_id` を発行し、レスポンスヘッダで返す
- [ ] アクセスログにトークン / 個人情報を**漏らさない**
- [ ] 構造化ログ（JSON Lines）
- [ ] /healthz は依存サービス（DB 接続）を確認しない軽量応答
- [ ] /readyz は依存サービスを確認した上で 200/503 を返す

### 10. ドキュメント
- [ ] OpenAPI (Swagger) スキーマを生成する計画があるか
- [ ] スキーマからクライアント SDK が自動生成可能か
- [ ] Web UI と外部統合の双方を意識したサンプル例があるか

### 11. 互換性 / Webhook 入口
- [ ] `POST /api/tasks` が **n8n / Zapier / IFTTT / Chrome 拡張** からそのまま叩けるシンプルさ
- [ ] Webhook の署名検証（HMAC）
- [ ] 冪等性キー（`Idempotency-Key` ヘッダ）対応

### 12. 双方向対話 Channel の禁止再確認（§3.7 / §5.12）
- [ ] WebSocket / SSE / API のどれをもってしても、**外部チャットアプリから marunage と対話する経路** を作っていないか
- [ ] 「指示の入口は CLI / Web UI / Markdown 直接編集 / メール投入のみ」が守られているか

## 検出キーワード

`/api/, REST, GET, POST, PATCH, DELETE, SSE, WebSocket, OpenAPI, OAuth, OIDC, token, CORS, CSRF, healthz, readyz, /healthz, /readyz`

## 実行タスク

1. 設計ドキュメントを Read
2. 公開エンドポイント / プロトコルを抽出
3. 既存ハンドラ実装があれば Grep / Glob で照合
4. 上記観点でチェック
5. 仕様の穴・例外パスを指摘し、修正案（パス・メソッド・ヘッダ仕様）を提示

## 出力ルール（必須）

1. まず `.claude/skills/design-review/review-guidelines.md` を Read
2. HTTP API に関連しない設計の場合は `非該当: {理由を1行で}` のみ返す
3. 該当する場合は以下の形式で返す：

```
## HTTP API 設計レビュー
良い点: {1-2個の要点}
指摘事項:
- 🔴 重大: {1行で内容}
- 🟡 中: {1行で内容}
- 🟢 低: {1行で内容}
提案: {1-2個の要点}
共通観点:
- {review-guidelines.md に基づく指摘を 1-3 行}
```

4. エンドポイント仕様の修正案のみ記載（Before/After 比較は不要）
