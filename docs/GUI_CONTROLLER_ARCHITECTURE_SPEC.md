# GUI Controller Architecture Specification

ImagePadServer の管理GUIを、Xcode/AppKit の ViewController 的な責務分離に寄せるための仕様書です。対象はローカル管理UIの JavaScript 構造であり、サーバーAPI、既存HTML ID、公開URL契約、画像/動画/OBS/履歴/設定のユーザー導線は維持します。

## 背景

GUI大改装後、見た目の整理は進んだ一方で、次のような不具合が同じ領域に集中しました。

- 動画プレビューが HLS.js とネイティブHLS判定の順序で壊れる
- 通常動画プレビューの HLS.js インスタンスが状態更新で破棄される
- 管理画面プレビューが外部公開URLへ寄り、Cloudflare Tunnel/DNS状態に影響される
- 再生ボタンの hover が共通 button CSS に引っ張られて位置ずれする
- 履歴公開、アップロード、OBS、プレビューが同じグローバル状態を直接触る

これらは「見た目」「状態同期」「ユーザー操作」「HLS/OBSのリソース寿命」が同じスクリプト内で混ざっていることが主因です。

## 目的

- GUIの各領域に Controller を用意し、責務と副作用範囲を明確にする
- 既存の HTML 構造、要素ID、API エンドポイントを維持する
- まず PreviewController を導入し、動画/画像/OBSプレビューの事故を封じる
- その後 UploadController、HistoryController、SettingsController へ段階的に展開する
- 1回の変更で GUI 全体を再破壊しない

## 非目的

- React/Vue/Svelte などへの移行
- Goテンプレートの全面再設計
- サーバーAPIの名前変更
- 既存ボタンID、フォームID、URLコピー導線の削除
- OBS/動画変換/アップロードのサーバー処理変更
- デザイン刷新そのもの

## 現在のファイル構成

現状は `internal/server/ui_scripts.go` が複数の JavaScript 断片を結合し、以下のファイルが主に GUI 挙動を担っています。

- `internal/server/ui_script_state.go`: 共有状態の初期定義
- `internal/server/ui_script_domrefs.go`: DOM参照と入力要素参照
- `internal/server/ui_script_statesync.go`: `/api/state` の反映
- `internal/server/ui_script_livesync.go`: SSE/BroadcastChannel/refresh scheduling
- `internal/server/ui_script_preview.go`: 共有URL表示、画像/動画/OBSプレビュー、HLS.js
- `internal/server/ui_script_uploadevents.go`: アップロード、リンク、候補ダイアログ
- `internal/server/ui_script_uploadstate.go`: 入力モードとフォーム状態
- `internal/server/ui_script_historyqueue.go`: 履歴、お気に入り、キュー表示
- `internal/server/ui_script_obs.go`: OBS状態、接続情報、RTSPコピー
- `internal/server/ui_script_settings.go`: 設定、テーマ、QR、補助モーダル
- `internal/server/ui_script_progressstatus.go`: 進捗表示
- `internal/server/ui_script_toast.go`: トーストとエラーレポート
- `internal/server/ui_css_components.go`: プレビュー、モーダル、履歴などの部品CSS

## 目標構成

Controller はクラス構文を使わず、現行のテンプレート文字列に自然に収まる関数オブジェクト形式にします。理由は、既存コードがグローバル関数と定数結合で構成されており、最小差分で導入できるためです。

### StateSync

責務:

- `/api/state` の取得
- 取得した state の正規化
- 各 controller への `render(data, context)` 呼び出し
- SSE/BroadcastChannel/focus/online の refresh scheduling

禁止:

- 個別UI領域の DOM を直接書き換えること
- HLS.js や video 要素を直接操作すること

### PreviewController

責務:

- `#preview` の中身だけを管理する
- `#previewHeading` などプレビュー見出しの文言を必要に応じて更新する
- 画像、通常動画、OBS HLSプレビュー、ingest/video変換進捗、空状態を排他的に描画する
- ローカル送信中はアップロード欄側の進捗を優先し、プレビュー枠に送信プログレスを出さない
- 他端末からのアップロード受信中は、ホスト側プレビュー枠に `受信中...` と受信バイト進捗を表示する
- HLS.js インスタンスの作成、維持、破棄を一元管理する
- 管理画面内プレビューは同一オリジンURLを優先する
- 通常動画の再生ボタン、状態同期、エラー表示を管理する

禁止:

- 履歴、アップロード、設定、OBS接続リストを直接書き換えること
- `state.shareURL` を直接変更すること
- `uploadMode` を変更すること
- 外部公開URLを管理画面プレビューの第一候補にすること

### UploadController

責務:

- ファイル選択、URL入力、OBSモード切替の UI 状態
- 送信中プログレス表示。表示位置は③公開ボタン群の直下に固定する
- 送信側のローカル進捗文言は `送信中...` とし、ホスト側の `受信中...` と混同しない
- 静止画/動画モードごとの入力制御
- 変換オプションの表示/非表示

禁止:

- `#preview` の中身を直接書き換えること。送信完了後の変換/生成状態だけ PreviewController に任せる
- 履歴リストを直接再描画すること

### HistoryController

責務:

- 履歴、お気に入り、動画変換キュー表示
- 公開切替ボタン、変換追加ボタン、お気に入りボタン
- 履歴選択後のターゲットモード誘導

禁止:

- OBSライブモードへ勝手に戻すこと
- プレビューDOMを直接書き換えること
- アップロードフォームを直接初期化すること

### SettingsController

責務:

- 設定モーダル
- テーマ設定
- スマホ接続QR
- アプリ終了
- OBSレイテンシなど低頻度設定

禁止:

- 公開中メディア、履歴、アップロード進捗を直接書き換えること

### ToastController

責務:

- トースト表示
- エラーレポート生成
- エラーレポートコピー

禁止:

- 各 controller の状態を補正すること

## Controller API

各 controller は次の形式を基本にします。

```js
const PreviewController = (() => {
  let hls = null;
  let mode = '';
  let mediaID = '';
  let mediaURL = '';

  function init(deps) {
    // deps: { preview, showToast, scheduleRefresh, getPageToken }
  }

  function render(data, context) {
    // context: { uploadMode, mediaIntent, localUploadActive, nextCurrentID }
  }

  function reset(reason) {
    // Release resources and clear local mode.
  }

  return { init, render, reset };
})();
```

## State Ownership

### Global state allowed during migration

既存コードとの互換性のため、次は当面グローバルのまま残します。

- `state`
- `uploadMode`
- `mediaIntent`
- `localUploadActive`
- `pendingQueue`
- `selectedFile`

ただし controller 内から直接変更できる範囲は段階的に狭めます。

### Controller local state

PreviewController は次を自分で持ちます。

- 現在の表示モード
- 現在の media ID
- 現在の preview URL
- HLS.js インスタンス
- OBS preview ID
- OBS preview URL

この情報は `state.previewMode` へ混ぜ戻さない方針にします。既存互換のため一時的に `state.previewMode` が残る場合でも、最終的な正は controller local state とします。

## Preview URL Policy

管理画面内の再生確認は、外部公開URLではなく同一オリジンURLを優先します。

入力:

- `data.hlsURL`
- `data.publicHLSURL`
- `data.videoURL`
- `data.publicVideoURL`
- `data.obs.previewURL`

通常動画:

```js
sameOriginPreviewURL(data.hlsURL || data.publicHLSURL || data.videoURL || data.publicVideoURL || '')
```

OBS:

```js
sameOriginPreviewURL(data.obs.previewURL || '')
```

ルール:

- `https://*.trycloudflare.com/stream/...` は `/stream/...` に正規化する
- 現在ページに `?token=` がある場合、正規化URLにも token を付ける
- 共有URL表示は既存の外部URL優先を維持する

## HLS.js Resource Policy

- HLS.js が利用可能なら native HLS 判定より優先する
- `video.canPlayType('application/vnd.apple.mpegurl')` は Chrome 系で `maybe` を返すことがあるため、第一候補にしない
- 表示中の通常動画が同じ media ID / URL のままなら HLS.js を破棄しない
- 画像、空状態、変換進捗、ローカル送信ガード、別動画、OBS切替では HLS.js を破棄する
- OBS と通常動画の HLS インスタンスは同時に持たない

## Preview Playback Button Policy

- ネイティブコントローラーは残す
- 通常動画プレビューには独自の再生/一時停止ボタンを重ねる
- ボタンは `video.play()` / `video.pause()` を直接呼ぶ
- ボタンは薄いグレーの半透明円、黒系アイコン
- hover で位置やサイズを変えない
- 共通 `button:hover` の transform を受けないよう、PreviewController/CSS側で固定する

## Existing Contract Preservation

次は削除/改名しません。

