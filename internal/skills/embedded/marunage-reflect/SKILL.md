---
name: marunage-reflect
description: 完了タスクに対する第三者レビュー観点の自動レビュープロンプト（オプトイン）
---
<!-- version: 0.2.0 -->
# marunage-reflect

完了タスクの自動レビュー用プロンプト。`tasks.status` が `done` に遷移した
直後、同じワークスペースに投入され、回答は `tasks.reflection` カラムに保存
される。

このフックは `[reflection].enabled` で on/off できる。デフォルトは off で、
有効化した場合のみこの SKILL が呼ばれる。`[reflection].sample_rate` で
1.0 未満にダイヤルすると、母集団の一部にだけ走らせるサンプリングが効く
（デフォルト 1.0 = `enabled` の時は全完了に走る）。

## 基本プロンプト

いま完了したタスクについて、第三者がレビューしたら指摘されそうな点を 3 つ
挙げてください。観点の例:

- 仕様の取り違えや欠落（要件文と実装の差分）
- 副作用の漏れ（ログ・通知・周辺ドキュメントの未更新）
- 再現性の欠如（テスト未追加、手元でしか動かない設定）

各指摘は次のフォーマットで返してください:

```
1. <指摘の見出し>
   根拠: <該当箇所 / 観察事実>
   重大度: critical | warning | info
   提案: <修正方針 1 行。すぐ直せるなら自分で直して PR を出す>
```

重大度が `critical` のものは可能なら同じセッション内で修正 PR を出すか、
新しい marunage タスクとして登録してください。

## 出力先（Reflection sentinel）

回答が完成したら、ワークスペース直下に `.reflection` を atomic に書き出す
こと。フックは `<workspace_dir>/.reflection` を監視しており、ファイルが
出現した瞬間にトリミングして `tasks.reflection` に保存する:

```sh
printf '%s\n' "<your full reflection>" > <workspace_dir>/.reflection.tmp
mv <workspace_dir>/.reflection.tmp <workspace_dir>/.reflection
```

`.reflection` を直接書くと、リーダ側が半分書きの状態を読んでしまう恐れが
あるので、必ず `.tmp` 経由で `mv` すること（同 FS 内の rename は POSIX で
atomic）。具体的なパスは Send されたプロンプトの「Reflection sentinel」
セクションに含まれているのでそれを使う。
