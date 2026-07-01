# ロードマップ: v1.4.6 → v1.5.0

起点: v1.4.6（2026-06-24現在）
ゴール: v1.5.0（画像フォーマット拡張 + 品質改善 + バグ修正の完結）

バグ出典: `docs/CODE_AUDIT_2026_06.md`
機能出典: `docs/PLAN_IMAGE_FORMAT_SETTINGS.md` / `docs/PLAN_PNG_OPTIMIZE.md`

---

## v1.4.7 — セキュリティ・権限修正

**根拠:** 優先度「高」の権限問題と、低コストで直せるセキュリティ改善をまとめて処理する。

### 修正内容

| # | 対象 | 内容 |
|---|---|---|
| 1 | `server.go` | **リレーデバイスのスコープ漏れ** — `/api/obs/relay-config` で `obs-relay` スコープデバイスが `settings.VideoPlayerEnabled = true` を永続保存できる問題を修正。relay スコープは受信起動のみに限定し、設定変更は admin 認証を要求する |
| 2 | `server.go:2462` | **`randomSuffix()` を `crypto/rand` に変更** — 現状は `time.Now().UnixNano()` で暗号的に予測可能。`pairing.go` の `randomToken()` と実装を統一 |
| 3 | `server.go:215,225` | **`handleIndex` / `handleState` にメソッドチェック追加** — GET 以外の HTTP メソッドを 405 で弾く |

---

## v1.4.8 — デッドコード・コピペバグ除去

**根拠:** デッドコードは読者を混乱させ、将来の変更時に「呼ばれているかも」という誤読を生む。コストゼロで削除できる。

### 修正内容

| # | 対象 | 内容 |
|---|---|---|
| 1 | `server.go:1340,1373` | **`processSoundCloudFileAndPublish` / `Queue` を削除** — SoundCloud 処理は `processAudioFileAndPublish/Queue` 経由に統合済み |
| 2 | `video/publisher.go:440` | **`EnqueueSoundCloudForID` を削除** — `EnqueueAudioForID` に統合済み |
| 3 | `video/publisher.go:898` | **`DownloadURL` を削除** — コメント自体に「compatibility wrapper」とあり、`internal/` 内の呼び出し元ゼロ |
| 4 | `server/audio_ingest.go` | **`ffmpegPath2` 変数名修正 + `EnsureFFmpeg()` 二重呼び出し除去** — コピペ起源のバグ |
| 5 | `server.go` / `video/` | **`isSoundCloudURL` の重複解消** — `video` パッケージ側でエクスポートし、`server.go` の再実装を削除 |
| 6 | `library/store.go` | **`io/ioutil` を `os` に置き換え** — `ioutil.WriteFile` → `os.WriteFile`、`ioutil.ReadFile` → `os.ReadFile` |

---

## v1.4.9 — ストレージ・履歴の安定性

**根拠:** `state.json` の破損リスクとお気に入り操作の暗黙的副作用はユーザーデータに直接影響する。

### 修正内容

| # | 対象 | 内容 |
|---|---|---|
| 1 | `library/store.go:423` | **`saveCurrentLocked` をアトミック書き込みに変更** — `ioutil.WriteFile` の直接書き込みを `tmp + os.Rename` に変更。`saveFavoritesLocked` と実装を統一 |
| 2 | `library/store.go:274` | **`SetFavorite` の unfavorite 暗黙削除を修正** — unfavorite 時にファイルが history ディレクトリに存在しない場合に履歴から消える挙動を修正。削除は `ClearHistory` などの明示的操作にのみ行う |
| 3 | `library/store.go:462` | **`pruneHistoryLocked` でサムネイルも削除** — 本体ファイルを `os.Remove` する際に対応する `thumb-{id}.jpg` も削除 |
| 4 | `library/store.go:162` | **`SetCurrent` の `addHistoryLocked` エラーを捨てない** — `_ = s.addHistoryLocked(...)` をエラー返却に変更 |

---

## v1.4.10 — ランタイム品質

**根拠:** ゴルーチン滞留と毎リクエストのディスク I/O はロングラン時の挙動を劣化させる。

### 修正内容

