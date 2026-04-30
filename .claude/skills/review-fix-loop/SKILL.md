---
name: review-fix-loop
description: ローカル差分（または PR）をマルチエージェントでコードレビューし、Critical/Warning がなくなるまで t_wada TDD で自動修正・テスト・コミットをループする
triggers:
  - review fix loop
  - ローカルレビューループ
  - warning なくなるまで修正
  - レビュー修正ループ
argument-hint: "[PR 番号 or ベースコミット（省略時はカレントブランチの追跡 PR / なければ main からの差分）]"
---

# review-fix-loop スキル（marunage 版）

## 概要

ローカルの **未プッシュコミット込み** の差分をマルチエージェントでレビューし、Critical / Warning 指摘がなくなるまで以下のサイクルを自動で回します。

```
レビュー → 修正（t_wada TDD）→ テスト → コミット → 再レビュー
```

プッシュ不要なので、「push → PR 確認 → 修正」の往復が消えます。


## 前提

- 実行ディレクトリは marunage リポジトリ直下
- `gh` CLI でログイン済み（`gh auth status` で確認）
- Conventional Commits を採用（OSS 方針 §13）
- **必ずテストを実行してからコミット**する（CLAUDE.md の絶対ルール）
- t_wada TDD：失敗するテストを先に書き、Red → Green → Refactor の順で進める

## 実行手順

### Step 0: 初期設定

```bash
PR_NUMBER="${1:-$(gh pr view --json number -q .number 2>/dev/null || echo "")}"
MAX_ITERATIONS="${MAX_ITERATIONS:-5}"
ITERATION=0

REVIEW_DIR="$(git rev-parse --show-toplevel)/.marunage-review"
mkdir -p "$REVIEW_DIR"

REVIEW_FILE="$REVIEW_DIR/review-$(date +%s).md"
SUMMARY_FILE="$REVIEW_DIR/fix-summary.md"
: > "$SUMMARY_FILE"
```

> `.marunage-review/` は `.gitignore` に追加することを推奨。
> 既に追加済みでなければループ開始時に自動で追記する。

### Step 1: ベースコミットの確定

優先順位：
1. 引数でベースコミット明示（hash / branch）
2. PR があれば「最後のレビューループ完了コミット」（`fix: review-fix-loop iteration <N>` の最新タグ付きコミット）
3. PR があれば PR ベースブランチとの merge-base
4. なければ `origin/main` との merge-base

```bash
if git rev-parse --verify "$1" >/dev/null 2>&1; then
  BASE_COMMIT="$1"
elif [ -n "$PR_NUMBER" ]; then
  BASE_BRANCH=$(gh pr view "$PR_NUMBER" --json baseRefName -q .baseRefName)
  BASE_COMMIT=$(git merge-base HEAD "origin/$BASE_BRANCH")
else
  BASE_COMMIT=$(git merge-base HEAD origin/main 2>/dev/null || git rev-parse HEAD~1)
fi

echo "BASE_COMMIT=$BASE_COMMIT"
```

### Step 2: テストランナーの自動検出

marunage は言語未確定（Go / Rust / Bun 候補）。リポジトリ直下から以下の順で **最初に見つかったもの** を使う。
複数該当する場合は変更ファイルの拡張子から推定し、迷う場合は `Makefile` の `test` ターゲットを最優先する。

| 優先 | ファイル                        | テストコマンド            |
| ---- | ------------------------------- | ------------------------- |
| 1    | `Makefile` の `test` 定義       | `make test`               |
| 2    | `go.mod`                        | `go test ./...`           |
| 3    | `Cargo.toml`                    | `cargo test --all`        |
| 4    | `bun.lockb` / `bunfig.toml`     | `bun test`                |
| 5    | `package.json` の `test` script | `npm test` / `pnpm test`  |
| 6    | `pyproject.toml` / `pytest.ini` | `pytest`                  |
| 7    | （何もない場合）                | テスト実行スキップ + 警告 |

