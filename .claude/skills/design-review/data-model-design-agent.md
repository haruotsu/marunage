# データモデル / SQLite 設計レビューエージェント

## 目的

marunage は **Markdown が SSOT、SQLite + FTS5 + ベクトルは検索インデックスとして従属**（§3.8 / §5.13.5 / §7）。
本エージェントは tasks / kv_state / 検索インデックス / 同期メタデータの設計をレビューする。

## 前提コンテキスト

- 既定 Store: SQLite (WAL モード)
- スキーマ: §7.1 `tasks` / §7.2 `kv_state`
- 一意制約: `(source, external_id)` で重複登録を防ぐ
- atomic sentinel パターンで完了検知
- DB は Vault の **外**（`~/.marunage/index/`）に置き、Vault 同期に巻き込まない
- Markdown を消したらタスクが消える、編集したら反映される

## レビュー観点

### 1. tasks テーブル（§7.1）
- [ ] 必須カラム: `id` `source` `external_id` `title` `body` `status` `priority` `cwd` `workspace_ref` `metadata` `created_at` `updated_at`
- [ ] `status` の enum（`pending|running|completed|failed|cancelled`）が CHECK 制約 or アプリ層で保証
- [ ] `metadata` JSON のスキーマ進化方針（未知キーは保持）
- [ ] `UNIQUE(source, external_id)` の重複登録防止
- [ ] インデックス: `(status, priority DESC, id)` 等の dispatch クエリ最適化
- [ ] `updated_at` の自動更新（トリガー or アプリ層）

### 2. kv_state テーブル（§7.2）
- [ ] チェックポイント用途: `slack_last_ts` / `gmail_last_id` 等のキー命名規約
- [ ] 値の暗号化が必要なものを区別する設計か（トークン類は OS Keychain へ、ここには置かない）
- [ ] 履歴保持の必要性（変更履歴を追えるか / 上書きで十分か）

### 3. WAL / 並行性
- [ ] WAL モード有効化が初回マイグレーションに含まれているか（`PRAGMA journal_mode=WAL`）
- [ ] `synchronous = NORMAL` の妥当性
- [ ] `busy_timeout` の設定値
- [ ] 並列実行時のロック衝突想定（同時実行 N=3 + Heartbeat + Discovery）

### 4. Markdown ↔ DB 同期
- [ ] Markdown 編集 → DB 反映の **watcher** 設計
- [ ] DB 編集 → Markdown 反映の必要性（基本は片方向：MD → DB）
- [ ] ファイル削除を検知して DB から消す
- [ ] 同期失敗時の `marunage refresh` で完全再構築が冪等
- [ ] ファイル名と `id` の関係（リネーム時の追跡 / `id` を frontmatter に持つかファイル名に持つか）

### 5. atomic sentinel パターン（§5.1）
- [ ] 完了検知が `.exit_code.tmp` → `mv .exit_code` で実装されているか
- [ ] WAL + sentinel の組み合わせで「プロセス強制終了でもキュー一貫性が壊れない」が満たされるか
- [ ] sentinel の置き場所（Workspace 内 or `~/.marunage/run/`）

### 6. マイグレーション
- [ ] スキーマバージョン管理（`schema_migrations` テーブル等）
- [ ] 前方互換: 旧バイナリで新スキーマを読み出した場合の挙動
- [ ] ロールバック手順
- [ ] 大量データ時のオンライン migration 戦略

### 7. FTS5 / ベクトル検索
- [ ] FTS5 仮想テーブル設計（content table との triggers / external content）
- [ ] BM25 ランキング（既定）
- [ ] **`sqlite-vec`** の導入方針：オプトイン、ローカル埋め込み既定（`embeddinggemma-300m` 等）
- [ ] **70% vec + 30% BM25 のハイブリッド**スコアの実装方針
- [ ] リモート埋め込み API の利用は明示オプトイン
- [ ] インデックス再構築のバックグラウンド戦略

### 8. DB の置き場所と Vault 互換性
- [ ] **DB は Vault の外**（`~/.marunage/index/`）に置く
- [ ] Vault 同期（Obsidian Sync / iCloud / Syncthing / Git）に巻き込まれない
- [ ] DB は再生成可能な検索インデックスとして位置付けられている
- [ ] Vault のみ同期した先（別マシン）で `marunage refresh` で DB を再構築できる

### 9. データ保護
- [ ] DB ファイルのパーミッション（Unix 0600）
- [ ] バックアップ戦略（オンラインバックアップ API 利用）
- [ ] 暗号化が必要な場合の選択（SQLCipher / OS-level encryption）
- [ ] DB が破損した場合の検出と復旧手順

### 10. クエリ性能
- [ ] dispatch クエリ（pending を優先度順に N 件 claim）の EXPLAIN QUERY PLAN が想定通りか
- [ ] N+1 になりやすいパターンを避ける設計（join / 一括取得）
- [ ] 古い completed / failed タスクの archive / vacuum 戦略

### 11. metadata JSON
- [ ] JSON1 拡張の利用（`json_extract` / `json_each`）
- [ ] スキーマレスにしすぎてクエリ困難にしない（よく使うキーは別カラムに昇格）
- [ ] サイズ上限（極端な巨大 JSON を防ぐ）

### 12. 観測性
- [ ] 全 SQL に `request_id` をコメントで付与してログから追える設計か
- [ ] スロークエリログ
- [ ] DB サイズ / WAL サイズ / 接続数のメトリクス

## 検出キーワード

`SQLite, FTS5, sqlite-vec, WAL, schema, migration, tasks, kv_state, frontmatter, sentinel, embedding, BM25, ベクトル, ハイブリッド, watcher`

## 実行タスク

1. 設計ドキュメントを Read
2. データモデル / マイグレーション / 同期戦略を抽出
3. 既存スキーマ・SQL があれば Grep / Glob で照合
4. 上記観点でチェック
5. スキーマ修正案 / インデックス追加案を提示

## 出力ルール（必須）

1. まず `.claude/skills/design-review/review-guidelines.md` を Read
2. データモデル / DB に関連しない設計の場合は `非該当: {理由を1行で}` のみ返す
3. 該当する場合は以下の形式で返す：

```
## データモデル / SQLite レビュー
良い点: {1-2個の要点}
指摘事項:
- 🔴 重大: {1行で内容}
- 🟡 中: {1行で内容}
- 🟢 低: {1行で内容}
提案: {1-2個の要点}
共通観点:
- {review-guidelines.md に基づく指摘を 1-3 行}
```

4. SQL / スキーマ修正案のみ記載（Before/After 比較は不要）
