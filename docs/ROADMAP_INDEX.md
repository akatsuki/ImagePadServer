# Code Index for ImagePadServer

このドキュメントは、ImagePadServer のコードを AI が修正・拡張するときの「地図」として使えるよう、ファイルごとの責務、処理フロー、重要ポイントをまとめた目録です。

今後の機能計画や優先順位は `docs/ROADMAP.md` に分離しています。このファイルは「何をやるか」ではなく「どこを読むか」を短時間で判断するための索引です。

---

## 0. 目次の使い方

- AI/開発者がまず見る順番:
  1. `cmd/imagepadserver/main.go`
  2. `internal/app/app.go`
  3. `internal/server/server.go`
  4. `internal/imageproc/processor.go`
  5. `internal/library/store.go`
  6. `internal/video/publisher.go`
  7. `internal/network/lan.go` / `internal/tunnel/tunnel.go`
  8. `internal/settings/settings.go` / `internal/config/config.go`
- 「どこに変更を入れればよいか」をすばやく見つけるため、責務ごとにファイルを分類しています。
- コメントやマークダウンの見出しで、ファイルの目的・起点・注意点を明示します。

### 0.1. 全体フローの読み方
- まず `cmd/imagepadserver/main.go` → `internal/app/app.go` の流れで起動シーケンスを把握します。
- 次に、目的の機能に応じて該当モジュールに移動します。
  - 画像処理: `internal/imageproc/processor.go`
  - メディア管理: `internal/library/store.go`
  - 動画/HLS: `internal/video/publisher.go`
  - 公開 URL / ネットワーク: `internal/network/lan.go` / `internal/tunnel/tunnel.go`
- このドキュメントは「何を変更するか」ではなく「どこを読むか」を短時間で判断するための案内です。
- 変更箇所が不明なときは、まず `internal/app/app.go` と `internal/server/server.go` を確認してください。

---

## 1. 実行エントリ / 起動制御

### `cmd/imagepadserver/main.go`
- 実行バイナリの入り口。
- `main()` では `app.Run()` を呼び出し、エラーがあればログに出力して終了コード `1` を返す。

### `internal/app/app.go`
- アプリ起動の「心臓部」。
- `Run()` / `OpenOrRun()` / `OpenWindowOrRun()` を定義。
- `config.FromEnv()` で `IMAGEPAD_*` 環境変数を読み取り。
- `library.NewStore()` で `%APPDATA%/ImagePadServer/media` を初期化。
- `http.Server` を作成し、`server.New(...).Register(mux)` でルート登録。
- `browser.Open()` / `appwindow.Show()` で UI 起動。
- `tray.Start()` でトレイアイコン（Windows）を開始。
- `tunnel.Start(originURL)` で Cloudflare Tunnel をバックグラウンド開始。
- `measureNetworkOnce()` で一度だけ帯域チェックし、`settings` に保存。
- 終了時に `resetMediaWorkspace()` を呼んで生成中メディアを削除。

#### 重要ポイント
- `serverIsHealthy()` は既存インスタンス検知に使われる。
- `UPnP auto port mapping is disabled for safety.` として、UPnP は本体起動時には利用されない。
- SteamVR はコード上に残るが、ここでは起動しない設計。

---

## 2. HTTP サーバー / Web UI / 管理 API

### `internal/server/server.go`
- 管理インターフェースと API を一つの `Server` に集中させている。
- `Server` が保持する主な状態:
  - `cfg`, `store`, `tmpl`
  - `lanURL`, `imageURLBase`, `previewURLBase`
  - `tunnelStatus`, `tunnelURLBase`
  - `adminToken`
- `New(cfg, store, imageURLBase)` で初期化し、`EnsureAdminToken()` で管理トークンを生成。