```bash
detect_test_cmd() {
  if [ -f Makefile ] && grep -qE '^test:' Makefile; then echo "make test"; return; fi
  if [ -f go.mod ];           then echo "go test ./..."; return; fi
  if [ -f Cargo.toml ];       then echo "cargo test --all"; return; fi
  if [ -f bun.lockb ] || [ -f bunfig.toml ]; then echo "bun test"; return; fi
  if [ -f package.json ] && jq -e '.scripts.test' package.json >/dev/null 2>&1; then
    if [ -f pnpm-lock.yaml ]; then echo "pnpm test"
    elif [ -f yarn.lock ];     then echo "yarn test"
    else                            echo "npm test"; fi
    return
  fi
  if [ -f pyproject.toml ] || [ -f pytest.ini ]; then echo "pytest"; return; fi
  echo ""
}
TEST_CMD="$(detect_test_cmd)"
```

### Step 3: ループ本体

`ITERATION` が `MAX_ITERATIONS` に達するか、Critical / Warning 指摘がゼロになるまで繰り返す。

```bash
ITERATION=$((ITERATION+1))
ITER_START_COMMIT=$(git rev-parse HEAD)
ITER_REVIEW_FILE="$REVIEW_DIR/review-iter-$ITERATION.md"
```

#### Step 3a: マルチエージェント並列レビュー

`git diff $BASE_COMMIT HEAD` の出力を **Task tool で 4 エージェントに並列レビューさせる**。

エージェントは以下の 4 観点で起動。各エージェントへの共通指示テンプレート：

```
以下の git diff をコードレビューしてください。

## 対象差分
{git diff $BASE_COMMIT HEAD の出力（長すぎる場合はファイル単位で分割可）}

## 参照すべき設計判断基準
- リポジトリ直下の REQUIREMENTS.md（marunage の要件・思想）
- リポジトリ直下の CLAUDE.md（t_wada TDD・cmux ブラウザ操作）
- .claude/skills/design-review/review-guidelines.md（共通観点）

## あなたのレビュー観点
{下記 4 観点のうち 1 つ}

## 出力フォーマット
以下を厳格に守ってください（review-fix-loop の判定で使われます）。

```markdown
## あなたのエージェント名

## Critical（必須修正）
- {ファイル:行}: {問題と修正方針を 1-2 行}
（なければ「なし」とだけ書く）

## Warning（推奨修正）
- {ファイル:行}: {問題と修正方針を 1-2 行}
（なければ「なし」とだけ書く）

## Info（参考情報）
- {1-2 行}（任意）
```
```

4 エージェント観点：

1. **Security & Secrets**
   - 平文クレデンシャル、`0.0.0.0` バインド、トークン URL 露出、サンドボックス未使用、`--dangerously-skip-permissions` の常用化、外部発信の人間承認回避、ログのトークン混入
   - OpenClaw 反面教師（REQUIREMENTS.md §11.1）と照合

2. **Design Conformance**
   - REQUIREMENTS.md の §3.x 思想と §6 拡張点規約への適合
   - 双方向対話 Channel の禁止違反検知（§3.7 / §5.12）
   - Markdown SSOT 違反（DB を SSOT として扱う等、§3.8）
   - 用語の一貫性（Workspace=Vault、Discovery、atomic sentinel 等）

3. **Test Quality (t_wada TDD)**
   - 変更に対応するテストが書かれているか
   - **テストが先に書かれた形跡**（テストリスト / 失敗テスト）があるか
   - モックの乱用で本物のバグを隠していないか
   - 並行性 / 非同期テストが flaky になっていないか
   - スリープ依存テストになっていないか

4. **Code Quality & Simplicity**
   - 過剰抽象 / 過剰最適化 / デッドコード / コメントアウトされたコード
   - エラーハンドリングの一貫性
   - 命名の一貫性
   - public / private 境界の妥当性
   - 不要な依存追加

各エージェントの出力を集約し、`$ITER_REVIEW_FILE` に以下の形式で書き出す：

