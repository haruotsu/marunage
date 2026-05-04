# pr_203_skill_registry_design.md 設計レビュー結果

## 📋 対象ドキュメント
- パス: `docs/pr_203_skill_registry_design.md`
- タイトル: PR-203 Shared Skill Registry — Design Doc
- 対象 version: v0.3 相当（中央集権 Skill レジストリの初期実装）
- 影響レイヤー: go / markdown
- 関連領域: security / pluggability / http-api / daemon-runtime / data-model / test-strategy / non-goal-consistency

## 🔴 重大な指摘（必須対応）

### 1. HTTPS only ではなく http も既定許容している
**問題点**: `assertSafeScheme` が `http` も `https` も無条件で通すため、平文経路で manifest 自体を MITM で書き換えられれば pin された sha256 まで攻撃者の digest に置換される。OpenClaw §11.1 #10 のレジストリ運用教訓と直接衝突。
**改善提案**: 既定は **HTTPS only** とし、`http` は `--insecure` または `MARUNAGE_SKILLS_REGISTRY_ALLOW_HTTP=1` のような明示的 opt-in に限定する。
**根拠**: review-guidelines §3 セキュリティ既定条件 / OpenClaw 反面教師 #10。

### 2. コード署名・サンドボックスの欠如と REQUIREMENTS との不整合
**問題点**: §6 で署名・サンドボックスを「非ゴール」と宣言しているが、REQUIREMENTS 側は「未署名 Skill は警告 + 許可制 install」「サードパーティ Skill のサンドボックスなし実行は禁止（既定）」を求めている。HTTPS + sha256 のみではレジストリ侵害時に新規 digest をそのまま信任してしまう。
**改善提案**:
- `manifest.json.versions[]` に `signature` / `git_commit` を **optional フィールドとして予約**し、未指定時は warning + 明示確認 install。
- 設計ドキュメントに「信頼境界（embedded/registry/local）」の章を追加し、install 時に SKILL.md を表示・人間承認するフローを Phase 1 に組み込む。
- 非ゴールは「公式インデックスは署名 PR まで未公開・default URL を持たない」のセットで初めて成立する旨を明記。
**根拠**: REQUIREMENTS §11.1 #10 / pluggability-design-agent §9 / security-design-agent。

### 3. HTTP timeout / context propagation の規定不足
**問題点**: `Client.HTTPClient` が nil の場合のみ 30s timeout を当てているが、Web UI の `/api/skills/registry` プロキシは `r.Context()` を受け取るのみで per-request deadline を独自に設けていない。daemon が `marunage loop` から呼ぶ場合、ハングしたレジストリで goroutine が固まり自己復旧バックオフを汚染する。
**改善提案**: 設計 §2 に「Web/CLI どちらも `context.WithTimeout(parent, 10s)` を必ず重ねる」「同時実行上限・429/Retry-After ポリシー」を明記。
**根拠**: daemon-runtime-design-agent / http-api-design-agent。

### 4. 設計ドキュメントの構造的欠落
**問題点**: テスト戦略がチェックボックス形式の「テストリスト」になっておらず、Phase 分割・ロールバック条件・Mermaid 図・リスク章が一切無い。t_wada TDD の「テストリストを書く」ステップが文書化されていない。
**改善提案**: 「Phase 計画 (P0 protocol → P1 client → P2 extractor → P3 installer → P4 CLI → P5 Web UI)」「リスク・トレードオフ」「ロールバック / 撤退条件」「Mermaid (state file ↔ embedded ↔ registry の境界 / install シーケンス)」「テストリスト (- [ ])」の 5 章を追加。
**根拠**: review-guidelines §1, §9, §10。

### 5. state file に schema_version が欠落（クライアント側）
**問題点**: 設計に state file の `schema_version` が書かれておらず、将来フィールド追加時の migrate ルートが無い（実装側では `State.SchemaVersion` がある）。
**改善提案**: 設計に `{schema_version: 1, installed: [...]}` の JSON shape を明記。読み込み時に未知 version は read-only fallback、`migrateV1toV2` 形式の関数列を予約。
**根拠**: data-model-design-agent。

## 🟡 中程度の指摘（推奨対応）

### 6. `index.latest` と `manifest.versions[0]` の解決規則が未定義
manifest と index が乖離した場合の優先順位を明記する。実装は manifest 先頭、`FindUpdates` は index.latest を比較に使っており規則化が必要。