### 主要なルート
- `/` : 管理 UI を返す。
- `/api/state` : UI が画面表示用に呼ぶ現在状態。
- `/api/upload` : 画像や動画ファイルアップロード受付。
- `/api/upload-url` : 動画サイト URL 取り込み。
- `/api/clear` : 現在のメディアをクリア。
- `/api/copy-url` : コピー対象 URL を取得。
- `/api/about` : バージョンなど。
- `/api/update-check` : 更新チェック。
- `/api/video-player` : ビデオプレーヤーモード ON/OFF。
- `/api/video-quality` : 画質変更。
- `/api/network-check` : 帯域再測定。
- `/qr/phone.png` : QR コード画像生成。
- `/image/current` 系: 現在画像を配信。
- `/video/current.mp4` : 生成中/最新 MP4 を配信。
- `/stream/current.m3u8` : HLS プレイリスト配信。
- `/stream/` : HLS セグメント配信。
- `/favicon.ico` : アイコン。
- `/healthz` : シンプルなヘルスチェック。

### 管理権限の設計
- `admin()` ミドルウェアで API を保護。
- `adminAllowed()` の条件:
  - ループバックアクセスは常に許可。
  - プライベート LAN / リンクローカルは許可。
  - `token` パラメータ、`X-ImagePad-Token` ヘッダ、Cookie の `imagepad_admin` を検証。
- 管理トークンは `settings` に保存され、QR からのアクセスも許可。

### UI 表示と公開 URL
- UI 表示は埋め込み HTML テンプレート `indexHTML` による。
- サーバー側の UI ロジックや JavaScript 生成補助は `internal/server/ui.go` に入る。
- 管理 UI は公開 URL、QR、メディア状態、ビデオモード状態、Tunnel 状態を表示。
- `server` が `tunnelStatus` と `upnp` 状態を UI に渡す。

---

## 3. 画像処理 / 変換フロー

### `internal/imageproc/processor.go`
- 画像変換の中心。
- サポート画像形式: JPEG / PNG / SVG / BMP / TIFF / WEBP / GIF。
- SVG 変換は `oksvg` + `rasterx` でベクターからラスター化。
- EXIF 方向補正を `exifOrientation()` で判定し、`applyOrientation()` で鏡像・回転処理。
- `resizeToFit()` で最大辺 2048px に縮小。
- PNG/JPEG 選択を `Options.Format` で制御。JPEG は `encodeJPEGWithinLimit()` で最大バイト数制限。
- 出力結果は `Result{Path, PublicName, ContentType, Width, Height}`。

### 重要な挙動
- 入力を `io.LimitReader` でサイズ制限し、巨大なファイルを防止。
- `MaxBytes` のチェックを厳密に行い、超過時はエラー。
- 変換後ファイル名は `processed.jpg` / `processed.png` で統一。

---

## 4. メディア状態とワークスペース

### `internal/library/store.go`
- 現在公開中のメディア状態とファイルを保持する。
- `Store` の役割:
  - `dir` : メディア出力先ディレクトリ。
  - `current` : 現在表示中の `CurrentImage`。

### 保存・復元
- `NewStore(dir)` で強制リセットし、クリーンなワークスペースを作成。
- `Reset()` で `current` をクリアしディレクトリを再作成。
- `Clear()` でファイルを削除し、メタ情報を空に。
- `save()` / `load()` で `state.json` に永続化。

### `CurrentImage` の内容
- `ID` / `Kind` / `FileName` / `PublicName`
- `ContentType` / `Width` / `Height` / `SizeBytes`
- `OriginalName` / `UpdatedAt`

### 重要な関数
- `SetCurrent(srcPath, info)` : 受け取ったファイルを `current` にコピーし、メタを更新。
- `SetCurrentInfo(info)` : ファイルコピー不要でメタのみ更新。
- `CurrentPath()` : 現在の公開用ファイルパスとメタを取得。

---

## 5. 動画 / HLS / 外部ツール連携

### `internal/video/publisher.go`
- HLS と MP4 の生成ロジックを中心に持つパッケージ。
- 画像→動画変換や、動画サイト URL 取り込み時の変換を担当。