| # | 対象 | 内容 |
|---|---|---|
| 1 | `server.go:1421` | **`watchConversion` にキャンセル対応を追加** — 外部から context をキャンセルできるようにし、ジョブが queue から pruned されたら最大 6 時間待たずに早期終了する |
| 2 | `server.go` | **`settings.Load()` のキャッシュ追加** — `videoPlayerEnabled()` / `musicModeEnabled()` / `musicQualityPreset()` / `videoQualityPreset()` / `obsLatencyProfile()` がリクエストごとにディスク読み取りしている問題を解消。`settings.Save` 時にキャッシュを無効化する |
| 3 | `server.go:225` | **`handleState` の `recover()` を除去して根本修正** — パニックが起きうる `s.state(r)` の原因を特定して修正し、防衛的 recover を削除 |
| 4 | `pairing.go:204` `server.go:1674` | **`settings.Update` エラーを記録** — `_ = settings.Update(...)` を `if err := ...; err != nil { log.Printf(...) }` に変更。特に `LastSeenAt` の更新失敗が無音になる問題を改善 |

---

## v1.4.11 — Publish/Queue 二重化の解消

**根拠:** 約 150 行のダウンロードルーティングが逐語的に二重化しており、片方だけ直す更新漏れが現実に起きうる。これが監査で指摘した最大の構造問題。

### 修正内容

| # | 対象 | 内容 |
|---|---|---|
| 1 | `server.go:333,502` | **`handleUploadURL` / `handleUploadURLQueue` を統合** — SoundCloud 判定 → musicMode → yt-dlp → 直接 DL → メディア種別 switch のルーティングを共通関数 `resolveUploadedMedia(r, reader, name) (mediaResult, error)` に抽出し、Publish/Queue どちらからも呼ぶ |
| 2 | `server.go` | **`processVideoAndPublish/Queue` を統合** — `dest` パラメータ（`"source-"` vs `"queued-source-"` のファイル名プレフィックス）を引数化 |
| 3 | `server.go` | **`processVideoFileAndPublish/Queue` を統合** — `SetCurrentInfo` vs `AddHistory` の分岐を引数化（`publish bool` か関数渡し） |
| 4 | `server/audio_ingest.go` | **`processAudioFileAndPublish/Queue` を統合** — 同上 |

---

## v1.4.12 — 画像フォーマット設定 UI 拡張

**根拠:** `PLAN_IMAGE_FORMAT_SETTINGS.md` より。回線品質の悪い環境への対応と WebP 対応。

### 追加・変更内容

| # | 対象 | 内容 |
|---|---|---|
| 1 | `imageproc/processor.go` | **`Options` 構造体に `WebPQuality int` / `PNGQuality string` 追加**、`DefaultOptions()` のデフォルト形式を WebP に変更 |
| 2 | `imageproc/webp_encode.go` | **新規: `EncodeWebP(src, outPath, quality)`** — `flatten()` でアルファを黒背景合成 → 中間 PNG（非劣化）を一時ファイルへ → `ffmpeg -quality <q>` で WebP 出力 |
| 3 | `imageproc/processor.go` | **フォーマット分岐に `"webp"` 追加**、PNG を `BestCompression` エンコーダーに変更 |
| 4 | `server/server.go` | **品質プリセットマップ追加** — `qualityPresetToJPEG` / `qualityPresetToWebP` / `qualityPresetToPNGRange`、`optionsFromValues` を更新 |
| 5 | `server/ui.go` | **UI を `<select>` プリセットに変更** |
| | | 最大辺: 2048 / 1024 / 512 / 256 / 128 px |
| | | 形式: 非劣化(PNG) / 高品質(WebP, デフォルト) / 高圧縮(JPEG) |
| | | 品質: PNG=6段階(非劣化〜最低) / JPEG・WebP=5段階(最高〜最低) |
| | | 最大MB: 30 / 20 / 10 / 5 / 1 |
| 6 | `server/ui.go` | **JS: format 変更時に品質選択肢を動的差し替え**（PNG 選択時のみ「非劣化」が先頭に追加される） |
| 7 | `server/ui.go` | **`updateUploadControlsVisibility()` を追加** — `obsMode || state.videoPlayerEnabled` で `.controls` の表示を制御。`setUploadMode` と `applyVideoPlayer` の両方から呼ぶ |

