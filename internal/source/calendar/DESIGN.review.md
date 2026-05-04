# DESIGN.md 設計レビュー結果

## 📋 対象ドキュメント
- パス: `internal/source/calendar/DESIGN.md`
- タイトル: PR-81 Google Calendar Discovery source — design
- 対象 version: v0.1（Phase 2、PR-81）
- 影響レイヤー: go
- 関連領域: security / pluggability / daemon-runtime / data-model / test-strategy / non-goal-consistency

## 🔴 重大な指摘（必須対応） — 修正済み

### 1. DST 跨ぎでの境界計算ミス（daemon-runtime + test-strategy）
**問題点**: `timeMax = timeMin.Add(24*time.Hour)` は壁時計加算のため、DST spring-forward 日（例: America/New_York 2026-03-08）に翌日 00:00 を 1 時間オーバーシュートし、当日アジェンダに翌朝の予定が滲み込む。
**改善提案**: `nextMidnight(t)` を `time.Date(y, m, d+1, 0, 0, 0, 0, t.Location())` で計算するヘルパーに置換。
**対応**: ✅ `calendar.go:nextMidnight` 追加、`Plugin.List` 更新、`TestPluginListBoundarySurvivesSpringForwardDST` (C15) で回帰防止。

### 2. cancelled exception 通過（data-model）
**問題点**: `singleEvents=true` で取得すると recurring の削除済み instance が `status="cancelled"` で返る。`Plugin.List` がこれを通すと PR-71 の materialiser が孤児行を作る。
**改善提案**: `Event.Status` を持ち、`Plugin.List` で `cancelled` を declined と並んでフィルタ。
**対応**: ✅ `EventCancelled` 定数追加、Plugin で 2 段フィルタ、`TestPluginListSkipsCancelledEvents` (C14) と `TestGWSListEventsParsesEventStatus` (G7)。

### 3. DefaultRunner の型アサーション漏れ（go）
**問題点**: `err.(*exec.ExitError)` はラップされた error を見落とす。
**改善提案**: `errors.As(err, &exitErr)` に置換。
**対応**: ✅ 修正。同時にラップ済みエラーから stderr 平文を落とし、PII / トークン漏洩経路を断つ（OpenClaw §11.1-7 反面教師）。

### 4. Setup が Status エラーを握りつぶす（go + security）
**問題点**: `if status, _ := c.Status(ctx);` で gws バイナリ不在やネットワーク断が "未認証" と区別不能。
**改善提案**: `probe(ctx)` を抽出し、Status は I/O 失敗を `AuthNotConfigured` に降格、Setup の対話パスは probe 直叩きで runner error を verbatim に伝搬。
**対応**: ✅ 実装、`TestGWSSetupInteractiveSurfacesProbeError` (G8) で固定。

## 🟡 中程度の指摘（推奨対応） — 一部対応・残りは Open Question 化

### 1. 観測性ノードと撤退条件が DESIGN に欠落（go）
- DESIGN にリスク／ロールバック／オープンクエスチョン節がない。Setup / Status / List の slog event 名と field allowlist を将来 PR-71 と合わせて固める必要あり。
- **対応**: PII 取り扱いポリシーは「Subprocess and PII policy」節として追加（stderr 落としと RawMetadata 経由の field-allowlist 責務を明示）。観測性 schema と監査連携の詳細は PR-71 観測性 PR で確定する旨を Open Question として残置。

### 2. RawMetadata の `start`/`end` vs `start_date`/`end_date` 二系統（data-model）
- SQLite JSON1 の `json_extract($.start)` で all-day が空 hit するため将来クエリ性で痛む可能性。
- **対応**: PR-71 の materialise 設計時に再検討。`all_day` フラグで型分岐できるため当面は読み取り側で吸収、設計簡潔さを優先。

