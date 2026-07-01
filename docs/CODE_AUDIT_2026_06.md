# コード品質監査レポート — 2026-06-24

確認のみ（作業なし）。全項目に優先度を付与。

---

## 1. 肥大化（単一ファイルの巨大化）

| ファイル | 行数 | 問題 |
|---|---|---|
| `internal/server/server.go` | 2480行 | HTTPハンドラ約35本＋ビジネスロジック＋状態組み立て＋キュー管理＋ユーティリティが全混在。最大の問題。 |
| `internal/video/publisher.go` | 1306行 | キュー管理・品質計算・HLS変換・ネットワーク計測・URLダウンロード・サムネイル生成・ファイル管理が全混在。 |
| `internal/video/toolchain.go` | 1220行 | ツールパス解決・ダウンロード・SHA256検証・バージョン管理・zip展開・汎用execラッパーが全混在。 |
| `internal/video/visualizer_background.go` | 1073行 | 色分析・コントラスト計算・シャドウ描画・PNG I/O・画像スケーリングが1ファイルに集中。 |
| `internal/video/audio_analysis.go` | 846行 | DSP分析・スペクトラム・BPM・LUFS抽出を内包。 |
| `internal/video/audio_visualizer.go` | 682行 | FFmpegコマンド組み立て＋フレーム描画が混在。 |

`internal/video/` パッケージ全体（77ファイル）も実質5〜6ドメインの集合体：音声分析 / ビジュアライザー描画 / ツールインストール / SoundCloud API / 動画エンコード / パブリッシャー

---

## 2. コードの重複

### clamp系関数が4箇所に分散

| 関数 | ファイル |
|---|---|
| `clampByte(v float64) byte` | `video/audio_visualizer.go:96` |
| `clamp01(v float64) float64` | `video/visualizer_color.go:268` |
| `clamp01f64(v, limit float64) float64` | `video/visualizer_loudness.go:284` |
| `clampInt(v, min, max int) int` | `video/content_adaptive.go:234` |

`clamp01` と `clamp01f64` は limit=1 のとき完全に同一。

### srgbLuminance の同ファイル内重複（`visualizer_background.go`）

- `srgbLuminance(c color.RGBA) float64`（L483）
- `srgbLuminance8(r, g, b float64) float64`（L634）

どちらも `srgbLinearize(value/255)` × 3 → 輝度係数適用で等価。`srgbLuminance8` は不要。

---

## 3. Publish/Queue の全面的二重化（最大の構造問題）

「現在に公開する」vs「履歴キューに追加する」の分岐を**関数名**で分けたため、全処理パスが二重化されている。

| Publish 版 | Queue 版 | 実質的な差分 |
|---|---|---|
| `handleUploadURL`（L333） | `handleUploadURLQueue`（L502） | 約150行のダウンロードルーティングが逐語的重複 |
| `processAndPublish` | `processAndQueue` | 同一構造 |
| `processVideoAndPublish` | `processVideoAndQueue` | ファイル名prefix（`source-` vs `queued-source-`）のみ |
| `processVideoFileAndPublish` | `processVideoFileAndQueue` | `SetCurrentInfo` vs `AddHistory` のみ |
| `processAudioFileAndPublish` | `processAudioFileAndQueue` | 同上（`audio_ingest.go`） |

`handleUploadURL` vs `handleUploadURLQueue` の重複内容：SoundCloud判定 → musicMode判定 → yt-dlp → 直接DL → メディア種別switch のルーティング全体。

---

## 4. デッドコード（計4件）

| 関数 | ファイル | 状況 |
|---|---|---|
| `processSoundCloudFileAndPublish` | `server.go:1340` | どこからも呼ばれていない |
| `processSoundCloudFileAndQueue` | `server.go:1373` | どこからも呼ばれていない |
| `EnqueueSoundCloudForID` | `video/publisher.go:440` | どこからも呼ばれていない |
| `DownloadURL` | `video/publisher.go:898` | コメントに「compatibility wrapper」とあり、`internal/` 内の呼び出し元ゼロ |

