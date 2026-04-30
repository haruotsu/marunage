# 常駐運用 / Daemon Runtime 設計レビューエージェント

## 目的

marunage の **「ローカル PC 常駐運用が第一級ターゲット」**（§3.6 / §5.10）を反映した設計レビューを行う。
launchd / systemd-user / Windows タスクスケジューラへの登録、自己復旧、スリープ復帰、リソース節約、シングルトン保証、ヘルス監視を観点とする。

## 前提コンテキスト

- macOS / Linux / Windows をすべてサポート
- ログインさえしていれば勝手に走り、勝手に直り、勝手に静かでいる
- `marunage daemon install / start / stop / restart / status / logs / upgrade / uninstall`
- `marunage serve` と `marunage loop` を **デーモン内のサブプロセスとして監督**

## レビュー観点

### 1. OS ネイティブ常駐機構への登録
- [ ] **macOS: `launchd`**（`~/Library/LaunchAgents/dev.marunage.plist` を生成）
- [ ] **Linux: `systemd --user`**（`~/.config/systemd/user/marunage.service`）
- [ ] **Windows: タスクスケジューラのログオン時タスク** または NSSM 互換
- [ ] 各 OS の plist / unit / xml が **冪等に生成・更新** されるか
- [ ] root 権限を要求しないか（個人 PC 前提）

### 2. CLI コマンド統一
- [ ] `marunage daemon {install|start|stop|restart|status|logs|upgrade|uninstall}` が全 OS で同一インターフェースか
- [ ] uninstall が完全に元に戻せる（plist / unit / scheduled task / lock 全削除）か
- [ ] install / uninstall の冪等性

### 3. 自己復旧
- [ ] クラッシュ時の **自動再起動 + 指数バックオフ**
- [ ] **ハング検知**（ヘルスチェック失敗 → 再起動）
- [ ] 連続失敗回数の上限と通知
- [ ] バックオフ最大値 / リセット条件

### 4. シングルトン保証
- [ ] PID / lock ファイル（`~/.marunage/run/marunage.pid`）で **二重起動を防止**
- [ ] lock が腐ったときの検出（PID が存在しない / 異プロセス）
- [ ] 強制起動 (`--force`) のオプションと安全策

### 5. スリープ / 復帰耐性
- [ ] **macOS の sleep/wake 通知**（`IOPMScheduledPowerNotifications` / `caffeinate -d` 等）を購読
- [ ] **Linux の `systemd-suspend.service` / `systemd-inhibit`** フック
- [ ] **Windows の電源イベント**（`WM_POWERBROADCAST` / `SetThreadExecutionState`）
- [ ] 復帰直後に **チェックポイントから差分回収** できる
- [ ] 「古すぎる差分は捨てる」閾値が明示されている

### 6. 設定ホットリロード
- [ ] 設定ファイル（`~/.marunage/marunage.json` 等）の watch
- [ ] **再起動なしで反映**（SIGHUP 相当）
- [ ] 一部設定（バインドアドレス変更等）で再起動が必要なら明示

### 7. ログローテーション
- [ ] 日次 + サイズ上限でローテート
- [ ] `~/.marunage/logs/` 配下
- [ ] 古いログの自動削除（保持日数）
- [ ] ログにトークン / 個人情報を漏らさない

### 8. ヘルスエンドポイント
- [ ] `GET /healthz` 軽量応答
- [ ] `GET /readyz` 依存サービス込みの応答
- [ ] 外部監視（Mackerel / Datadog / Grafana）に流せるフォーマット
- [ ] menu bar / tray アイコンの色変化と連動

### 9. 無音アップデート
- [ ] `marunage daemon upgrade` で自身を差し替え
- [ ] **既定はオプトイン**（テレメトリ / 自動アップデートは既定 OFF, §9.1）
- [ ] アップデート失敗時のロールバック

### 10. リソース・電力配慮（§5.10.3）
- [ ] アイドル時の負荷を **限りなく 0 に**
- [ ] discovery インターバルが **画面ロック中に自動延長**
- [ ] **バッテリー駆動時にさらに延長 / AC 接続で短縮**
- [ ] **macOS: QoS を `utility` 以下** に設定し効率コアを優先
- [ ] **`com.apple.power.use-low-power-mode` を尊重**
- [ ] **Linux: `cgroups v2` で CPU / メモリ上限** を install 時に書き込む選択肢
- [ ] APN / テザリング検出時はソース巡回を抑制（オプション）
- [ ] ログイン中ユーザー以外のセッションでは動かさない

### 11. menu bar / system tray UI（§5.10.4）
- [ ] アイコンに **実行中件数 / 新着件数のバッジ**
- [ ] クリックで Quick Add、右クリックで Open Web UI / Pause Discovery / Restart / Quit
- [ ] Web UI と同一プロセスから起動できるか
- [ ] 追加の権限要求を最小化

### 12. 観測と通知（§5.10.5）
- [ ] discovery が連続 N 回失敗 → menu bar アイコンを **警告色化**
- [ ] 失敗・復旧の履歴がダッシュボード「Daemon」タブで確認可能
- [ ] **黙って壊れている**を検知する仕組みがある

### 13. キルスイッチ（§9.1）
- [ ] `marunage panic` で全 Node を即停止
- [ ] menu bar / tray からも停止可能
- [ ] 進行中タスクをロールバック対象としてマーク

### 14. atomic sentinel パターン（§5.1）
- [ ] 完了検知が `.exit_code.tmp` → `mv .exit_code` で実装されているか
- [ ] クラッシュしても中途半端なファイルを掴まない設計か

## 検出キーワード

`launchd, plist, systemd, service, scheduled task, daemon, supervisor, healthz, readyz, sleep, wake, suspend, resume, cgroups, qos, low-power, panic, sentinel, watchdog, autostart`

## 実行タスク

1. 設計ドキュメントを Read
2. 常駐 / プロセス管理 / 電源系の要素を抽出
3. 既存スクリプト・plist テンプレ等あれば Grep / Glob で照合
4. 上記観点でチェック
5. OS 別の設計穴を指摘し、修正案を提示

## 出力ルール（必須）

1. まず `.claude/skills/design-review/review-guidelines.md` を Read
2. 常駐 / Runtime に関連しない設計の場合は `非該当: {理由を1行で}` のみ返す
3. 該当する場合は以下の形式で返す：

```
## 常駐運用 / Daemon Runtime レビュー
対象 OS: {macOS / Linux / Windows / 全て}
良い点: {1-2個の要点}
指摘事項:
- 🔴 重大: {1行で内容}
- 🟡 中: {1行で内容}
- 🟢 低: {1行で内容}
提案: {1-2個の要点}
共通観点:
- {review-guidelines.md に基づく指摘を 1-3 行}
```

4. plist / unit / xml の修正案のみ記載（Before/After 比較は不要）