### 3. ExternalID の安定性（data-model）
- recurring の編集済み instance / cancelled exception の id 安定性は Google の `singleEvents=true` 仕様に依存。multi-calendar 対応時に `(calendarId, eventId)` 正規化が必要になる可能性。
- **対応**: `Out of scope` に「multi-calendar」と明記済み。`(source, external_id)` UNIQUE は primary 単独運用で衝突しない。multi-calendar PR 時に再設計。

### 4. Subprocess timeout / kill group（daemon-runtime）
- `exec.CommandContext` で ctx kill は効くが、グループキル（`Setpgid`）は未設定。
- **対応**: PR-81 の射程は plugin 内部のみ。daemon ループ側 (PR-71) の責務として Open Question 化。

### 5. TZ 変更時の Location キャッシュ（daemon-runtime）
- `time.Now().Local()` は process 起動時の `time.Local` を見続ける。
- **対応**: DESIGN の Day boundary 節に TZ 変更時の限界を明記。PR-81 では out of scope。

## 🟢 軽微な指摘 / 提案

- `Event` struct のゴルーチン安全性: now 関数差し替えはコンストラクタ後のみ想定。 → 既に `New` で確定するため問題なし。
- testdata ゴールデンファイル導入: 将来の Google API レスポンス回帰検出として有用。 → 別 PR 候補。
- `DisallowUnknownFields`: 想定外フィールドの黙認を防ぐ。 → 現状 minimal subset 抜き出しで漏出経路は閉じている。Open Question。

## ✅ 良い点

- **read-only コントラクトの型レベル強制**: Adapter が Adder/Completer/Deleter/Sincer を実装しないことを `TestAdapterDoesNotImplementOptionalCapabilities` (A8) で固定。pluggability 規約と完全一致。
- **gws delegate-cli 委譲**: OAuth トークン管理を gws 側に閉じ込め、本パッケージはトークンを保持・永続化しない。OpenClaw §11.1-1 反面教師の素直な実装。
- **Client interface seam**: 3 メソッド（ListEvents / Status / Setup）の最小設計。production の GWSClient と test の fakeClient が同一契約で動き、google-api-go-client への将来 swap も容易。
- **Runner 注入**: GWSClient の `WithRunner` で gws JSON パーサとコマンド形状を offline テスト可能。
- **manifest の自己検証**: `RegisterBuiltin` 時に `ValidateAgainstManifest` で capability ↔ interface 整合性を起動時にチェック。drift は first-dispatch でなく registration で発覚。
- **t_wada TDD の徹底**: test_list.md に Red→Green サイクル単位で番号付け、各テストにコメント `— C5` 等で対応関係を明示。

---

## レイヤー特化レビュー

### Go 実装
良い点: io seam の絞り込み（3 メソッド）、`startOfDay`/`nextMidnight` の time.Date 使用、機能オプションスタイルの統一（Plugin / GWSClient で揃い）、A8 のコンパイル時 read-only 保証。
重大: ExitError の type assertion → `errors.As`、Setup の Status error 握りつぶし、stderr 経由の PII 漏洩経路。すべて対応済み。

### Rust 実装
非該当: 純 Go 実装、ABI / FFI / Rust crate 関連なし。

### TypeScript / フロントエンド
非該当（補足のみ）: 将来 Web UI 化時の API 形状で `RawMetadata.start`/`start_date` の二系統分岐は OpenAPI oneOf 化が望ましい。

### Markdown / Vault 互換
非該当: Markdown SSOT / Vault 構造には触れない。ドキュメント自身の Markdown 構造は適切。

---

## 機能横断レビュー

### セキュリティ（OpenClaw 教訓照合）
- §11.1-1 クレデンシャル保存: ✅ gws 委譲で本パッケージは保持しない。
- §11.1-7 ログ漏洩: ⚠️ → ✅ DefaultRunner の stderr バンドルを除去、DESIGN に PII 取り扱い節を追加。
- §11.1-5 アウトバウンド allowlist: gws 経由のため direct allowlist 対象外。`marunage init` 側 doctor で gws 存在確認済み前提。

