# FFmpeg 取得元の高速化 & Web からのアプリ終了ボタン — 設計

- Date: 2026-06-22
- Status: Draft (awaiting review)
- Scope: 2 つの独立した機能を 1 つの実装サイクルで扱う

---

## 機能 1: FFmpeg ダウンロード速度の改善

### 背景 / 問題

Windows の FFmpeg 取得は現在、主ソースが `gyan.dev` の
`ffmpeg-release-essentials.zip`(+ `.sha256` サイドカー)、ミラーが BtbN の
GitHub リリース資産という順序になっている。

- 定義: `internal/video/toolchain.go:25-26`、`internal/video/tool_sources.go:18-27`
- 問題: 日本からの初回インストール時、主ソース `gyan.dev` のダウンロードが
  遅く・詰まる。`gyan.dev` は実質単一オリジン寄りで地理的に不利。さらに主ソースは
  ダウンロード前に sha256 を**同期取得**するため gyan.dev へ最低 2 往復する
  (`toolchain.go:349-357`)。

### 着想: essentials を保ったまま GitHub CDN へ

gyan の essentials ビルドは **gyan 本人(GyanD)の GitHub リポジトリ
`GyanD/codexffmpeg` にも同一ファイルがミラー**されている。これを使えば、
ビルド種別(= 対応コーデック/フォーマット)を一切変えずに、配信元だけを
速い GitHub の CDN へ移せる。

#### 実測(2026-06-22 時点)

| 取得先 | ファイル | 実サイズ | 備考 |
|---|---|---|---|
| gyan.dev(現・主) | `ffmpeg-release-essentials.zip` | 109,282,242 B ≈ 104.2 MiB | 単一オリジン寄り・遅い |
| GitHub GyanD(改訂・主) | `8.1.1/ffmpeg-8.1.1-essentials_build.zip` | 109,282,242 B ≈ 104.2 MiB | **同一ファイル**(HEAD 200 確認)・GitHub CDN |
| GitHub GyanD `.7z` | `…essentials_build.7z` | 33,664,824 B ≈ 32.1 MiB | 1/3 サイズだが 7z 解凍は Go 標準外。今回見送り |

- 改訂後の主ソースは**現在ユーザーが使っているファイルとバイト一致**(同 publisher・
  同サイズ)。よって対応フォーマット/コーデックは**現状から一切変化しない**。
  アプリが依存する libx264(H.264)・ネイティブ AAC・libass/libfreetype/fontconfig
  (ビジュアライザの drawtext/ASS)はすべて essentials に同梱済み。
- 版なしの「latest」エイリアス(`releases/latest/download/<versionless>`)は GyanD では
  404。よって**版番号を固定**する必要がある(下記)。
- 104.2 MiB は現行の 160 MiB 上限に十分収まるため、**サイズ上限の変更は不要**。

### 決定(スコープ: 最小 = 取得元の差し替え)

1. **主ソースを GitHub の GyanD 版固定 essentials に差し替え**、`gyan.dev` essentials を
   フォールバックに残す。BtbN は heavy で上限ギリギリのため**リストから外す**
   (独立ミラーが必要になれば将来、上限引き上げとセットで復帰)。

2. **`toolSource` に個別 `checksum` フィールドを追加**。GyanD GitHub には `.sha256`
   サイドカーが無い(404 実測)ため、主ソースのハッシュはコードに直書きし、版固定と
   セットで管理する。現状の全ソース共通既定 `ffmpegDownloadSHA256` を直書きすると
   gyan.dev フォールバックが将来の版ズレで検証失敗するため、ソース個別に持たせて
   独立させる。

```go
type toolSource struct {
    url         string
    checksum    string // inline sha256 (sidecar が無いソース用)
    checksumURL string
}

func ffmpegWindowsSources() []toolSource {
    return []toolSource{
        // 主: GyanD GitHub mirror, 版固定 essentials (GitHub CDN, fast from JP).
        // gyan.dev と同一ファイル。直書き sha256 で検証。
        {url: ffmpegGitHubURL, checksum: ffmpegGitHubSHA256},
        // フォールバック: gyan.dev release-essentials (sidecar sha256)。
        {url: ffmpegDownloadURL, checksumURL: ffmpegSHA256URL},
    }
}
```