SoundCloud変換は現在 `acquireDownloadedSoundCloud` → `processAudioFileAndPublish/Queue` → `EnqueueAudioForID` 経由に統合済み。旧経路が4関数分残骸として存在。

---

## 5. コピペ起源のミス

`audio_ingest.go` の `processAudioFileAndQueue` 内：
- 変数名が `ffmpegPath2`（`processAudioFileAndPublish` の `ffmpegPath` をコピーして名前変更し忘れた痕跡）
- `EnsureFFmpeg()` を関数内で2回呼んでいる（冒頭の `_` 捨てと途中の `ffmpegPath2`）

---

## 6. ルート認証の構造（確認済み）

**認証ティア:**

| ルート群 | 認証 | 評価 |
|---|---|---|
| 全 `/api/*` 書き込み系 | `admin()` — loopback OR（LAN IP + token） | 正常 |
| `/image/*`, `/video/*`, `/stream/*` | `publicReadAllowed()` — LAN IPのみ・token不要 | 正常（OBS向け） |
| `/api/pairing/*` | LAN IP + hostチェック（token不要） | 正常（ペアリング前はtokenが存在しない） |
| `/healthz`, `/favicon.ico` | 認証なし | 意図的・問題なし |

**`/stream/` の二重登録:**  
`/stream/current.m3u8` と `/stream/` の両方から `handleCurrentHLS` に到達できる。Go ServeMux は長いパス優先なので動作は正しいが、経路が2本ある。

---

## 7. セキュリティ・権限のグレーゾーン

### [高] リレーデバイスのスコープ漏れ

`/api/obs/relay-config` に `obs-relay` スコープのデバイスがアクセスすると `obsRelayConfig(startReceiver=true)` が呼ばれ：
1. `settings.VideoPlayerEnabled = true` を**永続保存**（設定書き換え）
2. OBS receiverを**起動**

リレーデバイスの認可スコープは「OBSストリーム受信」のみのはずで、グローバル設定の変更権限は持つべきでない。

### [中] `validatePublicURL` が `validateHTTPURL` と機能的に同一

`remote_upload.go:80-86` — 名前が「Public」を示唆するが実装は `validateRemoteHTTPURL` を呼ぶだけで同一。  
`downloadRemoteImage` が `validatePublicURL` を使い、`handleUploadURL/Queue` が `validateHTTPURL` を使う非対称も意図不明。

### [低] `randomSuffix()` が暗号的に予測可能

`server.go:2462` — `time.Now().UnixNano()` を36進数変換。一時ファイル名（`source-xxx.mp4`）に使用。  
`pairing.go` の `randomToken()` が `crypto/rand` を使うのと不統一。

### [記録のみ] `isSoundCloudURL` の意図的重複

```go
// duplicates video.isSoundCloudURL (unexported) to keep server.go self-contained
```
`video` パッケージ側でエクスポートすれば解消できる。

### [記録のみ] Tailscale レンジの非対称ブロック（意図的・正しい設計）

| 用途 | `100.64-127.*`（Tailscale CGNAT） |
|---|---|
| 管理者認証 `isAllowedAdminIP`（auth.go） | **許可** |
| SSRF防止 `isBlockedIP`（remote_upload.go） | **ブロック** |

Tailscaleデバイスを管理者として信頼しつつ、SSRFのフェッチ先としてはブロックする設計。一見矛盾に見えるが正しい非対称。

### [確認済み・問題なし] SSRF対策

`downloadRemoteImage`：URLを検証し、リダイレクトごとに `validatePublicURL` で再検証（`CheckRedirect` フック）。適切。

---

## 8. 環境変数の散在と重複読み取り

環境変数の一覧ファイルが存在しない。追加・リネーム時は全箇所を検索しないと更新漏れが起きる。

**重複読み取り（同一変数を複数箇所で独立して読む）:**