- `#preview`
- `#shareURL`
- `#shareURLLabel`
- `#imageInput`
- `#uploadButton`
- `#queueUploadButton`
- `#settingsButton`
- `#phoneConnectButton`
- `#historyList`
- `#favoritesList`
- `#videoQueueList`
- `/api/state`
- `/api/history/select`
- `/api/upload`
- `/api/upload-url`
- `/stream/...`
- `/image/current`
- `/video/current.mp4`

## Migration Strategy

### Phase 1: PreviewController

最初に PreviewController だけを導入します。

理由:

- 最近の不具合が最も集中している
- HLS.js のリソース寿命を局所化できる
- 他領域への影響が小さい
- ブラウザ検証で成否が明確

### Phase 2: UploadController

次に UploadController を導入します。

理由:

- 送信中プログレス、ちらつき、大容量アップロード時の表示が絡む
- 静止画/動画モードの UI 分岐が肥大化している

### Phase 3: HistoryController

履歴、公開切替、お気に入り、変換キューを分けます。

理由:

- 履歴公開後のモード遷移が複数回問題化している
- OBS録画と通常動画のターゲットモードが混ざりやすい

### Phase 4: SettingsController

設定、QR、テーマ、OBSレイテンシ設定を整理します。

理由:

- 常用導線ではないが、状態とモーダルが散らばっている
- 設定項目を隠す方針と相性がよい

## Acceptance Criteria

### PreviewController

- 画像公開時、`#preview img` が表示される
- 通常動画公開時、`#preview video` と `.preview-play-button` が表示される
- 通常動画の再生ボタンをクリックすると `currentTime` が進む
- HLS.js 利用時、`video.currentSrc` は blob URL になる
- `/stream/...m3u8` と `.ts` セグメントが同一オリジンから取得される
- hover 前後で再生ボタンの座標が変わらない
- OBSモード時、OBSプレビューは既存通り HLS プレビューとして表示される
- 空状態、変換中、送信中ガードの表示が壊れない

### UploadController

- 静止画モードで動画変換UIが出ない
- 動画モードで画像専用変換ツールが出ない
- スマホ/別PCアップロード時に送信プログレスがちらつかない
- 送信プログレスはプレビュー枠ではなく③公開ボタン群の直下に表示される
- ホスト側では受信中にプレビュー枠へ `受信中...` が表示される
- 動画変換の設定画質は上限として扱い、入力映像が低解像度なら入力解像度以下の実効画質でエンコードする
- 2GB超過時のエラーが送信後に明示される

### HistoryController

- 履歴の「公開に切替」で保存済み動画が live OBS モードへ戻らない
- お気に入りSVGの中塗り/中空が統一される
- 履歴スクロール領域がウィンドウ下限にフィットする

### SettingsController

- スマホ接続QRはPC起動時のみ表示され、スマホ表示では起動時自動表示しない
- QR保護表示はクリックで解除される
- テーマ設定がライト/ダーク/OS追従で維持される

## Verification Commands

基本確認:

```powershell
go test ./internal/server -timeout 120s
go test ./internal/app ./internal/obsrtmp ./internal/video -timeout 120s
go build -o (Join-Path $env:TEMP 'imagepadserver-codex-gui-latest.exe') ./cmd/imagepadserver
```

起動確認:

```powershell
$ports = Get-NetTCPConnection -LocalPort 8819 -State Listen -ErrorAction SilentlyContinue
foreach ($p in $ports) { Stop-Process -Id $p.OwningProcess -Force -ErrorAction SilentlyContinue }
$env:IMAGEPAD_PORT = "8819"
$out = Join-Path $env:TEMP "imagepadserver-codex-gui-latest.exe"
Start-Process -FilePath $out -WorkingDirectory (Get-Location) -WindowStyle Hidden
Start-Sleep -Seconds 5
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8819/healthz -TimeoutSec 5
```

ブラウザ確認:

- `http://127.0.0.1:8819/` を開く
- 履歴から動画を公開へ切り替える
- 再生ボタンをクリックする
- `video.currentTime` が 2秒以上進む
- 再生ボタン hover 前後の `getBoundingClientRect()` が一致する

## Rollback Strategy

PreviewController 導入で問題が出た場合:

- `ui_script_preview_controller.go` の結合を `ui_scripts.go` から外す
- `ui_script_preview.go` の既存関数呼び出しへ戻す
- `ui_media_test.go` の controller 専用期待値だけを戻す

Upload/History/Settings は PreviewController 完了後に進めるため、段階ごとに戻せる状態を維持します。