3. **`attempt` のチェックサム優先順位**を `env(IMAGEPAD_FFMPEG_SHA256) → src.checksum
   → src.checksumURL 取得` に整理する(テスト用 env 上書きを最優先のまま維持)。
   `ffmpegDownloadSHA256` を全ソース共通既定として使う現挙動は廃し、上記に置き換える。

4. **新規定数**(`toolchain.go`):
   - `ffmpegGitHubURL = "https://github.com/GyanD/codexffmpeg/releases/download/8.1.1/ffmpeg-8.1.1-essentials_build.zip"`
   - `ffmpegGitHubSHA256 = "<8.1.1 essentials zip の sha256>"`
   - 直書き値は実装時に当該 zip を実ダウンロードして算出・確定する
     (gyan.dev の現サイドカー `6f58ce889f59c311410f7d2b18895b33c03456463486f3b1ebc93d97a0f54541`
     と一致するはず。一致を実装時に検証する)。

### この変更で守ること / 影響範囲

- **検証強度は維持**。主・フォールバックともに sha256 検証付き(主=直書き、
  フォールバック=サイドカー)。さらに extract 後 `validateExecutable(ffmpeg, "-version")`。
- **happy path で gyan.dev を一切叩かない**(同期チェックサム往復が消える)。これが
  速度改善の本丸。
- **zip レイアウト**: GyanD essentials も従来どおり `bin/` 配下。`extractNamedBinaryFromZip`
  は basename 照合でレイアウト非依存。ffprobe.exe も同 zip から抽出される
  (`extractFFmpegZip`, `toolchain.go:736-748`)。
- **更新運用**: 新しい FFmpeg へ上げる時は `ffmpegGitHubURL` の版番号と
  `ffmpegGitHubSHA256` を**セットで bump**する(リポジトリの pin 方針と整合)。
- **darwin / linux**: 変更しない。本件は Windows の `gyan.dev` 起因のため。
- `acquireFromSources` のリトライ/バックオフ、`maxBytes`(160MiB)は**変更しない**。
- HTTP クライアントの粒度タイムアウト不足(`toolchain.go:541,592` が
  `Timeout: 5*time.Minute` のみで Transport 未設定)は実在課題だが今回スコープ外。
  将来のハードニング候補として記録に残す。

### テスト

- 既存の `internal/video` テストが緑であること(`bundled-only-tools`方針により、
  実ダウンロードを誘発しないよう `IMAGEPAD_*` のピン留めは維持)。
- `ffmpegWindowsSources()` の先頭要素が GitHub(github.com を含む URL)で、かつ
  `checksum` が非空であることを確認する単体テスト(順序・検証欠落の回帰防止)。
- フォールバック要素が従来どおり `checksumURL` を持つことを確認。
- env(`IMAGEPAD_FFMPEG_SHA256`)上書きが引き続き最優先になることのテスト。

---

## 機能 2: Web からのアプリ終了ボタン

### 背景 / 問題

現在アプリの終了はトレイの Exit、または OS シグナル経由でのみ可能
(`internal/app/app.go:131-206`、`trayExit` チャネルを閉じると graceful shutdown が
走る)。Web 管理画面からは終了できない。

### 決定

#### 到達範囲 / 認証

`POST /api/quit` を **`s.admin(...)` 配下**で新設する。これにより到達範囲は
既存の管理 API(`/api/clear` 等)と同一:

- localhost(ループバック + ローカルホスト名)、または
- LAN のプライベート IP かつ有効な管理トークン保持者

(`internal/server/auth.go:10-18`)。

#### サーバ側の配線

`SetTunnelReconnect` と同じ流儀で、アプリ終了をトリガーするコールバックを
サーバへ注入する。

- `Server` に `exitRequested func()` フィールドを追加。
- `func (s *Server) SetExitRequested(fn func())` を追加(`mu` で保護)。
- `app.go` の `run()` で `srv.SetExitRequested(trayExitRequested)` を呼ぶ
  (`trayExitRequested` は既存。`trayExit` を一度だけ閉じる `sync.Once` ラッパ)。
