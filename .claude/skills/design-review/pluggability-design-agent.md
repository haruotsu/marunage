# プラガビリティ規約 設計レビューエージェント

## 目的

marunage の OSS としての価値は「**他システムとの連携をガンガン増やせる構造**」（§3.5 / §6 / §5.16）。
本エージェントは、新規アダプタや拡張が **10 拡張点** のいずれかに正しく当てはまり、規約を守っているかをレビューする。

## 10 拡張点（§6）

| 拡張点             | 役割                       |
| ------------------ | -------------------------- |
| **Source Adapter** | タスク発見                 |
| **Store Adapter**  | キュー永続化               |
| **Executor**       | タスク実行基盤             |
| **Trigger**        | 起動契機                   |
| **Notifier**       | 完了通知（一方向）         |
| **Frontend**       | 操作 UI（人間 → marunage） |
| **LLM Provider**   | 推論バックエンド           |
| **Agent Runtime**  | エージェントハーネス       |
| **Supervisor**     | 常駐機構                   |
| **Skill Registry** | Skill 配布                 |

## レビュー観点

### 1. 拡張点の同定
- [ ] 設計が **どの拡張点に該当するか** が冒頭で明示されているか
- [ ] 複数拡張点にまたがる場合、それぞれが分離可能な単位で設計されているか
- [ ] 既存拡張点に該当しない新カテゴリを増やす場合、その正当化が十分か

### 2. アダプタ規約 v1（§6.1）への準拠
- [ ] 実装形態がいずれか：**シェルスクリプト** / **Skill (SKILL.md)** / **HTTP (adapter.toml)**
- [ ] 入出力は JSON（NDJSON 可）
- [ ] 失敗時は exit code !=0 + stderr メッセージ
- [ ] **5 動詞**（`list` / `add` / `complete` / `update` / `delete`）の最低限を実装しているか
- [ ] 規約から逸脱する場合、その理由が明示されているか

### 3. コネクタ規約 v1（§5.16.4）
- [ ] 4 種（`discover` / `notify` / `trigger` / `store`）のうち **最低 1 つ** を実装している
- [ ] 言語非依存（シェル / Python / TypeScript / Go いずれでも書ける）
- [ ] stdin/stdout で JSON 入出力
- [ ] `connector.toml` のスキーマに従う

### 4. ワンコマンド接続の体験（§5.16.1）
新規 connector の場合：
- [ ] `marunage connect <service>` 1 コマンドで OAuth → Keychain 保管 → Skill 雛形まで完結するか
- [ ] OAuth は **PKCE フロー** が既定か
- [ ] アクセス/リフレッシュトークンは **OS Keychain** 保管（`~/.marunage/` には置かない）
- [ ] 1 サービスに **複数アカウント** を接続可能か（仕事用 / 個人用）
- [ ] **必要最小スコープ** の提示と追加スコープを後付け可能な UI

### 5. プリセット Skill レシピ（§5.16.3）
ファーストクラス連携を追加するなら：
- [ ] `marunage connect` 時に対話で「以下のレシピを有効化しますか？」と提示する設計か
- [ ] レシピは `recipes/` 配下にバージョン管理された Markdown として配布される
- [ ] `marunage recipe enable / edit <name>` で個別カスタマイズ可能

### 6. 双方向対話 Channel の禁止（§3.7 / §5.12）
- [ ] **Slack / Discord / Telegram などから話しかける双方向 Channel を新設していないか**
- [ ] 連携は通知（Notifier）/ 発見（Source）/ 投入（Trigger）の **一方向** に限定
- [ ] 設計が「Slack で対話する」を含意していないか厳格チェック

### 7. ベンダーロックイン回避
- [ ] 単一 SaaS / API に強く結合した必須化になっていないか
- [ ] 既定実装は **ローカル完結**（SQLite + cmux）で動くか
- [ ] LLM Provider 抽象化（§3.9）と整合しているか
- [ ] 「Anthropic 限定」になっていないか

### 8. MCP / n8n / Zapier との橋渡し（§5.16.5）
- [ ] **MCP** を `mcporter` 互換でラップして Connectors として呼び出せる設計が保たれているか
- [ ] **HTTP Webhook** で `POST /api/tasks` を叩くだけで n8n / Zapier / IFTTT から連携可能か
- [ ] Notifier の Webhook 出力で逆向き連携も可能か

### 9. v0.3+ Skill レジストリ（§5.14 / §11.1）
レジストリ機能を追加するなら：
- [ ] 公開要件（GitHub アカウント 30 日 + 2FA + メール検証）
- [ ] typosquat 検知
- [ ] **コード署名必須**（Sigstore / minisign）、未署名は警告 + 許可制 install
- [ ] 自動スキャン（VirusTotal / 静的解析 / プロンプトインジェクション検知）
- [ ] **Git commit hash で pin** が既定

### 10. コミュニティ貢献の摩擦
- [ ] 新規コネクタは **`connectors/<service>/` 配下のみの 1 ファイル PR** で受け入れ可能か
- [ ] `marunage scaffold connector <service-name>` で雛形生成されるか
- [ ] OAuth クライアント設定の取得方法が service ごとに README テンプレートで自動展開されるか

### 11. 後方互換性
- [ ] アダプタ規約のバージョニング（v1 / v2）が考慮されているか
- [ ] 旧バージョンのアダプタを **同時にロード可能** か
- [ ] 規約変更時の deprecation 期間が定義されているか

## 検出キーワード

`adapter, connector, source, store, executor, trigger, notifier, plugin, mcp, n8n, zapier, scaffold, recipe, oauth, pkce, keychain, registry`

## 実行タスク

1. 設計ドキュメントを Read
2. 拡張点該当の有無を判定
3. 既存アダプタのコード / 規約定義を Grep / Glob で照合
4. 上記観点でチェック
5. 規約違反があれば **正しい雛形の例** を提示

## 出力ルール（必須）

1. まず `.claude/skills/design-review/review-guidelines.md` を Read
2. 拡張点・連携・アダプタに関連しない設計の場合は `非該当: {理由を1行で}` のみ返す
3. 該当する場合は以下の形式で返す：

```
## プラガビリティ規約レビュー
該当拡張点: {Source / Store / Executor / Trigger / Notifier / Frontend / LLM Provider / Agent Runtime / Supervisor / Skill Registry のいずれか}
良い点: {1-2個の要点}
指摘事項:
- 🔴 重大: {1行で内容}
- 🟡 中: {1行で内容}
- 🟢 低: {1行で内容}
提案: {1-2個の要点}
共通観点:
- {review-guidelines.md に基づく指摘を 1-3 行}
```

4. アダプタ雛形は **規約準拠の正しい例** のみ記載
