# Gmail source test list

## A. Plugin construction
- [x] A1. New() のデフォルト値が PR-80 仕様と一致する (Query / CompleteLabel / CheckpointKey)
- [x] A2. WithQuery でデフォルトを上書きできる
- [x] A3. WithCompleteLabel でデフォルトを上書きできる
- [x] A4. WithCheckpointKey でデフォルトを上書きできる

## B. Plugin.List
- [x] B1. WithClient 未設定で ErrClientNotSet を返す
- [x] B2. メッセージなしで空スライスを返す
- [x] B3. Message → source.Task への変換が正しい (Source / ExternalID / Title / Body / SourcePath)
- [x] B4. From が空の場合 RawMetadata["from"] を省略する
- [x] B5. ThreadID / Labels が空の場合 RawMetadata から省略する
- [x] B6. RawMetadata["labels"] は防御コピーである
- [x] B7. archive ラベルが付いているメッセージは Done=true になる
- [x] B8. 設定済みクエリをクライアントに渡す
- [x] B9. クライアントエラーを wrap して返す
- [x] B10. context キャンセル時にクライアントを呼ばない

## C. Plugin.Since (Sincer)
- [x] C1. Checkpointer 未設定で List にフォールバックする
- [x] C2. 初回呼び出しで全件返し checkpoint を先頭 ID に更新する
- [x] C3. checkpoint 以降のメッセージのみ返す
- [x] C4. 新着ゼロでも checkpoint を変更しない
- [x] C5. WithCheckpointKey を使う
- [x] C6. クライアントエラー時に checkpoint を変えない
- [x] C7. WithClient 未設定で ErrClientNotSet を返す
- [x] C8. context キャンセルを尊重する
- [x] C9. Checkpointer.Get エラーを wrap して返す
- [x] C10. Checkpointer.Set エラーを wrap して返す

## D. Plugin.Complete (Completer)
- [x] D1. archive + 既読解除を 1 回の ModifyLabels で行う
- [x] D2. WithCompleteLabel で設定したラベルを使う
- [x] D3. ErrClientMessageNotFound → ErrTaskNotFound に変換する
- [x] D4. その他のクライアントエラーを wrap して返す
- [x] D5. WithClient 未設定で ErrClientNotSet を返す
- [x] D6. context キャンセルを尊重する

## E. Plugin.AuthStatus
- [x] E1. nil クライアントで AuthNotConfigured を返す
- [x] E2. AuthAuthenticated をそのまま返す
- [x] E3. ErrClientNotConfigured → AuthNotConfigured に変換する
- [x] E4. ErrClientCredentialsExpired → AuthExpired に変換する
- [x] E5. ErrClientCredentialsRevoked → AuthRevoked に変換する
- [x] E6. 未分類エラーを wrap して返す
- [x] E7. context キャンセルを尊重する

## F. Plugin.Setup
- [x] F1. SetupOptions をクライアントに転送する
- [x] F2. WithClient 未設定で ErrClientNotSet を返す
- [x] F3. クライアントエラーを wrap して返す
- [x] F4. context キャンセルを尊重する

## G. GWSClient
- [x] G1. messages.list コマンド形状 (サブコマンド / userId / q / maxResults / --format json)
- [x] G1b. maxResults のデフォルト値が正の数である
- [x] G1c. WithMaxResults でデフォルトを上書きできる
- [x] G2. メッセージ1件ごとに messages.get を発行する (N+1)
- [x] G3. Subject / Snippet / Labels / From を正しくパースする
- [x] G4. メッセージなしで空スライスを返す (get 呼び出しなし)
- [x] G5. WithNewerThan でクエリに newer_than:Nd が付加される
- [x] G5b. newerThanDays=0 では newer_than が付加されない
- [x] G6. messages.list エラーを wrap する
- [x] G7. messages.get エラーをメッセージ ID 付きで wrap する
- [x] G8. messages.list の JSON が壊れていたら decode エラーを返す
- [x] G9. messages.get の JSON が壊れていたら decode エラーを返す
- [x] G10. messages.modify コマンド形状 (params.id / body.addLabelIds / body.removeLabelIds)
- [x] G11. messages.modify エラーを wrap する
- [x] G12. AuthStatus: probe 成功で AuthAuthenticated を返す
- [x] G13. AuthStatus: probe 失敗で AuthNotConfigured を返す (エラーなし)
- [x] G14. Authenticate: NonInteractive=true でランナーを呼ばずエラーを返す
- [x] G15. Authenticate: Interactive でプローブを実行しエラーを surface する
- [x] G16. Authenticate: Interactive でプローブ成功なら nil を返す
