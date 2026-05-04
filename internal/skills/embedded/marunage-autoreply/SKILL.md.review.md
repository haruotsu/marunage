# marunage-autoreply SKILL.md 設計レビュー結果

## 📋 対象ドキュメント
- パス: `internal/skills/embedded/marunage-autoreply/SKILL.md`
- タイトル: marunage-autoreply
- 対象 version: 1.0.0
- 影響レイヤー: go
- 関連領域: security / pluggability / test-strategy

---

## 🔴 重大な指摘（必須対応）

### 1. NG カテゴリがユーザー設定(TOML)で上書き可能
**問題点**: `config.go` の `toml.Unmarshal(data, &c)` は配列フィールドを完全置換するため、ユーザーが `autoreply.toml` に `deny = []` と書けばデフォルト Deny リスト全体が空になる。`SKILL.md` は「NEVER auto-reply, regardless of configuration」と宣言しているが実装がその約束を守れない。  
**該当箇所**: > `config.go:67` `toml.Unmarshal(data, &c)` / `boundary.go` `IsAllowed()` が `cfg.Permissions.Deny` のみ参照  
**改善提案**: `boundary.go` に `hardcodedDenyCategories` 定数スライスを追加し、`IsAllowed()` で設定に依存せず常にチェックする。テストとして「deny=[] の設定でも personal_information は拒否される」ケースを追加。  
**根拠**: security-design-agent / rust-design-agent / go-design-agent 一致。OpenClaw §11.1「外部発信操作の人間承認必須・設定で無効化不可」相当

### 2. audit.log のパスが SKILL.md と実装で不一致
**問題点**: SKILL.md には `~/.marunage/audit.log` と記載されているが、実装（`internal/logging/audit.go`）は `~/.marunage/logs/audit.log` を使用する。  
**該当箇所**: > SKILL.md 43行目 `Log the escalation reason to ~/.marunage/audit.log`  
**改善提案**: SKILL.md のパスを `~/.marunage/logs/audit.log (JSONL)` に修正する。  
**根拠**: pluggability-design-agent / non-goal-consistency-agent

### 3. エスカレーション時の audit.log 書き込みが未実装
**問題点**: SKILL.md は「NG 検出時に audit.log へ記録する」と明記しているが、`boundary.go` / `config.go` に書き込みロジックが存在しない。NG カテゴリ検出という最重要セキュリティイベントが監査証跡に残らない。  
**該当箇所**: > SKILL.md 43行目 / `boundary.go` `IsAllowed()` に Auditor 注入口がない  
**改善提案**: `Boundary` に `config.Auditor` を注入し、`IsAllowed=false` のとき `{action:"autoreply.escalation", category:...}` を JSONL で記録する。または呼び出し側 Executor が担保する旨を SKILL.md に明記する。  
**根拠**: security-design-agent / go-design-agent / test-strategy-design-agent 一致

---

## 🟡 中程度の指摘（推奨対応）

### 4. version 1.0.0 が他スキルと不整合
他スキル（triage/execute: 0.1.0, reflect: 0.3.0）はすべて `0.x.y` だが autoreply のみ `1.0.0`。  
**改善提案**: `0.1.0` に統一する。

### 5. `--draft-only` CLI フラグの実装が未確認
SKILL.md に記述されているが `internal/cli/autoreply.go` が存在しない。  
**改善提案**: この PR のスコープ外なら SKILL.md に「CLI は Phase 2」と明記する。

### 6. ドラフト保存ロジックが未実装
`IsDraftOnly()` はあるが、実際に `~/.marunage/autoreply-drafts/<task-id>.md` を書くコードがない。  
**改善提案**: `DraftWriter` インターフェースを定義するか、SKILL.md に「Draft 保存は Executor 層の責任」と記載する。

### 7. draft_mode のデフォルトが OFF（即送信）で外部発信の人間承認要件に要注意
`draft_mode.enabled = false` がデフォルトで人間承認なし即送信となる。SKILL.md に「このスキルは OK カテゴリに限定した自動返信のため §9.1 人間承認要件のスコープ外」と明示する。

---

## 🟢 軽微な指摘 / 提案

- audit.log 書き込みフォーマット（JSONL）が SKILL.md に明示されていない
- 入力スキーマ（`source`, `external_id`, `category` 等）が未定義
- `<source>/<id>` の source 値域が未定義で後段パーサーが壊れる可能性
- NG カテゴリ検出テストが SKILL.md の形式に対するテストリストを含まない

---

## ✅ 良い点

- Deny-wins の安全設計が `boundary.go` で一貫している
- `TestIsAllowed_DenyTakesPrecedenceOverAllow` で境界ケースを明示的にピン留め
- `config.toml` と分離した独立監査可能な設計（コードコメントで明示）
- `RequiredAutoReplySections` による install 時検証でセクション欠落を防止
- ファイル不在時のデフォルト適用（フェイルセーフ）が実装されている

---

## 次アクション
- [ ] 🔴 NG カテゴリのハードコード保護を boundary.go に追加（テスト先行）
- [ ] 🔴 SKILL.md の audit.log パスを修正（`logs/audit.log` + JSONL 明示）
- [ ] 🟡 version を 0.1.0 に変更
- [ ] 🟡 Auditor 注入の設計方針を決定（本 PR スコープ内 or Phase 2 明記）
