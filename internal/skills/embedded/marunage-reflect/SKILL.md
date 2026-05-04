---
name: marunage-reflect
description: 完了タスクに対する第三者レビュー観点の自動レビュープロンプト（オプトイン）
---
<!-- version: 0.3.0 -->
# marunage-reflect

完了タスクの自動レビュー用プロンプト。`tasks.status` が `done` に遷移した
直後、同じ cmux ワークスペースに `cmux send` で投入され、Claude の回答は
`tasks.reflection` カラム（NULL 許容 TEXT）に保存される。

このフックは `[reflection].enabled` で on/off できる。デフォルトは off で、
有効化した場合のみこの SKILL が呼ばれる。`[reflection].sample_rate` で
1.0 未満にダイヤルすると母集団の一部にだけ走らせるサンプリングが効く
（デフォルト 1.0 = `enabled` の時は全完了に走る）。

## 入力

- 直前の `done` タスクのコンテキスト（同 cmux ワークスペース上で原タスクを
  実行した Claude セッション）。
- 自分でファイルを開く必要はない — そのまま「いま完了したタスク」を対象に
  レビューする。

## 出力フォーマット

3 つ以下の指摘を、以下のフォーマットで列挙してください（言語は日本語可）:

```
1. <指摘の見出し>
   根拠: <該当箇所 / 観察事実>
   重大度: critical | warning | info
   提案: <修正方針 1 行。critical なら同セッションで修正 PR を出すか
         marunage に追加タスクを登録する>
```

観点の例:

- 仕様の取り違えや欠落（要件文と実装の差分）
- 副作用の漏れ（ログ・通知・周辺ドキュメントの未更新）
- 再現性の欠如（テスト未追加、手元でしか動かない設定）
- セキュリティ既定（O_NOFOLLOW / サイズ上限 / シークレット混入）の見落とし

`critical` の指摘を見つけた場合、外部発信を伴う修正（PR 作成 / 通知など）
は人間承認モードに従ってください。Skill が独断で外部 push を行わないこと。

## 制約 / 出力先（Reflection sentinel）

具体的な保存先パス・ヒアドキュメント例・タイムアウト・最大バイト数は、
ディスパッチャが Send 時にプロンプト末尾へ自動付与する
**「## Reflection sentinel (auto-injected)」** セクションを正とすること。
SKILL.md は意図的にここで重複を避け、矛盾の起点を作らないようにする。

## 観測性

このフックは `audit.log` に以下のイベントを残す:

- `reflection.start` — Send 直前
- `reflection.done` — `tasks.reflection` 保存成功
- `reflection.fail` — Send 失敗 / SetReflection 失敗
- `reflection.timeout` — sentinel 未達
- `reflection.cancel` — 親 ctx キャンセル

`marunage` 側のテスト一覧は `.test-list.md` の "PR-102 reflection hook"
セクション参照。