### 7. tar エントリ数上限がない
`MaxTarBytes` (64 MiB) は累積バイトのみ。`MaxFiles` (例 2048) と `MaxDepth` を別途設けないと「1 byte × 数百万ファイル」型の inode 枯渇を許す。gzip bomb / truncated tar のテストも欠落。

### 8. JSON エラーレスポンス形式・URL/バージョニング規約が未定義
`http.Error` で `text/plain` 返却。RFC 7807 problem+json か `{"error":{"code","message"}}` を採択し、`/api/v1/skills/...` あるいは Accept ヘッダで API バージョニング方針を明文化する。

### 9. Web UI のアウトバウンド allowlist / SSRF / 認証
`/api/skills/registry` は CSRF・認証・rate-limit が無く `?q=` で上流 GET をプロキシする。LAN 公開時 (`0.0.0.0`) はトークン必須・既定 127.0.0.1 バインドの旨を §5 に明記し、private-IP block を `ErrInsecureRegistry` に統合。

### 10. 並行実行制御 (flock) と graceful shutdown
Web UI と CLI の `~/.claude/skills/` 同時アクセスで lost-update が起こる可能性。`flock(~/.claude/skills/.lock)` と「install 中の SIGTERM で tmp dir GC」を §6 に追加。

### 11. SKILL.md `<!-- version: -->` とマニフェスト version の整合
両者が乖離する前提（手書き編集）に対する規則が §8 オープンクエスチョン送り。install 時に「両者一致しなければ拒否」または「manifest を SSOT とする」のいずれかを本 PR で決着する。

### 12. typosquat / homoglyph / SKILL.md 静的解析
レジストリ発行者規約として §1 に明記する。最低でも install 時に SKILL.md 全文を表示し人間承認を必須化する。`origin: external/registry` タグを SKILL.md frontmatter に必須化する規約も追加。

### 13. 監査ログ（JSONL）の emission
embedded installer は既に `config.Auditor` に `setup.skills.install/update/skip` を吐く。registry installer も `skills.registry.install/update` 等を JSONL に出すべき。`--allow-embedded-override` は audit log に必須。

### 14. 複数レジストリサポート / Git commit pin
team mirror / fork 時代に備え `MARUNAGE_SKILLS_REGISTRY_URLS` (複数) と name 衝突時の優先度規約、`tarball_url` + `git_commit` pin を v1 schema に予約する。

## 🟢 軽微な指摘 / 提案

- Cache-Control: no-store の付与
- `User-Agent: marunage-web/marunage-cli` の design への明記
- 10 拡張点のうち「Skill Registry」拡張点に該当することの明示
- TOCTOU 対策: `filepath.EvalSymlinks(filepath.Dir(dest))` による親方向 symlink 検査
- state file `sha256` の再検証（`marunage skills refresh`）コマンド検討
- tarball helper 重複の `internal/skills/registry/registrytest` への抽出
- `updated_at` のタイムゾーン規約（UTC ISO8601）と `source` の正規化ルール
- `list` 出力での「embedded / registry」区別表示
- tar entry の executable bit / setuid bit の扱い

## ✅ 良い点

- ハードコード default URL を持たず、`MARUNAGE_SKILLS_REGISTRY_URL` / `--registry` 必須の設計は OpenClaw §11.1 #10 とフォーカルポイント設計に整合
- sha256 検証 + scheme allowlist + size cap + path traversal/symlink 拒否 + tmp→rename atomic + 0600/0700 権限の多層防御が一貫
- PR-34 embedded skill との衝突を `ErrEmbeddedConflict` + `--allow-embedded-override` で明示分離
- Web UI を読み取り専用に閉じ、mutating endpoint を別 PR に切り出した分担
- 各テストの Doc コメントが「何を pin しているか」を述べており t_wada TDD のテストリストの代替として機能
- httptest.Server を使った忠実度の高い境界テスト（モック乱用回避）
- 型付き sentinel エラー（`ErrUnsupportedSchema` / `ErrManifestMalformed` / `ErrIntegrity` / `ErrInsecureRegistry` / `ErrUnsafeTarPath` / `ErrTarTooLarge` / `ErrEmbeddedConflict` / `ErrUpstream`）が明確

---

## レイヤー特化レビュー