### 画質とプリセット
- `QualityPreset` に画質パラメータを集約。
- `ResolveQuality(mode, networkMbps)` : `auto` をネットワーク帯域から `1080/720/360` に決定。
- `BitrateOnlyPreset()` : 高さは維持しつつビットレートのみ変更。

### 生成フロー
- `PublishStillImage()` で画像を MP4 と HLS に同時生成。
- `PublishStillImageForID()` で ID ベースの HLS セグメント名を生成。
- 非同期版 `PublishStillImageAsyncForID()` では `activeHLS` 管理を用い、UI が進捗監視できる。
- `stopActive()` で既存 HLS ジョブを中断。

### 外部ツールパス解決
- `EnsureFFmpeg()` / `EnsureYTDLP()` で実行バイナリを探す。
- Windows では必要なら `%APPDATA%/ImagePadServer/bin` へ自動ダウンロード。
- ダウンロード時には SHA256 検証を想定しているが、固定値が空の場合はダウンロードを抑制。

### 重要ポイント
- `publish` 処理は `ffmpeg` コマンドを直接組み立てる。
- `-hls_playlist_type vod` で VOD 形式、`-hls_time 2` でセグメント化。
- `current0.ts` / `current.m3u8` など固定名と ID 付与を使い、古いキャッシュを回避。

---

## 6. ネットワークと公開 URL

### `internal/network/lan.go`
- `BestReachableIP(preferTailscale)` で最適な IP を選択。
- ループバック以外の IPv4 アドレスを探索。
- `PreferTailscale` が有効なら、`tailscale` インタフェースまたは 100.64.0.0/10 を優先。

### `internal/tunnel/tunnel.go`
- Cloudflare Tunnel の起動と監視。
- `Start(originURL)` : `cloudflared tunnel --no-autoupdate --url <originURL>` を実行。
- 標準出力/標準エラーから `https://xxx.trycloudflare.com` を抽出。
- `Tunnel` オブジェクトを返し、停止時にプロセスを終了。
- `ensureCloudflared()` : PATH 参照後、Windows なら自動ダウンロードを試行。
- `downloadCloudflared()` : 最新リリースをダウンロードし、`verifySHA256()` で検証。
- `ImageURL(base, path, id)` : URL 文字列を組み立て、キャッシュ突破用の `?v=` を付加。

### `internal/upnp/upnp.go`
- UPnP は存在するが、`app/app.go` では `SetPublicNetworkMessage()` で安全に無効化。
- つまり、UPnP ルートはコードにあるものの、本番起動では使用されない。

---

## 7. 設定 / 環境変数 / 永続化

### `internal/config/config.go`
- アプリ起動設定を環境変数から生成。
- サポートする環境変数:
  - `IMAGEPAD_HOST`
  - `IMAGEPAD_PORT`
  - `IMAGEPAD_ADVERTISE_HOST`
  - `IMAGEPAD_PREFER_TAILSCALE`
- `Config.URLForHost(host)` で `http://host:port/` を生成。
- `Config.AdvertisedHost(defaultHost)` で外部公開向けホスト名を決定。
- `truthy()` で `true/1/yes/on` を判定。

### `internal/settings/settings.go`
- アプリ固有設定を JSON で保存。
- `Settings` に保持する値:
  - `SteamVRExplicitlyDisabled`
  - `VideoPlayerEnabled`
  - `VideoQualityMode`
  - `NetworkMbps`, `NetworkUploadMbps`
  - `AdminToken`
- `Load()`, `Save()`, `Update()` でファイル排他制御。
- `EnsureAdminToken()` はトークンの自動生成と永続化。
- 保存先は `APPDATA/ImagePadServer/settings.json`。

---

## 8. プラットフォーム固有 / UI 補助

### `internal/browser/browser.go`
- ブラウザ起動を抽象化。
- OS 依存のブラウザ呼び出しをまとめる。