### プラガビリティ規約
- sync_mode=read-only と capabilities=[list, setup, auth-status] が `Adder/Completer/Deleter/Sincer` 非実装と完全一致。
- ValidateAgainstManifest で起動時 fail-loud。Manifest version bump で `Sincer` を後付け追加可能（後方互換）。

### HTTP API 設計
非該当: 新規 HTTP endpoint なし。

### 常駐運用 / Daemon Runtime
- 日跨ぎロールオーバー: List 毎に now() 読み直し ✅。
- DST: nextMidnight ヘルパーで対応済み ✅。
- ctx 伝搬: `exec.CommandContext` 経由で kill on cancel ✅。
- subprocess timeout / kill group / 連続失敗ヘルス: 残課題は PR-71 daemon の責務。

### データモデル / SQLite
- ExternalID = event.id の安定性: primary 単独 + singleEvents=true で当面 OK。
- cancelled instance の handling: フィルタ済み ✅。
- RawMetadata 二系統 (start vs start_date): all_day フラグで型分岐、当面は読み取り側で吸収。

### テスト戦略（t_wada TDD）
- C / A / B / G の 4 tier、Red→Green サイクル単位で 27 テスト（C15・G8 追加後）。
- DST テスト追加 ✅。cancelled テスト追加 ✅。
- 不正 JSON / 空 items / 余計フィールドの forward-compat は未カバー（軽微）。

### 非ゴール / 要件整合性
- 双方向同期しない / triage はしない / multi-calendar しない / recurring 展開は API 任せ — すべて遵守。
- requirement.md の delegate-cli 委譲（§461）と整合。
- Out of scope 節で webhook / triage hand-off / CLI wiring の責務分離が明示。

---

## 📝 総合チェックリスト

### 全体
- [x] ドキュメント構造の完全性（PII 節追加、Open Question は本ファイルで併記）
- [x] REQUIREMENTS / pr_split_plan.md PR-81 セクションとの整合性
- [x] セキュリティ既定条件（gws 委譲、stderr 落とし）
- [x] プラガビリティ規約（read-only manifest、ValidateAgainstManifest）
- [x] 常駐運用 / 静粛性（List 毎 now()、DST 対応）
- [x] 観測性: PII allowlist は PR-71 にバトン
- [x] テスト方針（t_wada TDD、test_list.md）
- [x] 用語の一貫性

### Go レイヤー
- [x] errors.As の使用
- [x] context 伝搬（exec.CommandContext）
- [x] read-only の型レベル強制
- [x] interface seam の最小性

### 機能横断
- [x] セキュリティ（OpenClaw §11.1-1 / -7）
- [x] プラガビリティ
- [x] daemon-runtime（DST、ctx）
- [x] data-model（cancelled / ExternalID）
- [x] test-strategy（DST、cancelled テスト追加）
- [x] non-goal-consistency

## 🧭 次アクション

- [x] 必須対応（🔴）4 件すべて修正・テスト追加・コミット完了
- [ ] 推奨対応（🟡）の Open Question を PR-71（discover ループ）と PR-91（観測性）に持ち越し
  - subprocess kill group / timeout 契約
  - audit JSONL の field-allowlist 設計
  - RawMetadata の `start`/`start_date` 二系統 → 単一化検討
  - multi-calendar 対応時の ExternalID 正規化
- [ ] 軽微（🟢）はバックログ
  - `DisallowUnknownFields` 採用検討
  - testdata ゴールデンファイル導入

## 総評

PR-81 は read-only / delegate-cli / interface seam という三本柱で marunage の核となる思想（Markdown SSOT、双方向 Channel 非ゴール、OpenClaw 反面教師、モデル中立、ローカル完結）を素直に守っており、設計レビューが指摘した真の不具合 4 件はいずれも本 PR スコープ内で修正可能だった。残課題はすべて PR-71+ の daemon ループ / 観測性 PR の責務に切り分け済み。t_wada TDD の test_list.md と Red→Green の対応関係も明示されており、層内設計として完成度が高い。