### Go 実装
- 多層防御の実装は良好。`net/http` 標準ライブラリ依存で CGO_ENABLED=0 維持可能。
- 設計ドキュメントの Phase / テストリスト / Mermaid / リスク章が欠落（🔴）。

### Rust 実装
非該当（Go 実装のみ）。

### TypeScript / フロントエンド
非該当（html/template SSR、TS 不在）。

### Markdown / Vault 互換
- tarball が `<name>/SKILL.md` 単一ディレクトリ規約で人間が直接読み書きできる前提が保たれている。
- frontmatter（`name`/`version`/`origin`/`sha256`/`installed_at`）必須化と `<!-- version: -->` との関係決着が必要。

---

## 機能横断レビュー

### セキュリティ（OpenClaw 教訓照合）
- #10 (レジストリ): sha256 / size cap / scheme guard は手当済みだが **署名・http 拒否・typosquat・SKILL.md 静的解析が未対応**。「ClawHub マルウェア」再発リスク。
- #8 (時間差プロンプトインジェクション): SKILL.md に `origin: external/registry` タグの必須化が不足。
- #9 (サンドボックス): install 後の実行時隔離が非ゴールのまま、§3.10 既定と矛盾。

### プラガビリティ規約
- ハードコード default URL なしは ✅。
- 署名フック / Git commit pin / 複数 registry が未予約（🟡）。

### HTTP API 設計
- 読み取り専用化と 503/502 切り分けは良好。
- JSON エラー形式・バージョニング・per-request timeout・上流 allowlist の明文化が必要。

### 常駐運用 / Daemon Runtime
- HTTP timeout 必須化と graceful shutdown / flock の追加が必要。

### データモデル / SQLite
- state file の schema_version 明記、SQLite 移行非ゴールの明記、`marunage skills refresh` 検討。

### テスト戦略（t_wada TDD）
- テストリスト（- [ ]）形式への書き換えが必要。
- gzip bomb / truncated tar / state 破損 / 部分失敗 / `--registry` env 競合 / audit JSONL のテストを追加。

### 非ゴール / 要件整合性
- v0.3 機能であることの明示と「default URL を持たない＋公式インデックスは署名 PR まで未公開」を成立条件として残す。

---

## 📝 総合チェックリスト

### 全体
- [ ] ドキュメント構造の完全性（Phase / リスク / ロールバック / Mermaid / テストリスト）
- [x] REQUIREMENTS.md との整合性（default URL なし、HTTPS、sha256）
- [ ] セキュリティ既定条件（HTTPS only、署名、サンドボックス、SKILL.md 静的解析、allowlist）
- [ ] プラガビリティ規約（複数 registry、Git commit pin、署名フック）
- [ ] Markdown SSOT / Vault 互換（frontmatter 必須化、origin タグ）
- [ ] 常駐運用 / 静粛性（HTTP timeout、graceful shutdown、flock）
- [ ] 観測性 / 介入可能性（audit JSONL、register override の警告）
- [ ] 図表の適切性（Mermaid 不在）
- [ ] Phase / 実装計画（章の不在）
- [ ] テスト方針（チェックリスト形式へ）
- [x] ライセンス / OSS 衛生（OK）
- [x] 用語の一貫性（Skill Registry 拡張点としての位置づけ追加が望ましい）

## 🧭 次アクション

- [ ] HTTPS only 既定化 + `--insecure` opt-in
- [ ] tar entry 数上限 / 親 symlink 検査 / state schema_version 明文化
- [ ] manifest に `signature` / `git_commit` フィールドを optional 予約
- [ ] Web UI per-request timeout・private-IP block・allowlist 明記
- [ ] 設計ドキュメントに Phase / テストリスト / リスク / ロールバック / Mermaid を追加
- [ ] audit log の `skills.registry.install/update/override` action を追加

## 総評

レジストリ運用の **既定セキュリティ** は実装側でよく守られており、OpenClaw 反面教師の主要対策（sha256 / size cap / scheme guard / atomic / 0600）は揃っている。一方で **HTTPS only 化・署名フック予約・サンドボックス連携・設計ドキュメントの構造（Phase/リスク/テストリスト/Mermaid）** が不足しており、Markdown SSOT / モデル中立 / ローカル完結の各原則とは整合的だが、サードパーティ配布解禁の前提条件をもっと明示しないと "v0.3 機能" として出荷判断ができない。