---

## v1.4.13 — PNG 最適化パイプライン

**根拠:** `PLAN_PNG_OPTIMIZE.md` より。pngquant + oxipng で PNG を 70〜85% 削減。非劣化設定時は oxipng のみ実行。

### 追加内容

| # | 対象 | 内容 |
|---|---|---|
| 1 | `imageproc/tools.go` | **新規: `EnsurePngquant()` / `EnsureOxipng()`** — パス解決（env var → バンドル → PATH）、Windows は自動ダウンロード、macOS は PATH のみ |
| 2 | `imageproc/png_optimize.go` | **新規: `OptimizePNG(path, quality string)`** — quality="lossless" 時は oxipng のみ、それ以外は pngquant(`--quality=min-max`) → oxipng の二段パイプライン |
| 3 | `imageproc/processor.go` | **PNG 出力後に `OptimizePNG(path, opts.PNGQuality)` を呼ぶ** — ベストエフォート（失敗してもエラーにせず元ファイルを提供） |
| 4 | `server.go` 起動シーケンス | **`imageproc.ValidateImageTools()` を追加** — `video.ValidateInstalledTools()` と並列で起動時に pngquant / oxipng を事前取得 |
| 5 | 環境変数 | `IMAGEPAD_PNGQUANT` / `IMAGEPAD_OXIPNG` / `IMAGEPAD_PNGQUANT_SHA256` / `IMAGEPAD_OXIPNG_SHA256` を追加 |

---

## v1.5.0 — リリース

**前バージョンまでの内容を全て含むリリース。**

### リリース前チェックリスト

- [ ] `go test ./...` グリーン
- [ ] WebP 出力の手動確認（VRChat / OBS での表示）
- [ ] PNG 最適化の圧縮率確認（各品質プリセット）
- [ ] ビデオプレーヤーモード ON/OFF 時の UI 表示確認
- [ ] お気に入り操作（追加・解除・再選択）の動作確認
- [ ] `state.json` アトミック書き込みの確認（書き込み途中プロセス終了シミュレーション）
- [ ] `about.go` + `winres/winres.json` のバージョン更新
- [ ] Windows リソース再生成（`go-winres`）
- [ ] `chore: cut v1.5.0-devN` → `release: v1.5.0` コミット

---

## サマリー

| バージョン | テーマ | 監査優先度 |
|---|---|---|
| v1.4.7 | セキュリティ・権限修正 | 高 × 1、低 × 2 |
| v1.4.8 | デッドコード・コピペバグ除去 | 中 × 1、低 × 5 |
| v1.4.9 | ストレージ・履歴の安定性 | 中 × 3、低 × 1 |
| v1.4.10 | ランタイム品質 | 中 × 3、低 × 1 |
| v1.4.11 | Publish/Queue 二重化の解消 | 高 × 1 |
| v1.4.12 | 画像フォーマット設定 UI 拡張 | 新機能 |
| v1.4.13 | PNG 最適化パイプライン | 新機能 |
| v1.5.0 | リリース | — |

**スコープ外（v1.6 以降）:**
- `publisher.go` / `toolchain.go` の分割リファクタ（1000行超のファイル分割）
- `video` → `settings` 依存方向の逆転解消（独立した `toolchain` パッケージの新設）
- clamp 系重複・`srgbLuminance8` 重複の整理
- 手書き EXIF パーサーのライブラリ化

---

## v1.6 大まかなロードマップ — ミュージックモード拡張

v1.5 完了後の次フェーズ。ミュージックモードを独立した体験として作り直す。

### v1.5.x — 技術的負債の継続解消（v1.6 前の地均し）

v1.5 のスコープ外とした構造問題を先に解消し、ミュージック機能の土台を整える。

- `publisher.go` の分割（キュー管理 / 変換実行 / ネットワーク計測 / ダウンロード）
- `toolchain.go` の分割（パス解決 / インストール / 実行ヘルパー）
- `video` → `settings` 依存の逆転解消

---

### v1.6.0 — ミュージックモード専用 UI

