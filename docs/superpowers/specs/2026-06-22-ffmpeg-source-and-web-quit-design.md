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
  遅く・詰まることがある。`gyan.dev` は実質単一オリジン寄りで地理的に不利。
- 一方 BtbN は GitHub リリース資産(Fastly CDN・日本にエッジあり)で、一般に
  日本からは速く安定する。yt-dlp は既に GitHub 直取得で問題が出ていない。

### 決定: ソース順の入れ替え(案 A)

`ffmpegWindowsSources()` の優先順位を入れ替え、**GitHub(BtbN)を主**、
`gyan.dev` をフォールバックにする。

```go
func ffmpegWindowsSources() []toolSource {
    return []toolSource{
        // 主: BtbN nightly win64 build (GitHub / Fastly CDN, fast from JP).
        // sidecar checksum なし → 既存どおり ffmpeg -version で検証。
        {url: "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip"},
        // フォールバック: gyan.dev release-essentials (sha256 検証付き)。
        {url: ffmpegDownloadURL, checksumURL: ffmpegSHA256URL},
    }
}
```

### この変更で守ること / 影響範囲

- **検証**: BtbN は sha256 サイドカーを持たないが、これは既存ミラーと同じ扱い。
  抽出後の `validateExecutable(ffmpeg, "-version")` で実行検証されるため、検証の
  強度は現状から低下しない(主ソースが検証付き sha256 → 実行検証に変わる点のみ
  許容する。今回のゴールは「速度」であり、検証強化は別スコープ)。
- **zip レイアウト**: BtbN の zip は `bin/` 配下に格納するが、
  `extractNamedBinaryFromZip` は basename 照合なのでレイアウト非依存
  (`tool_sources.go:22-24` のコメントどおり)。ffprobe.exe も同 zip から抽出される
  (`extractFFmpegZip`, `toolchain.go:736-748`)ため両方そろう。
- **再現性**: BtbN master はローリングビルド。今回のゴール(速度)では許容する。
  将来、再現性を重視する場合は「自リポジトリ Release への自前ホスト+ピン留め」
  (今回見送った案 B)を別途検討する。
- **darwin / linux**: 変更しない。本件は Windows の `gyan.dev` 起因のため。
- `acquireFromSources` のリトライ/バックオフ機構は変更しない。順序のみ変える。

### テスト

- 既存の `internal/video` テストが緑であること(`bundled-only-tools`方針により、
  実ダウンロードを誘発しないよう `IMAGEPAD_*` のピン留めは維持)。
- `ffmpegWindowsSources()` の先頭要素が BtbN(github.com を含む URL)であることを
  確認する単体テストを追加する(順序の回帰防止)。

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

## 非対象(YAGNI)

- FFmpeg のバージョンピン留め・自前ホスト(案 B)、並列レースダウンロード(案 C)。
- 終了ボタンの localhost 限定化(ユーザー判断で admin 範囲に決定済み)。
- 再起動ボタン等、終了以外のライフサイクル操作。

## 影響ファイル(見込み)

- `internal/video/tool_sources.go`(順序入替)+ 同テスト
- `internal/server/server.go`(`exitRequested`、`SetExitRequested`、`handleQuit`、`Register`)
- `internal/app/app.go`(`SetExitRequested` 呼び出し)
- `internal/server/ui.go`(CSS grid-area + ボタン + JS)
- サーバ側ハンドラのテスト