```markdown
# review-fix-loop イテレーション {N} レビュー結果

base: {BASE_COMMIT}
head: {現在の HEAD}
date: {ISO 8601}

## Critical
- {Security} {file:line}: {内容}
- {Design Conformance} {file:line}: {内容}
- ...

## Warning
- {Test Quality} {file:line}: {内容}
- ...

## Info
- ...

---

## エージェント別レビュー全文
{4 エージェントの raw 出力をそのまま添付}
```

#### Step 3b: 判定

```bash
HAS_CRITICAL=$(awk '/^## Critical$/{f=1;next}/^## /{f=0}f' "$ITER_REVIEW_FILE" \
  | grep -v "^$" | grep -v "^なし" | head -1)
HAS_WARNING=$(awk '/^## Warning$/{f=1;next}/^## /{f=0}f' "$ITER_REVIEW_FILE" \
  | grep -v "^$" | grep -v "^なし" | head -1)

if [ -z "$HAS_CRITICAL" ] && [ -z "$HAS_WARNING" ]; then
  echo "✅ Warning 以上の指摘なし。ループ終了。"
  # Step 4 へ
fi
```

#### Step 3c: t_wada TDD で修正

修正は別の Task agent（subagent_type=`general-purpose`）に依頼する。
**1 イテレーションでは「指摘の中で最も独立性が高いもの」をひとつ選び**、t_wada TDD のサイクルを回す：

```
## 修正指示テンプレート

以下の指摘リストから、まず「ひとつだけ」選んで t_wada TDD で修正してください。
複数指摘を同時に修正しないこと（テストリスト思考）。

## 選択基準（優先順位）
1. Critical のうち、他の指摘と依存関係が薄いもの
2. Warning のうち、修正範囲が小さいもの
3. テストが書きやすいもの

## TDD 手順（必須）
1. 選んだ指摘に対する **失敗するテスト** を先に書く（Red）
2. テストを実行して **Red を確認**（テストランナー: {TEST_CMD}）
3. プロダクトコードを変更してテストを通す（Green）
4. Refactor。既存テスト全部通ることを確認

## 全体制約
- 修正は最小限。過剰なリファクタリング禁止
- 既存テストを壊さないこと
- 新たな問題を引き込まないこと
- セキュリティ既定（平文クレデンシャル禁止 / 0.0.0.0 バインド禁止 等）を絶対に下げない
- 双方向対話 Channel を新設しない（REQUIREMENTS.md §3.7）
- Markdown SSOT 原則を破らない（§3.8）

## 指摘リスト
{$ITER_REVIEW_FILE の Critical + Warning セクション}

## このイテレーションで対応する 1 件
{選んだ 1 件をエージェント自身が宣言してから着手}
```

修正後、エージェントから返ってきた「対応した 1 件」をサマリーに追記：

```bash
cat >> "$SUMMARY_FILE" << EOF

### イテレーション $ITERATION で対応した指摘

[エージェントが宣言した「選んだ 1 件」と修正概要]
EOF
```

> **判断**: 指摘が極めて独立で互いに干渉しない場合に限り、複数同時修正を許可する（高々 3 件まで）。
> 干渉の有無は修正エージェントが自己判断する。

#### Step 3d: テスト実行

```bash
if [ -n "$TEST_CMD" ]; then
  echo "▶ Running: $TEST_CMD"
  if ! eval "$TEST_CMD"; then
    echo "❌ テスト失敗。修正をエージェントに差し戻し（同じイテレーション内で再修正）。"
    # 失敗ログを修正エージェントに渡して再依頼
    exit_iter=1
  fi
else
  echo "⚠️ テストランナー未検出。手動で確認してください。"
fi
```

> 言語未確定の段階では `Makefile` を整備しておくと、`make test` で全体を回せる。
> 既存テストが何もない場合（v0.1 立ち上げ初期）は、修正と同時にテストファイルが新規追加されることを期待する。

#### Step 3e: コミット

テストが通ったら **このイテレーションの差分のみ** をコミットする。

