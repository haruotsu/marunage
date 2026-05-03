# PR-44 reaper — 設計メモ

> 本書は PR-44 (orphan / 24h-stuck reaper) の **設計判断の記録**。実装は `feat/pr-44-reaper` ブランチ。`docs/pr_split_plan.md` の "PR-44 reaper" セクションと `docs/requirement.md` 不変条件 #1 / #5 が正本。

## 目的

`status='running'` のタスクが、cmux ワークスペース消滅やマシン再起動で取り残される (orphan) ケースを 1 周のスイープで回収する。長期運用 (数日〜数週間) で「勝手に running が累積する」事象を防ぎ、不変条件 #5 "Crash safety" を閉じる。

## スコープ (本 PR)

1. **cmux 側に存在しない `ws` 参照を `failed` に遷移**
   - DB 上 `status=running` で `ws` 列を持つ全行を引き、`cmux list-workspaces` 出力と照合。
   - マッチしない行を `MarkFailedWithReason(id, "workspace disappeared (reaper)")` で failed へ。
   - audit `reaper.failed` を 1 件 record (`Key=task:<id>`, `Value=<vanished ws>`)。
2. **`started_at + threshold (default 24h)` 超の running を警告**
   - 閾値は config `execution.reaper_stuck_threshold` (default `"24h"`)。
   - `judgment_reason` に `[reaper] stuck running over <threshold>` を append (既存 reason は `; ` で連結)。
   - audit `reaper.warn` を 1 件 record。
   - **status は変更しない** (人間判断を保留)。
3. **CLI**: `marunage reaper` で 1 回スイープ。daemon 化は PR-71 の責務。

## スコープ外

- daemon / cron / loop ループ (PR-71)
- `marunage clean --apply` のような ws 列単独のクリア (PR-22 既出)
- 24h 経過 row の自動 fail (人間判断必須)

## アーキテクチャ

```
cmd/marunage
   └── internal/cli/reaper.go          (newReaperCmd → reaperFactory)
        └── internal/reaper/           (新規)
             ├─ Reaper{store, cmux, now, stuckThreshold, auditor}
             ├─ Option (functional options)
             └─ Run(ctx) error  ← 1 周スイープ
                ├─ store.List(Statuses=[running])
                ├─ cmux.ListWorkspaces()  ← 新規 / Client IF 拡張
                ├─ disappeared 判定 → MarkFailedWithReason + audit.failed
                └─ stuck 判定 → AppendJudgmentReason + audit.warn
```

### 依存パッケージへの追加

| パッケージ | 追加 | 理由 |
|---|---|---|
| `internal/cmux` | `Client.ListWorkspaces()` メソッド + 実装 | task 指示「`cmux list-workspaces` の parse は internal/cmux/ に追加するのが自然」。dispatch_test の fakeCmux は noop stub を生やして既存テスト緑のまま。 |
| `internal/store` | `TaskRepo.AppendJudgmentReason(id, suffix)` | 既存 `MarkFailedWithReason` は status=failed への遷移を伴うため、stuck warn (status 維持) には使えない。新規 helper は単一 SQL UPDATE の CASE 式で「既存値 + `; ` + suffix」を atomic に書き込み、Get + Update の race を回避。 |
| `internal/config` | `ExecutionConfig.ReaperStuckThreshold` (string, default "24h") | `human_wait_timeout` と同じく `time.ParseDuration` 形式の文字列。Validate で空文字拒否 + duration parse 検証。 |
| `internal/cli` | `reaper.go` 新規 (factory hook + cobra cmd) + root.go 登録 | dispatch.go の factory hook パターン踏襲。`activeReaperFactory()` で test override 可能。 |

## 設計上の判断ログ

### A. `Client.ListWorkspaces` は cmux パッケージに置く (cli ではなく)

- **問題**: 既存の `internal/cli/workspace_lister.go` (PR-22 clean 用) も同じ regex で list-workspaces を parse している。
- **選択**: 本 PR では cmux に正規パスを置き、cli 側はそのまま (将来 dedup PR で統合)。**理由**: PR-44 のスコープを reaper に閉じるため。cli 側の dedup は別 PR の責務。
- **代替検討**: cli の `cmuxWorkspaceLister` を reaper でも使う案 → 却下 (cli が下位レイヤを露出してしまう)。

### B. `AppendJudgmentReason` を store に追加 (reaper 内で Get + Update せず)