| 環境変数 | 読み取り箇所 |
|---|---|
| `IMAGEPAD_FFPROBE` | `toolchain.go:67`（ffprobePath）・`toolchain.go:102`（usableFFprobePath） |
| `IMAGEPAD_YTDLP` | `toolchain.go:221`（ytdlpPath）・`toolchain.go:350`（EnsureLatestYTDLP） |
| `IMAGEPAD_FFMPEG_SHA256` | `toolchain.go:410`（downloadFFmpeg）・`toolchain.go:530`（downloadDarwinFFmpeg） |
| `IMAGEPAD_YTDLP_SHA256` | `toolchain.go:483`・`toolchain.go:591` |
| `IMAGEPAD_CLOUDFLARED_SHA256` | `tunnel/tunnel.go:223`・`tunnel/tunnel.go:269` |

**ファイル別の担当変数:**

| ファイル | 変数 |
|---|---|
| `config/config.go` | `IMAGEPAD_HOST`, `PORT`, `ADVERTISE_HOST`, `PREFER_TAILSCALE` |
| `settings/settings.go` | `IMAGEPAD_DATA_DIR` |
| `video/toolchain.go` | `IMAGEPAD_FFMPEG`, `FFPROBE`, `YTDLP`, `FFMPEG_SHA256`, `YTDLP_SHA256` |
| `tunnel/tunnel.go` | `IMAGEPAD_CLOUDFLARED_SHA256` |
| `app/obs_relay.go` | `IMAGEPAD_ADMIN_TOKEN`, `RELAY_CLIENT_ID`, `RELAY_CLIENT_SECRET` |
| テストファイル各種 | `IMAGEPAD_RUN_NETWORK_TESTS`, `RUN_LOCAL_TOOL_INSTALL_TESTS`, `RUN_REAL_INSTALL`, `FFPROBE` |

---

## 9. 依存方向の逆転とパッケージ責務の問題

### `video` パッケージが `settings` を直接参照（依存方向の逆転）

`video/toolchain.go` と `video/process_registry.go` が `settings.Dir()` を直接呼ぶ。  
`server` は `store.Dir()` を引数で明示的に渡す呼び方もしている。  
→ 同じデータディレクトリへの参照が「引数経由」と「settings直接参照」の2経路で混在。

### `imageproc` と `obsrtmp` が `video` に依存してffmpegパスを取得

```
imageproc/processor.go  → video.EnsureFFmpeg()
obsrtmp/manager.go      → video.EnsureFFmpeg()
server/（7ファイル）    → video.EnsureFFmpeg() × 20箇所
```

ffmpegのパス解決は `video` パッケージが担うが、`imageproc`・`obsrtmp` はそのためだけに `video` に依存している。本来は独立した `toolchain` パッケージが担うべき責務。

### `settings.Load()` がリクエストごとにディスク読み取り（キャッシュなし）

`server.go` でリクエストのたびに独立してファイル読み取りを行うメソッド群：

| メソッド | 呼び出す関数 |
|---|---|
| `videoPlayerEnabled()` | `settings.Load()` |
| `musicModeEnabled()` | `settings.Load()` |
| `musicQualityPreset()` | `settings.Load()` |
| `videoQualityPreset()` | `settings.Load()` |
| `obsLatencyProfile()` | `settings.Load()` |

`/api/state` 1回で5回以上のディスクI/O。設定変更頻度は低いのにキャッシュを持っていない。

### `toolchain.go` の汎用execラッパー混入

`run()` / `runInDir()` / `runInDirContext()`（L1170-1200）はツール管理と無関係な汎用ヘルパーだが `toolchain.go` 末尾に置かれている。

### 更新漏れリスクのある分散実装

| 分散している実装 | 箇所 | リスク |
|---|---|---|
| `IMAGEPAD_FFPROBE` 読み取りロジック | `ffprobePath` と `usableFFprobePath` | 片方だけ直す更新漏れ |
| `IMAGEPAD_YTDLP` 読み取りロジック | `ytdlpPath` と `EnsureLatestYTDLP` | 同上 |
| `IMAGEPAD_FFMPEG_SHA256` 読み取り | `downloadFFmpeg` と `downloadDarwinFFmpeg` | チェックサム検証挙動の食い違い |
| ffmpegパス取得 | 7ファイルが `EnsureFFmpeg()` を独立して呼ぶ | ツール切り替え時に全箇所変更が必要 |

