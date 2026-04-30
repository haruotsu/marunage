---
name: design-review
description: marunage の設計ドキュメント（REQUIREMENTS.md / Design Doc / Phase 計画書）を 11 エージェント並列でレビューし、レイヤー特化と機能横断の観点から統合レビューを生成する
triggers:
  - design review
  - 設計レビュー
  - design doc レビュー
  - REQUIREMENTS レビュー
  - REQUIREMENTS.md レビュー
  - 設計ドキュメントレビュー
argument-hint: "[ファイルパス（省略時は REQUIREMENTS.md）]"
---

# design-review スキル（marunage 版）

## 概要

marunage の設計ドキュメントを以下の **全 11 エージェント** で並列レビューし、統合された `<元ファイル名>.review.md` を同階層に出力する。

- レイヤー特化（4本）: go / rust / typescript-frontend / markdown-spec
- 機能横断（7本）: security / pluggability / http-api / daemon-runtime / data-model / test-strategy / non-goal-consistency

## 対象ドキュメント

引数で渡されたファイル。引数が空の場合は **リポジトリ直下の `REQUIREMENTS.md`**。

## 実行手順

### 手順 0: 前提整備

- 対象ドキュメントが存在することを確認する
- 同じディレクトリの関連ドキュメント（mermaid 図、Phase 別計画書、ADR など）があればファイル名を控える
- 既存の `<元ファイル名>.review.md` が同階層にあれば、それも控える（履歴として参照）

### 手順 1: 対象ドキュメントの確認

対象ドキュメントの **冒頭 50 行のみ** を Read し、タイトル・対象範囲・version 表記を把握する（全文は読まない）。
全文を読むのは各サブエージェントの責任。メインは「俯瞰と統合」に集中する。

### 手順 2: 全 11 エージェント並列起動

以下の **全 11 エージェント** を Task tool（subagent_type は `general-purpose`）で **並列起動** する。
該当 / 非該当の判断はメインで行わず、各エージェントが自律的に判断する（`非該当: {理由}` を返す権利がある）。

> 例外: `non-goal-consistency-agent` は **常に該当扱い**（要件全体俯瞰が責務）。

各エージェントへの指示テンプレート:

```
以下の設計ドキュメントをレビューしてください: {ドキュメントパス}

まず .claude/skills/design-review/{NAME}-{KIND}-agent.md を Read し、
その指示に従ってレビューを実行してください。

リポジトリ直下の REQUIREMENTS.md と CLAUDE.md は前提知識として
必要に応じて参照して構いません。
```

#### レイヤー特化エージェント（4 本）

- `.claude/skills/design-review/go-design-agent.md`
- `.claude/skills/design-review/rust-design-agent.md`
- `.claude/skills/design-review/typescript-frontend-design-agent.md`
- `.claude/skills/design-review/markdown-spec-agent.md`

#### 機能横断エージェント（7 本）

- `.claude/skills/design-review/security-design-agent.md`
- `.claude/skills/design-review/pluggability-design-agent.md`
- `.claude/skills/design-review/http-api-design-agent.md`
- `.claude/skills/design-review/daemon-runtime-design-agent.md`
- `.claude/skills/design-review/data-model-design-agent.md`
- `.claude/skills/design-review/test-strategy-design-agent.md`
- `.claude/skills/design-review/non-goal-consistency-agent.md`

### 手順 3: 統合レビュー生成

以下の形式で書く：

```markdown
# {対象ファイル名} 設計レビュー結果

## 📋 対象ドキュメント
- パス: ...
- タイトル: ...
- 対象 version: v0.x（不明なら「不明」）
- 影響レイヤー: go / rust / typescript-frontend / markdown （該当のみ）
- 関連領域: security / pluggability / http-api / daemon-runtime / data-model / test-strategy （該当のみ）

## 🔴 重大な指摘（必須対応）

### 1. {カテゴリ}
**問題点**: ...
**該当箇所**: > 引用
**改善提案**: ...
**根拠**: REQUIREMENTS.md §x.x / OpenClaw §11.1 表の {行}

## 🟡 中程度の指摘（推奨対応）
...

## 🟢 軽微な指摘 / 提案
...

## ✅ 良い点
...

---

## レイヤー特化レビュー
{該当レイヤーのみ展開}

### Go 実装
{go エージェントのサマリー}

### Rust 実装
{rust エージェントのサマリー}

### TypeScript / フロントエンド
{typescript-frontend エージェントのサマリー}

### Markdown / Vault 互換
{markdown-spec エージェントのサマリー}

---

## 機能横断レビュー
{該当領域のみ展開}

### セキュリティ（OpenClaw 教訓照合）
### プラガビリティ規約
### HTTP API 設計
### 常駐運用 / Daemon Runtime
### データモデル / SQLite
### テスト戦略（t_wada TDD）
### 非ゴール / 要件整合性

---

## 📝 総合チェックリスト

### 全体
- [ ] ドキュメント構造の完全性（review-guidelines §1）
- [ ] REQUIREMENTS.md との整合性（§2）
- [ ] セキュリティ既定条件（§3）
- [ ] プラガビリティ規約（§4）
- [ ] Markdown SSOT / Vault 互換（§5）
- [ ] 常駐運用 / 静粛性（§6）
- [ ] 観測性 / 介入可能性（§7）
- [ ] 図表の適切性（§8）
- [ ] Phase / 実装計画（§9）
- [ ] テスト方針（§10）
- [ ] ライセンス / OSS 衛生（§11）
- [ ] 用語の一貫性（§12）

### レイヤー
{該当レイヤーのみ}

### 機能横断
{該当領域のみ}

## 🧭 次アクション
- [ ] 必須対応（🔴）の修正担当・期限
- [ ] 推奨対応（🟡）のトリアージ
- [ ] Open Question への追記候補

## 総評
{全体評価。要件の核となる思想（Markdown SSOT / 双方向 Channel 非ゴール / OpenClaw 反面教師 / モデル中立 / ローカル完結）の遵守状況を 5 行以内で}
```

## 利用例

```
# REQUIREMENTS.md をレビュー
/design-review

# 個別の Design Doc をレビュー
/design-review docs/design/heartbeat.md

# 自然言語で起動（triggers 経由）
「REQUIREMENTS.md をレビューして」
「設計レビューお願い」
```

## 注意事項

- **必ず日本語**でフィードバックを提供する
- 具体的な箇所を**引用**して指摘する
- 単なる指摘だけでなく**改善提案**と**根拠（REQUIREMENTS.md §x.x など）**を含める
- セキュリティ（特に OpenClaw 反面教師 §11.1）は最重視する
- 既存処理 / 既存設計への影響は必ず分析する
- **全 11 エージェントを常に起動する**（該当 / 非該当はエージェント側が判断）
- `non-goal-consistency-agent` は必ず実質的レビューを返すこと（要件全体俯瞰が責務）
- 統合時、複数エージェントが同じ問題を指摘していたら **重複排除**して 1 つにまとめる
- 改善提案が要件と矛盾しそうな場合は、**REQUIREMENTS.md 側の改訂提案** として併記する

## review-fix-loop との関係

- **design-review（このスキル）**: 設計ドキュメント（Markdown）を 11 エージェントで並列レビュー → 同階層に `<file>.review.md` を生成
- **review-fix-loop**: コード差分を 4 エージェントで並列レビュー → 修正・テスト・コミットをループ

設計を変える場合は design-review、コードを直す場合は review-fix-loop を使う。