### `internal/clipboard/clipboard_windows.go`
- Windows のクリップボード操作を実装。
- コピー URL や状態メッセージのクリップボード書き込みに使われる。

### `internal/clipboard/clipboard_unsupported.go`
- Windows 以外ではクリップボード機能を無効化。

### `internal/appwindow/window_windows.go`
- Windows ネイティブウィンドウ表示。

### `internal/appwindow/window_unsupported.go`
- 非 Windows 環境ではネイティブウィンドウ機能は無効化。

### `internal/tray/tray_windows.go`
- Windows でトレイアイコンとコンテキストメニューを提供。

### `internal/tray/tray_unsupported.go`
- 他プラットフォームではトレイアイコン機能を無効化。

### `internal/appicon/icon.go`
- アプリのアイコンデータを読み込む。

### `internal/about/about.go`
- アプリ名・バージョン文字列などの定数を定義。

### プラットフォーム切り替えの注意
- `*_unsupported.go` はビルドタグではなく、Go のプラットフォームごとのファイル名によって自動選択される。
- `internal/video/hide_windows.go` / `internal/video/hide_unsupported.go` や `internal/tunnel/hide_windows.go` / `internal/tunnel/hide_unsupported.go` も同様の設計。

---

## 9. 冷凍済み SteamVR / 保守用資産

- `internal/steamvr/` 以下は「将来検証用」に残された実装。
- `app.go` 側のコメントでも「SteamVR integration is intentionally frozen.」と明示。
- したがって、本線のコード変更では `internal/steamvr` を触る必要は少ない。

---

## 10. 補足: 読みやすさについて

- このドキュメントはファイル責務と主要な処理領域を分かりやすく整理している。
- ただし、初めて見る人向けには「起動から公開 URL までの順序」や「画像→動画→配信のフロー図」を追加するとさらに理解しやすい。
- 重要な変更を加える場合は、まず `cmd/imagepadserver/main.go` → `internal/app/app.go` → `internal/server/server.go` の流れを追うとよい。

## 10. 追加ドキュメント

- `docs/ARCHITECTURE.md`
  - ファイル構造と起動フローの概略。
- `README.md`
  - ユーザー向けの操作手順、公開 URL / 動画モードの説明。
- `docs/TAILSCALE.md`
  - Tailscale 経路推奨とネットワーク設定に関する補足。
- `docs/WINDOWS_STEAMVR_HANDOFF.md`
  - SteamVR 開発／検証時の引き継ぎ用メモ。
- `docs/AI_DEVELOPMENT_WORKFLOW.md`, `docs/AI_SESSION_LOG.md`, `docs/AI_TEAM_HANDOFF.md`
  - AI 連携や開発セッションの記録、チーム向けハンドオフ情報。

---

## 11. 開発・ビルドツール

- `go.mod`
  - Go モジュール依存とバージョンを定義。
- `go.sum`
  - 依存パッケージのチェックサムを固定。
- `scripts/build-release.sh`
  - Windows 向けリリースビルド手順をまとめたスクリプト。
- `winres/winres.json`
  - Windows リソースビルド時のアイコンやメタ情報定義。
- `cmd/imagepadserver/rsrc_windows_amd64.syso`
  - Windows 実行ファイルに埋め込まれるリソースバイナリ。

---

## 12. アセットと BOOTH パッケージ

- `assets/`
  - アプリに必要な静的リソースを格納する汎用フォルダ。
- `booth/BOOTH_DESCRIPTION.md`
  - BOOTH 用の製品説明文。
- `booth/screenshots/`
  - BOOTH で使うスクリーンショット画像。

---

## 13. 参照用インデックス

