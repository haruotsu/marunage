---
name: marunage-triage
description: Discoveryで拾った生メッセージから、marunageが回すべきタスクをOrient判定で選別する司令系統
---
<!-- version: 0.1.0 -->
# marunage-triage

Discovery で拾った生のメッセージから「marunage が回すべきタスク」を選別する Orient
フェーズの司令系統。`marunage discover` から呼び出され、各メッセージに対して
`task` か `skip` か、そしてその理由を返す。

このファイルはユーザがカスタマイズすることを前提にしている。OSS が同梱する
バージョンは初期値であり、自分の役割や所属組織に合わせて編集してよい。
編集後 `marunage setup --skills --check-updates` で OSS 同梱版との差分を
確認できる。

## 入力

各メッセージは次のフィールドを持つ:

- `source` — `slack` / `gmail` / `github` / `markdown` などの discovery 種別
- `external_id` — ソース側の識別子（Slack の `ts`、GitHub の issue 番号 など）
- `external_url` — ヒトが開けるパーマリンク
- `title` — 短い要約（先頭 80 文字）
- `body` — 本文
- `mentions` — メッセージ内で名指しされたハンドルの配列
- `me` — 自分自身を表すハンドル（複数可）

## 判定ロジック

優先順に評価する。最初にマッチしたルールで結論を出す。

1. 自分が `mentions` に含まれている → `task`
   - 直接的な依頼または巻き込み。第三者の通報メンションも含む
2. 特定の他者宛で自分が `mentions` に含まれない → `skip`
   - 他人宛の依頼に割って入らない
3. `mentions` が空、もしくは全員宛だが、内容が自分の役割として対応すべき → `task`
   - 例: 自分がオーナーのリポジトリの CI 失敗、自分が担当するチャンネルの質問
4. 情報共有 / FYI のみ → `skip`
   - 配信ニュースレター、リリースノート転送、議事録共有など
5. 既に対応済み（自分のリプライがスレッドにある、Issue を自分が close 済み 等）→ `skip`
6. ソース別の追加ルール
   - Slack: 特定のリアクション (`:done:`, `:noted:` 等) が押されていれば強制 `skip`
   - GitHub: `wontfix` / `invalid` ラベルが付いていれば `skip`
   - Gmail: 自分が送信者なら `skip`

判定理由 (`reason`) は人間が後から監査できるように、どのルールでどの根拠で
結論したかを 1 文で記述する。Phase 1 の Markdown ソースは triage を経由せず
すべて `task` 扱いで `reason="phase1: markdown source bypass"` を付ける。

## 出力フォーマット

JSON Lines で 1 メッセージ 1 行を stdout に書く。スキーマ:

```json
{"external_id": "<id>", "decision": "task" | "skip", "reason": "<1文>", "priority": 0}
```

- `decision` — `task` または `skip` のいずれか。それ以外の値は dispatcher が拒絶する
- `reason` — 上記「判定ロジック」で適用したルール番号と根拠
  例: `"rule 1: @me が直接メンションされている"`
- `priority` — `task` のときのみ意味を持つ整数。0 が通常、正で高優先・負で低優先

`task` 判定の出力例:

```json
{"external_id": "T123.456", "decision": "task", "reason": "rule 1: @me が直接メンションされている", "priority": 1}
```

`skip` 判定の出力例:

```json
{"external_id": "T123.789", "decision": "skip", "reason": "rule 4: リリースノート転送のみで応答不要", "priority": 0}
```

判定不能（情報不足）の場合も `decision` は二択のいずれかに倒し、`reason` に
不確実性の根拠を残す。曖昧な行を出力しない。