**概要:** 現在ミュージックモードは通常の画像/動画アップロード UI に間借りしている。
音楽に特化した専用 UI タブとして独立させる。

**主な内容:**
- ミュージックモード専用タブ / パネル（現在の「画像アップロード」と分離）
- 楽曲情報表示（タイトル・アーティスト・アルバム・カバーアート）の強化
- 再生中の視覚的フィードバック（ビジュアライザー波形のプレビュー等）
- SoundCloud / URL / ローカルファイル の入力を専用 UI で統一
- 現在の再生位置・残り時間の表示（ffprobe 情報から）

---

### v1.6.1 — プレイリスト

**概要:** 現在は「1曲ずつキューに積む」のみ。複数曲を事前に並べて連続再生できるようにする。

#### UI 構成

**① リスト型プレイリスト UI**
- 現在のキューをリスト表示（曲名・アーティスト・サムネイル・追加元）
- ドラッグ＆ドロップまたは上下ボタンで順番変更
- 各行に削除ボタン・割り込み再生ボタン
- 再生中の曲をハイライト表示

**② 統合入力 UI（リンク・ローカルファイルを区別しない）**
- 入力欄ひとつで URL / ローカルファイルパス / ファイルドロップ のいずれも受け付ける
- バックエンド側で入力種別を自動判定（URL → yt-dlp、ローカル → 直接エンキュー）
- 現在の「ファイルタブ / リンクタブ」の切り替えを廃止してひとつの入力に統合
- 「追加」ボタンで即座にプレイリスト末尾へ追加（再生は現在曲が終わった後）
- 「今すぐ再生」ボタンで割り込み再生

#### その他の機能
- 曲の自動連続再生（現在曲が終わったら次を自動公開）
- シャッフル / ループ制御
- プレイリストの保存・読み込み（JSON 形式、お気に入りと同じストレージ層）

---

### v1.6.2 — パーティーモード（外部からの楽曲リクエスト機能）

> **前提条件: v1.6.1（プレイリスト）が完了していること。**
> ゲストのリクエストはプレイリストのキューに積まれるため、プレイリスト機能なしには成立しない。

**概要:** VRChat ワールドに来たゲストがスマホ・PCから楽曲リクエストを送れる仕組み。
ホストの明示的な承認が必須で、接続元の信頼度をサーバーが自動判定して通知する。

---

#### アクセスフロー

```
[ゲスト]
  ↓ ①  4桁コード + ユーザー名を入力
[ImagePad 公式サイト（静的ページ）]
  ↓ ②  Cloudflare Tunnel 経由でサーバーにアクセス要求を送信
[ImagePadServer]
  ↓ ③  接続元 IP を動画アクセス履歴と照合
  ↓ ④  ホストに通知（信頼度ラベル付き）
[ホスト（サーバー管理 UI）]
  ↓ ⑤  承認 / 却下
[ゲスト]
  ↓ ⑥  承認された場合のみリクエスト専用ページへ遷移
```

---

#### 各コンポーネントの詳細

**① 英数字混合 4 文字コード**
- ホストがパーティーモードを ON にした時点でサーバーが生成
- 数字 + アルファベット混合 4 文字（36^4 ≒ 168 万通り）
- コードは有効期限付き（セッション中のみ有効）、期限切れ・OFF 時は自動無効化
- 既存の `pairing.go` の PIN 生成ロジックを流用・拡張

**② 接続経路: QR 直接 / 公式サイトのハイブリッド**

パーティーモードを ON にした時点でサーバーが同時に実施:
1. 英数字コードを生成
2. 公式バックエンドに `{コード → トンネル URL}` を TTL 付きで登録
3. 管理 UI に **QR コード** と **4 文字コード** を並べて表示

```
経路 A — QR スキャン（URL が直接埋め込まれている）
  [ゲスト がQRをスキャン]
    → Cloudflare Tunnel URL へ直接アクセス
    → サーバーがアクセス要求を受け取る（公式サイトを経由しない）

経路 B — 公式サイト経由（QR が使えない場合・テキストコード共有時）
  [ゲスト が imagepad.example.com/party にアクセス]
    → 英数字 4 文字コード + ユーザー名を入力
    → 公式サイトがコードを引いてトンネル URL を解決
    → ゲストのブラウザを Cloudflare Tunnel URL へリダイレクト
    → サーバーがアクセス要求を受け取る
```