```bash
git add -A  # 修正エージェントが触ったファイルのみ。誤って .marunage-review/ を入れない
git reset -- .marunage-review/  # ループ生成物は除外

# Conventional Commits + イテレーション情報
git commit -m "$(cat <<EOF
fix: review-fix-loop iteration $ITERATION

$(awk '/^## Critical$/{f=1;next}/^## /{f=0}f' "$ITER_REVIEW_FILE" | grep -v "^なし" | grep -v "^$" | head -3 | sed 's/^/- /')

Co-Authored-By: review-fix-loop <noreply@marunage>
EOF
)"
```

> コミット署名やフックを **`--no-verify` で skip しない**。フックが落ちたら原因を直してから再コミットする。
> もし pre-commit hook が落ちて修正しても通らない場合は、ループを停止しユーザーに報告する。

### Step 4: 完了処理

ループ終了後、サマリーを生成して返す。

#### 4a: ローカルサマリー（必ず出力）

`$REVIEW_DIR/final-report.md` を生成し、ユーザーに表示する：

```markdown
# review-fix-loop 完了レポート

ベースコミット: {BASE_COMMIT}
最終 HEAD: {現在の HEAD}
イテレーション数: {N}
終了理由: {全指摘解消 / MAX_ITERATIONS 到達 / pre-commit hook 失敗}

## 対応した指摘と修正

{$SUMMARY_FILE の内容}

## 最終レビュー状態（{N} 回目）

### Critical
{最終イテレーションの Critical}

### Warning
{最終イテレーションの Warning}

## 次アクション
- [ ] git diff の最終確認: `git log $BASE_COMMIT..HEAD --oneline`
- [ ] 必要なら追加修正
- [ ] `git push` でリモートへ反映
```

#### 4b: PR がある場合のオプション処理

ユーザーが PR 番号を指定 / カレントブランチに PR がある場合、希望に応じて PR にも投稿する。
**既定では投稿しない**（プッシュ前提のため、ローカル完結を尊重）。

ユーザーが「PR にもコメントしてほしい」と言った場合のみ：

```bash
gh pr comment "$PR_NUMBER" --body-file "$REVIEW_DIR/final-report.md"
```

### Step 5: 結果報告

ユーザーに以下のテンプレートで報告：

```
✅ review-fix-loop 完了

イテレーション数: {N}
終了理由: {...}
対応した指摘: {合計件数}
レポート: .marunage-review/final-report.md

次アクション:
- 最終 diff を確認: git log {BASE_COMMIT}..HEAD --oneline
- 問題なければ git push でプッシュしてください
- PR にレポートを投稿したい場合はその旨指示してください
```

## 利用例

```
# カレントブランチの追跡 PR / なければ main からの差分でループ
/review-fix-loop

# PR 番号指定
/review-fix-loop 42

# ベースコミット指定（PR なしで feature ブランチ単独レビュー）
/review-fix-loop abc1234

# 上限イテレーション数を環境変数で変更
MAX_ITERATIONS=8 /review-fix-loop
```

## 注意事項

- **プッシュはしない**。コミットまで実行し、ユーザーが内容に納得したら手動で `git push`
- **テスト必須**。テストが落ちた状態ではコミットしない
- **スコープ制限**。修正はレビュー指摘箇所のみ。無関係なリファクタリングは禁止
- **t_wada TDD 必須**。失敗テスト → 実装 → リファクタの順を守る。テストを後付けにしない
- **`--no-verify` 禁止**。フックが落ちたら原因を直す
- **`.marunage-review/` をコミットしない**。`.gitignore` に必ず入れる
- **OpenClaw 反面教師（§11.1）の項目を緩める修正は不可**

## 依存

- `gh` CLI（PR 連携時）
- `jq`（package.json 解析時）
- リポジトリ内に何らかのテストランナー（言語未確定 v0.1 では `Makefile` 推奨）

## design-review との関係

- **design-review**: 設計ドキュメント（Markdown）を 11 エージェントで並列レビュー → 同階層に `<file>.review.md` を生成
- **review-fix-loop**: コード差分を 4 エージェントで並列レビュー → 修正・テスト・コミットをループ

両者は**スコープが明確に分かれている**。設計を変える場合は design-review、コードを直す場合は review-fix-loop を使う。