- ハンドラ `handleQuit`:
  - `POST` 以外は 405。
  - `exitRequested == nil` なら 503(トンネル再接続ハンドラと同じガード)。
  - 先に `{"ok":true,"message":"アプリを終了します"}` を JSON で返す。
  - レスポンス送出後、`go func(){ time.Sleep(200ms); exitRequested() }()` で
    終了を起動。これにより既存の graceful shutdown 経路(OBS停止・追跡中FFmpeg
    クリーンアップ・トンネル停止・`httpServer.Shutdown`)がそのまま走る。
- `Register` に `mux.HandleFunc("/api/quit", s.admin(s.handleQuit))` を追加。

#### UI

終了ボタンを **独立した grid-area `quit`** として追加する。これにより
「デスクトップは履歴の真下」「モバイルは最下部」を両立する。

- デスクトップ(`main` グリッド, `ui.go:40-49`):
  ```
  grid-template-areas:
    "sidebar content history"
    "sidebar content quit";
  ```
  → `quit` が history カラムの真下に入り、sidebar/content は 2 行ぶん高くなる。
- モバイル(`@media (max-width: 860px)`, `ui.go:757-764`):
  ```
  grid-template-areas:
    "content"
    "history"
    "sidebar"
    "quit";
  ```
  → `quit` が最下部。
- 見た目: 赤い独立ボタン。全幅ブロック、周囲から視覚的に分離(`danger` 系の赤背景
  + 白文字)。新規 CSS クラス(例 `.quit-button`)を追加。
- 挙動: クリックで確認ダイアログ(`confirm('アプリを終了しますか？')`)。OK なら
  `POST /api/quit` を送信。成功後はボタン領域を「終了しました（この画面は閉じて
  かまいません）」表示に置き換える(サーバ停止で以後の通信は失敗するため)。
- 既存の fetch ヘルパ/トークン付与の流儀(`/api/clear` 等の呼び出し)に合わせる。

### テスト

- `POST /api/quit` で注入したコールバックが呼ばれること(同期的に検証できるよう、
  ハンドラはコールバック起動を内部関数経由にし、テストでは遅延なしで検証)。
- `exitRequested` 未設定時に 503 を返すこと。
- `POST` 以外で 405。
- admin ガードは既存ミドルウェアで担保(非 admin は 403)。必要なら回帰用に
  1 ケース追加。

---

## 非対象(YAGNI / 今回スコープ外)

- HTTP クライアントへの粒度タイムアウト追加、リトライ/バックオフの調整
  (ユーザー判断で「最小スコープ」に決定。将来のハードニング候補として記録済み)。
- `.7z` essentials(32MiB)への切替(7z 解凍は Go 標準外=依存追加のため見送り)。
- BtbN を独立ミラーとして併用すること(heavy で 160MiB 上限ギリギリのため今回は外す。
  併用するなら `maxBytes` 引き上げとセットで将来検討)。
- 自リポジトリ Release への ffmpeg 自前ホスト(案 B)、並列レースダウンロード(案 C)。
- 終了ボタンの localhost 限定化(ユーザー判断で admin 範囲に決定済み)。
- 再起動ボタン等、終了以外のライフサイクル操作。

## 影響ファイル(見込み)

- `internal/video/tool_sources.go`(`toolSource.checksum` 追加、`ffmpegWindowsSources()` で
  主ソースを GyanD GitHub へ・BtbN 削除)+ 同テスト
- `internal/video/toolchain.go`(`ffmpegGitHubURL` / `ffmpegGitHubSHA256` 定数追加、
  `EnsureFFmpeg` 内 `attempt` のチェックサム優先順位を env→src.checksum→checksumURL に整理)
- `internal/server/server.go`(`exitRequested`、`SetExitRequested`、`handleQuit`、`Register`)
- `internal/app/app.go`(`SetExitRequested` 呼び出し)
- `internal/server/ui.go`(CSS grid-area `quit` + ボタン + JS)
- サーバ側ハンドラのテスト