いずれの経路でもサーバーへの到達後は同じフロー（③以降）。

**公式サービスの責務はルックアップのみ（GitHub Actions で実装）:**

専用リポジトリ（`imagepad-party-registry`）の JSON ファイルをストレージとして使い、
GitHub Actions が読み書きする。ホスティング費用ゼロ。

```
imagepad-party-registry/
  party.json                    ← コード → URL マッピング（raw.githubusercontent.com で配信）
  .github/workflows/
    register.yml                ← サーバーが ON 時に repository_dispatch でトリガー
    unregister.yml              ← サーバーが OFF 時にトリガー
    cleanup.yml                 ← 毎時実行、期限切れエントリを自動削除
```

`party.json` の構造:
```json
{
  "A4K2": {
    "url": "https://xxx.trycloudflare.com",
    "expires_at": "2026-06-24T22:00:00Z"
  }
}
```

各フロー:
```
[サーバー: パーティー ON]
  → GitHub API: repository_dispatch { event: "register", code: "A4K2", url: "...", ttl: 7200 }
  → register.yml 起動 → party.json を更新してコミット・プッシュ（30〜60秒）

[公式サイト: ゲストがコード入力]
  → raw.githubusercontent.com/.../party.json を fetch（静的、CDN 配信・即時）
  → コードでエントリを引いてトンネル URL へリダイレクト

[サーバー: パーティー OFF / コード期限切れ]
  → repository_dispatch { event: "unregister", code: "A4K2" }
  → unregister.yml 起動 → party.json からエントリを削除
```

制約と許容範囲:

| 項目 | 内容 |
|---|---|
| 登録の遅延 | workflow 起動 + commit で **30〜60 秒**。パーティー開始前に設定するため許容範囲 |
| lookup の速度 | `raw.githubusercontent.com` は CDN 配信で**ほぼ即時** |
| 認証 | サーバーは Fine-grained PAT（`Actions: write` のみ）を `settings.json` に保存 |
| コスト | **ゼロ**（GitHub Actions 無料枠で完結） |
| 同時書き込み | 複数サーバー同時登録でコンフリクトの可能性はあるが、想定規模では非問題 |

公式バックエンドは TTL 付き KV ストアとして機能するだけ。
アクセス要求・承認・トークン発行・リクエスト受付は**全てサーバーの責務**。

**③ IP と動画アクセス履歴の照合（信頼度判定）**
- サーバーは `/video/*` / `/stream/*` へのアクセス IP をメモリ上でトラッキング
- アクセス要求の送信元 IP が履歴にあれば → **信頼済み**（VRChat でストリームを視聴した実績あり）
- 履歴になければ → **未確認**（外部から初めて来た接続）
- Cloudflare Tunnel を経由する場合 `CF-Connecting-IP` ヘッダーから実 IP を取得

**④ ホストへの通知（サーバー管理 UI）**
- アクセス要求が届いたらサーバー UI にリアルタイム通知
- 通知には以下を表示:
  - ユーザー名
  - 信頼度ラベル: 「✅ 配信視聴済み」 / 「⚠️ 接続履歴なし」
  - 接続元 IP（参考表示）
  - 承認 / 却下ボタン

**⑤ 承認・セッショントークン発行**
- 承認するとサーバーが短命トークンを発行（例: 1セッション有効の UUID）
- トークンをゲストのブラウザにリダイレクトパラメータとして渡す
- 以降のリクエスト送信は全てこのトークンで認証

**⑥ リクエスト専用ページ**
- URL または SoundCloud リンクの投稿フォーム
- リクエスト済み曲のキュー表示（「あなたのリクエスト: 何番目」）
- スパム対策: トークン単位で一定間隔制限（例: 3分に1曲）

---

#### スコープ外（初期実装では対象外）
- 自動承認モード
- ゲスト間のリクエスト閲覧（他のゲストが何をリクエストしたか見える機能）