- **問題**: stuck warn は「現在の `judgment_reason` に suffix を `; ` 区切りで追記」する操作。reaper 内で Get → 連結 → MarkFailedWithReason 風 helper を呼ぶと non-atomic になり、並行 dispatcher / 人間操作と race する。
- **選択**: store に SQL CASE 式の atomic UPDATE を持つ `AppendJudgmentReason` を追加。
- **副次効果**: 将来「triage の append note」用途にも再利用可能。

### C. stuck warn は idempotent (loop / cron 前提)

- **問題**: 同じ row に対して daemon ループが毎ティック warn を append すると、judgment_reason と audit.log が肥大化する。
- **選択**: `judgment_reason` に既に `[reaper] stuck running over <threshold>` 文字列が含まれていたら 2 回目以降は audit/append とも skip。
- **テスト**: D6 で `Run` を 2 回呼んで `strings.Count == 1` を pin。

### D. cmux 消失と stuck warn が同時に発生する row は failed を優先

- **問題**: ws が消えた + 24h 超過 の row に対し warn と failed の両方を発火すると、二重 log + judgment_reason に過剰情報が乗る。
- **選択**: failed 遷移を優先し、warn は skip。**理由**: failed 状態の "理由" はもう確定しているので追加情報の価値が薄い。
- **テスト**: D7 で pin。

### E. 並行 dispatcher との race

- 現実装: `MarkFailedWithReason` は status guard 無しの UPDATE。dispatcher が同時に行を `done` に動かしていた場合、reaper の List スナップショット時点で `running` だったとしても reaper が後勝ちで failed に上書きしてしまう (理論上)。
- **判断**: dispatcher の write 順序 (SetWorkspace → SetStartedAt → UpdateStatus(running) → audit.start → WaitReady → ...) は dispatch.dispatchOne 完了まで running のまま。dispatchOne 内では markFailed 以外で running から脱出しない。一方 reaper が動くタイミングで dispatcher が同 row を done に動かす経路は**現状存在しない** (done 遷移は手動 CLI / atomic sentinel PR-43 のみ)。
- **将来 hardening**: PR-43 の atomic sentinel が daemon で並行動作するようになったら、`MarkFailedWithReason` に status guard を追加する PR を別途切る。本 PR では race E2 テスト (List 後に他者が done に動かす → reaper が catastrophic error にならない) のみ pin。

### F. config validation

- `Validate` は空文字も `time.ParseDuration` 失敗扱いで拒否 (空文字は parse エラー)。
- 将来「reaper 無効化」用途なら `0s` 等を明示。

### G. CLI factory pattern

- dispatch.go の factory hook パターン (`reaperFactoryHook` + `activeReaperFactory`) を踏襲。
- production factory が auditor open 失敗時に `NopAuditor` フォールバックする挙動も dispatch.go と一致。

## audit log フォーマット

| Action | Key | Value |
|---|---|---|
| `reaper.failed` | `task:<id>` | 消失した ws (`workspace:NNN`) |
| `reaper.warn`   | `task:<id>` | append された warn 文字列 (`[reaper] stuck running over 24h`) |

## テスト観点

- TDD: `.test-list.md` に 6 グループ (A〜H) で網羅、Red→Green→Refactor を 1 件ずつ。
- store の SQL CASE 式は単独テスト 4 件 (空 / 既存あり / empty suffix reject / not found) で pin。
- reaper パッケージは fake `Cmux.ListWorkspaces` + 実 SQLite + recordingAuditor で挙動を統合 pin。
- cli 層は factory hook で fake reaper を inject。
- race detector clean (`go test -race ./...`)。

## 影響評価

| 既存機能 | 影響 |
|---|---|
| dispatch | `cmux.Client` interface 拡張に伴い fakeCmux に `ListWorkspaces` noop stub を追加。それ以外は影響なし (dispatch は ListWorkspaces を呼ばない)。 |
| clean (PR-22) | 影響なし。`cli/workspace_lister.go` はそのまま。ただし将来 dedup PR で `cmux.Client.ListWorkspaces` への統合を検討。 |
| atomic sentinel (PR-43) | 影響なし。done への遷移は sentinel が担う。reaper が done を上書きする経路は前述 race 注記の通り「dispatcher が done に動かす経路がまだ存在しない」前提で安全。 |
| audit.log | 新規 Action 2 種 (`reaper.failed`, `reaper.warn`)。grep 整合性のためドット区切り規約に従う。 |

## 完了条件