### 優先的に読むべきコードパス
1. `cmd/imagepadserver/main.go`
2. `internal/app/app.go`
3. `internal/server/server.go`
4. `internal/imageproc/processor.go`
5. `internal/library/store.go`
6. `internal/video/publisher.go`
7. `internal/tunnel/tunnel.go`
8. `internal/config/config.go`
9. `internal/settings/settings.go`
10. `internal/network/lan.go`
11. `internal/upnp/upnp.go`
12. `internal/browser/browser.go`
13. `internal/appwindow/window_windows.go`
14. `internal/tray/tray_windows.go`
15. `internal/steamvr/steamvr_windows.go`

### 追加チェック対象
- `internal/config/config_test.go`
- `internal/imageproc/processor_test.go`
- `internal/library/store_test.go`
- `internal/server/server_test.go`
- `internal/settings/settings_test.go`
- `internal/network/lan_test.go`
- `docs/ARCHITECTURE.md`
- `scripts/build-release.sh`

### 変更時にチェックすべきポイント
- HTTP API ルートを追加するなら `internal/server/server.go` と `Register()` を確認。
- 画像変換ロジックを変更するなら `internal/imageproc/processor.go` の `Process()`、`decodeImage()`、`applyOrientation()` を確認。
- メディア公開状態を変えるなら `internal/library/store.go` の `SetCurrent()` / `Clear()` を確認。
- HLS / FFmpeg の挙動を変えるなら `internal/video/publisher.go` の `PublishStillImage*()` 系、`EnsureFFmpeg()` を確認。
- 公開 URL の生成やホスト選択を変えるなら `internal/config/config.go` / `internal/network/lan.go` / `internal/tunnel/tunnel.go` を確認。
- 管理トークンや設定永続化を変えるなら `internal/settings/settings.go` を確認。
- Windows 依存機能を調整するなら `internal/appwindow/` / `internal/tray/` / `internal/tunnel/` / `winres/winres.json` を確認。
- リリースビルド手順を変更するなら `scripts/build-release.sh` を確認。

---

## 14. 全ファイル一覧（参考）

- `go.mod`
- `go.sum`
- `README.md`
- `NOTICE.md`
- `LICENSE`
- `docs/ARCHITECTURE.md`
- `docs/AI_DEVELOPMENT_WORKFLOW.md`
- `docs/AI_SESSION_LOG.md`
- `docs/AI_TEAM_HANDOFF.md`
- `docs/TAILSCALE.md`
- `docs/WINDOWS_STEAMVR_HANDOFF.md`
- `docs/ROADMAP_INDEX.md`
- `assets/`
- `booth/BOOTH_DESCRIPTION.md`
- `booth/screenshots/`
- `scripts/build-release.sh`
- `winres/winres.json`
- `cmd/imagepadserver/main.go`
- `cmd/imagepadserver/rsrc_windows_amd64.syso`
- `internal/about/about.go`
- `internal/app/app.go`
- `internal/appicon/icon.go`
- `internal/appwindow/window_windows.go`
- `internal/appwindow/window_unsupported.go`
- `internal/browser/browser.go`
- `internal/clipboard/clipboard_windows.go`
- `internal/clipboard/clipboard_unsupported.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/imageproc/processor.go`
- `internal/imageproc/processor_test.go`
- `internal/library/store.go`
- `internal/library/store_test.go`
- `internal/network/lan.go`
- `internal/network/lan_test.go`
- `internal/server/server.go`
- `internal/server/ui.go`
- `internal/server/server_test.go`
- `internal/server/upload_url_test.go`
- `internal/settings/settings.go`
- `internal/settings/settings_test.go`
- `internal/steamvr/registration_windows.go`
- `internal/steamvr/registration_unsupported.go`
- `internal/steamvr/steamvr_windows.go`
- `internal/steamvr/steamvr_unsupported.go`
- `internal/tray/tray_windows.go`
- `internal/tray/tray_unsupported.go`
- `internal/tunnel/tunnel.go`
- `internal/tunnel/hide_windows.go`
- `internal/tunnel/hide_unsupported.go`
- `internal/upnp/upnp.go`
- `internal/video/publisher.go`
- `internal/video/hide_windows.go`
- `internal/video/hide_unsupported.go`