---

## 10. ランタイム品質の問題

### `watchConversion` ゴルーチンが最大6時間滞留する

```go
ticker := time.NewTicker(500 * time.Millisecond)
deadline := time.After(6 * time.Hour)
```

キャンセルのトリガーはジョブの `"done"` / `"error"` / `"canceled"` ステータスの観測のみ。  
しかし `queueState.pruneLocked` でキューから刈り取られたジョブは二度と検索に引っかからず、ゴルーチンは6時間のdeadlineまで500msごとに空振りし続ける。  
エンキューのたびにゴルーチンが1本追加されるため、キューを多用するほど滞留が積み上がる。

### `handleIndex` と `handleState` がHTTPメソッドを検査しない

35ハンドラ中19本はメソッドチェックあり。なしの2本：
- `handleIndex`（L215）— GET/POST/DELETE など何でも通る
- `handleState`（L225）— 同上

`admin()` ガードで実害は抑えられているが意味論的に誤り。

### `handleState` だけ panic リカバリを持つ

```go
defer func() {
    if recovered := recover(); recovered != nil {
        log.Printf("handleState panic: %v", recovered)
```

35ハンドラ中ここだけ `recover()`。`s.state(r)` がパニックしうることを認識しながら根本を直さずに封じ込めている。

### `settings.Update` エラーの無声廃棄

`_ = settings.Update(...)` が4箇所で使われ、設定ファイルのロックや破損時に静かに失敗する：

| 箇所 | 内容 | 問題度 |
|---|---|---|
| `pairing.go:204` | リレーデバイスの `LastSeenAt` 更新 | 監査ログとして機能しない |
| `server.go:1674` | OBSレイテンシ更新 | 設定が保存されない |
| `tool_install.go:66,77` | 失敗時ロールバック | best-effort なので許容範囲 |

---

---

## 11. `library/store.go` — お気に入り・履歴ストレージの問題

### [中] `saveCurrentLocked` が非アトミック（クラッシュでstate.json破損）

```go
// saveCurrentLocked — 直接書き込み（破損リスクあり）
ioutil.WriteFile(filepath.Join(s.dir, "state.json"), data, 0600)

// saveFavoritesLocked — tmpファイル+Rename（アトミック・正しい）
os.Rename(tmpPath, filepath.Join(s.favoriteDir, "favorites.json"))
```

同じファイル内でお気に入りはアトミック保存、現在状態は非アトミック。書き込み中にクラッシュすると `state.json` が破損し、次回起動時に状態が読めなくなる。

### [中] unfavorite の暗黙的履歴削除（UIと挙動が乖離）

`SetFavorite(id, false)` の内部（L318-320）:
```go
if _, err := os.Stat(filepath.Join(s.dir, s.history[i].HistoryFileName)); err != nil {
    s.history = append(s.history[:i], s.history[i+1:]...)
}
```

お気に入りに追加後に通常の履歴ファイルが `pruneHistoryLocked` で刈り取られた場合、unfavorite 操作が「★を外す」だけでなく**履歴からの完全削除**になる。ユーザーには「消えた」と見える。

### [低] `pruneHistoryLocked` でサムネイルを削除しない（ディスクリーク）

```go
_ = os.Remove(filepath.Join(s.dir, item.HistoryFileName)) // 本体のみ
// thumb-{id}.jpg が残り続ける
```

履歴が40件を超えるたびに `thumb-{id}.jpg` がディスクに蓄積する。

### [低] `SetCurrent` が `addHistoryLocked` エラーを捨てる

```go
_ = s.addHistoryLocked(info, dstPath) // L162
```

ディスクフル等でコピーに失敗しても `SetCurrent` は成功を返す。

### [低] `io/ioutil` deprecated

`L8, L428, L540` — Go 1.16 で非推奨。同ファイル内で `os.WriteFile`（`saveFavoritesLocked`）と混在。