- [x] `make test` race clean
- [x] `marunage reaper --help` が表示される
- [x] integration テスト (DB に 2 行 / cmux 1 行で 1 行のみ failed) が pin
- [x] design-review → 必須対応反映 (status guard / silent failure 解消 / sep 定数化 / 重複入力テスト)
- [ ] review-fix-loop で Critical/Warning ゼロ
- [ ] PR コメント + `docs/pr_split_plan.md` の `[x]` チェック

## audit 値のフォーマット契約

- `reaper.warn` の Value は `[reaper] stuck running over <threshold>`。`<threshold>` は `time.Duration.String()` の出力 (24h0m0s → `"24h"`、30m0s → `"30m0s"`)。設定値の文字列をそのまま転記しないため、`24h` と `1d` のような表記揺れを吸収する。
- `reaper.failed` の Value は消失した ws id (`workspace:NNN`)。

## per-row 書込失敗時のログ方針

- `markDisappeared` / `markStuck` の DB 書込失敗は `slog.Warn` に出す (audit ではなく構造化ログ)。
- 例外: `MarkFailedFromRunningWithReason` が `ErrInvalidTransition` を返した場合は `slog.Debug` (race window の想定挙動)。
- audit に `reaper.error` を追加するかは PR-71 の構造化ログ整備で再評価。

## auditor open 失敗フォールバック

- `productionReaperFactory` は audit.log open 失敗時に `NopAuditor` にフォールバックする (dispatch.go と一致)。
- 失敗ログは出さない (dispatch.go と同一)。doctor / startup wiring が disk 問題を独立に surface する責務。

## 設計レビュー反映ログ (2026-05-03 design-review)

| 指摘 | 対応 |
|---|---|
| security🟡 / data-model🟡 / daemon-runtime🔴: status guard 無しで done を上書きするリスク | `store.MarkFailedFromRunningWithReason` を新設、reaper はこれを使うよう変更。E1 race テスト強化。 |
| security🟡 / go-design🟡: per-row write 失敗が silent | `markDisappeared` / `markStuck` で `slog.Warn` (race window は `slog.Debug`)。 |
| data-model🟡: `"; "` がマジック文字列 | store に `judgmentReasonSeparator` const を新設、`AppendJudgmentReason` SQL でバインド。 |
| go-design🟢: `WithStuckThreshold(0)` semantics | godoc に「0/負値は 24h fallback」を明記。 |
| test-strategy🟢: ListWorkspaces 重複処理 | `TestListWorkspacesPreservesDuplicates` 追加。 |
| test-strategy🟡: C5 冪等性双子 | `TestRunFailedIsIdempotent` 追加 (status guard で自然成立)。 |

## Open Questions / Future hardening (本 PR スコープ外)

- **`marunage clean` と `marunage reaper` の用語整合**: `requirement.md` L90 では `clean` を「reaper の手動トリガ」と記載するが、`pr_split_plan.md` PR-22 / PR-44 は責務を分離している (clean = ws 列単独クリア / reaper = 状態遷移)。要件改訂 or `clean` を `reaper` の thin wrapper にする決着が `requirement.review.md` 側で必要。
- **24h 超 running の通知**: `requirement.md` L735 は audit + 通知と書くが、本 PR は audit のみ。Notifier 連携は PR-71 / Notifier 統合 PR の責務。
- **二重起動防止 (PID lock)**: cron と loop で `marunage reaper` を同時起動した際の挙動は SQLite WAL に委ねる前提。advisory lock の追加は PR-71 daemon 統合で再評価。
- **`cmux list-workspaces` 失敗の分類**: 現状は Run の戻り値に伝播するのみ。daemon ループでの transient retry / fatal exit 分類は PR-71 で。
- **`reaper.error` audit の追加**: per-row 書込失敗を audit 化する案は PR-71 の構造化ログ整備で。
- **doctor 連携**: reaper 死活確認 hook を `marunage doctor` に追加するのは PR-71。
- **`MarkFailedWithReason` への status guard 移植**: 本 PR は reaper 用に新 helper を作る方針。既存 `MarkFailedWithReason` (dispatch.markFailed が利用) への guard 追加は dispatch のテスト/挙動への影響評価が必要なため別 PR。
- **judgment_reason の長さガード**: triage note と reaper warn が積み重なる長期運用で TEXT 肥大化のリスク。truncate + 省略マーカー戦略は別 PR で。
- **stuck warn の AppendJudgmentReason への running guard**: stuck warn は破壊性が低いため本 PR では race を許容 (List スナップショット信頼)。気になるなら `AppendJudgmentReasonIfStatus` を別 PR で。