---

## 12. `imageproc/processor.go` — 初期実装の課題

### [低] 手書きEXIFパーサー

`exifOrientation`（L306）・`tiffOrientation`（L340）が独自バイナリパーサー。壊れた/非標準EXIFへの耐性は実装次第で、ライブラリ化されていない。

### [中] PNG が MaxBytes を超えたら即エラー（JPEGは品質を落として再試行する非対称）

```go
// JPEG — encodeJPEGWithinLimit で品質を二分探索して MaxBytes に収める
encoded, err := encodeJPEGWithinLimit(flatten(resized), opts.JPEGQuality, opts.MaxBytes)

// PNG — encode してからチェックするだけ、超えたらエラー
png.Encode(&buf, resized)
if int64(len(data)) > opts.MaxBytes { return error }
```

デフォルト MaxBytes = 30 MB・MaxDimension = 2048px なので実際に超えることは稀だが、PNGには圧縮削減のフォールバックが存在しない。

### PNG 圧縮改善の選択肢（調査結果）

ImageOptim は macOS アプリで pngcrush → pngquant → zopflipng → advpng を連鎖実行するラッパー。サーバー用には個別ツールを選択する。

| 方法 | 削減率 | 損失 | 依存 | 難易度 |
|---|---|---|---|---|
| `png.Encoder{CompressionLevel: png.BestCompression}` | 5〜15% | なし | ゼロ（標準ライブラリ） | 1行変更 |
| MaxBytes超過時にJPEGへフォールバック | 大 | 透過消失 | ゼロ | 10行程度 |
| **pngquant** バンドル | 60〜80% | **あり（パレット化）** | バイナリ追加 | ffmpegと同方式 |
| **oxipng** バンドル（Rust製） | 10〜30% | なし | バイナリ追加 | ffmpegと同方式 |
| zopflipng | 10〜20% | なし | バイナリ追加 | 非常に低速 |

**推奨:**

1. **即時（1行）**: `png.Encode(&buf, resized)` → `png.Encoder{CompressionLevel: png.BestCompression}.Encode(&buf, resized)` に変更。ゼロコストで5〜15%削減。
2. **PNG が透過不要な用途が多い場合**: MaxBytes超過時にJPEGでリトライするフォールバックを追加。
3. **透過を保ちつつ大幅削減したい場合**: oxipng（無損失）のバンドルを検討。pngquant（有損失）はロゴ・シンプルなグラフィックには有効だが写真には不向き。

純Go製の無損失PNGオプティマイザは主要なエコシステムには存在しない（2026年現在）。

---

## 優先度まとめ

| 優先度 | 項目 |
|---|---|
| **高** | リレーデバイスのスコープ漏れ（設定を永続変更できる） |
| **高** | `handleUploadURL` / `handleUploadURLQueue` の150行重複（更新漏れ発生源） |
| **中** | デッドコード4関数（混乱源・今すぐ削除可） |
| **中** | `watchConversion` ゴルーチン滞留 |
| **中** | `settings.Load()` のキャッシュなし |
| **中** | unfavorite の暗黙的履歴削除（UIと挙動の乖離） |
| **中** | `saveCurrentLocked` の非アトミック書き込み |
| **中** | PNG の MaxBytes フォールバックなし |
| **中** | `video` → `settings` 依存方向の逆転 |
| **中** | `publisher.go` / `toolchain.go` の肥大化 |
| **低** | `pruneHistoryLocked` サムネイル削除漏れ |
| **低** | clamp系重複・`srgbLuminance8` 重複 |
| **低** | `handleState` のpanicリカバリ |
| **低** | `randomSuffix()` の非暗号的ランダム性 |
| **低** | `io/ioutil` deprecated（store.go） |
| **低** | 手書きEXIFパーサー |
| **低** | メソッドチェックなしハンドラ2本 |
| **記録のみ** | `isSoundCloudURL` 意図的重複・Tailscale非対称・`validatePublicURL` 名前の乖離 |
