# ミュージックモード プレイリスト（v1.6.1）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 凍結中のプレイリスト機能を v1.6.1 仕様（ROADMAP_v1.5.md）どおり完成させる。ラジオ型連続配信（mediamtx 経由で RTSP / LL-HLS 同時提供）＋クラシック iTunes 風 GUI。

**Architecture:**
1. 各曲は追加時に既存ビジュアライザーで `radio-track-<id>.ts`（H.264+AAC）へ**事前レンダリング**する（先読み）。
2. 再生は OBS 基盤の mediamtx を再利用した **RadioManager**（`internal/obsrtmp`）が `ffmpeg -re -i track.ts -c copy -f rtsp` で連続 push。曲終了（ffmpeg 正常終了）→ キューの次曲を即 push。視聴側 URL（RTSP / LL-HLS）は固定。
3. キュー・シャッフル・ループ・JSON 永続化は純粋ドメイン `internal/playlist` パッケージ。
4. UI はミュージックボタンのモードメニュー（シングル/プレイリスト）を復活し、プレイリストモードでクラシック iTunes 風パネル（LCD 表示・トランスポート・ストライプ曲テーブル・統合入力）を表示。

**Tech Stack:** Go（net/http, ffmpeg, mediamtx）、素の JS/CSS（`ui_script_*` / `ui_css_*` に埋め込み）。

**設計判断（確定済み・ユーザー回答）:**
- デザイン: クラシック iTunes 風（LCD ナウプレイング＋列付き曲テーブル＋ストライプ行＋♪再生中マーク）
- スコープ: v1.6.1 全量（リスト UI・統合入力・自動連続再生・シャッフル/ループ・JSON 保存）
- 連続再生方式: ラジオ型。RTSP と HLS を選べる。OBS のシステム（mediamtx）に流し込む
- モード: シングル/プレイリスト切替を復活（パーティーは将来。メニューには出さない）

---

## Phase 1 — `internal/playlist` ドメインパッケージ（TDD）

### Task 1: Track と Queue の基本操作

**Files:**
- Create: `internal/playlist/playlist.go`
- Test: `internal/playlist/playlist_test.go`

- [x] Queue: Add / Remove / SetOrder / Current / PlayNow / Snapshot
- [x] Track: ID 自動採番（crypto/rand hex8）、Status = preparing|ready|failed
- [x] スレッドセーフ（sync.Mutex）。Snapshot はコピーを返す

```go
type Track struct {
    ID, Title, Artist, Album           string
    DurationSeconds                    int
    SourceKind, OriginalName           string
    MediaPath, ThumbnailPath           string
    Status                             TrackStatus
    Error                              string
    AddedAt                            time.Time
}
```

- [x] `go test ./internal/playlist/` green → commit `feat(playlist): add queue domain`

### Task 2: Next()（ループ・シャッフル）

- [x] loop=false: 末尾で "" を返す（停止）
- [x] loop=true: 末尾→先頭
- [x] shuffle=true: 未再生曲から乱択、全曲再生済みなら loop に従いリセット or 終了。ready 以外はスキップ
- [x] commit `feat(playlist): next-track selection with shuffle/loop`

### Task 3: JSON 永続化（保存・読み込み・一覧）

**Files:**
- Create: `internal/playlist/store.go`
- Test: `internal/playlist/store_test.go`

- [x] `Store{path}` — `playlists.json`（library dir、お気に入りと同じ層）
- [x] `Save(name, tracks)` / `Load(name)` / `List()` / `Delete(name)`。atomic write（tmp→rename）
- [x] Load 時に MediaPath 消失曲は Status=failed, Error="メディアファイルが見つかりません（再追加が必要）"
- [x] commit `feat(playlist): JSON persistence store`

---

## Phase 2 — `internal/video` レンダリング拡張

### Task 4: 曲の事前レンダリング（TS 出力）

**Files:**
- Modify: `internal/video/audio_visualizer.go`（出力フォーマット差し替えポイント: `audioVisualizerFFmpegArgsWithEncoder` の HLS 出力部）
- Create: `internal/video/radio_render.go`
- Test: `internal/video/radio_render_test.go`（args の純粋テスト + IMAGEPAD_* ピン留め済み実行テスト）

- [x] `audioVisualizerMPEGTSArgs(...)`: 既存 args の `-f hls ...` 部分を `-f mpegts <out.ts>` に置換した変種
- [x] `RenderRadioTrack(ctx, outDir, ffmpeg, input, trackID, preset) (string, error)` → `radio-track-<trackID>.ts`
  - 既存 `RunAudioVisualizerHLS` と同じフレーム供給（`writeVisualizerFrames`）を再利用
- [x] `RadioPushArgs(mediaPath, rtspURL)` = `-re -i <ts> -c copy -f rtsp -rtsp_transport tcp <url>`
- [x] commit `feat(video): pre-render radio track TS and RTSP copy-push args`

---

## Phase 3 — `internal/obsrtmp` RadioManager

### Task 5: RadioManager（mediamtx 再利用・連続 push・自動次曲）

**Files:**
- Create: `internal/obsrtmp/radio.go`
- Test: `internal/obsrtmp/radio_test.go`（fake managedProcess / fake pusher で遷移をテスト）

- [x] `NewRadioManager(outDir, host string, next func() (mediaPath string, trackID string, ok bool), cb RadioCallbacks)`
- [x] `Start(ctx)`: EnsureMediaMTX → allocMediaMTXPorts → mediaMTXCredential → newMediaMTXRuntime(path="radio-<sessID>") → 開始
- [x] push ループ: next() から曲を取得 → `ffmpeg RadioPushArgs` 実行 → 正常終了で次曲、next() が ok=false なら idle（mediamtx は維持）
- [x] `PlayNowSignal()`: 現 push を kill して即 next() から取り直す（割り込み再生・スキップ共用）
- [x] `Stop(timeout)` / `Status()`（RTSP URL, HLS proxy 準備, 現曲 trackID, 再生開始時刻）
- [x] `ProxyLLHLS(w, r, name)`（既存 mediaMTXRuntime.proxyHLS を委譲）
- [x] RadioCallbacks: `OnTrackStart(trackID)` / `OnIdle()` / `OnStopped()`
- [x] commit `feat(obsrtmp): radio manager for continuous playlist streaming`

---

## Phase 4 — server 統合（API）

### Task 6: Server 配線と状態

**Files:**
- Modify: `internal/server/server.go`（フィールド追加、ルート登録）
- Create: `internal/server/music_playlist.go`（ハンドラ群）
- Test: `internal/server/music_playlist_test.go`

- [x] Server に `musicQueue *playlist.Queue` / `radio *obsrtmp.RadioManager` / `playlistStore *playlist.Store` を追加
- [x] ルート（すべて `s.admin`）:
  - `GET  /api/music/playlist` → 状態（tracks snapshot, currentTrackID, playing, shuffle, loop, rtspURL, hlsURL）
  - `POST /api/music/playlist/add` → {url} または multipart file。202 で即応答し、裏で acquire→render→ready（ingest 進捗は既存 setIngest 流用）
  - `POST /api/music/playlist/remove` {id}
  - `POST /api/music/playlist/reorder` {ids: []}
  - `POST /api/music/playlist/play` {id?} → id 指定は PlayNow、radio 未開始なら Start
  - `POST /api/music/playlist/next` / `stop`
  - `POST /api/music/playlist/options` {shuffle?, loop?}
  - `GET/POST /api/music/playlists`（保存済み一覧/保存）、`POST /api/music/playlists/load` {name}
- [x] 統合入力の自動判定: `http(s)://` → musicURLAcquirer、それ以外はローカルパス/アップロードファイル
- [x] URL 公開: 既存 share-url 解決（c3ad051 の集約層）に radio の RTSP/HLS を追加
- [x] commit `feat(server): music playlist API and radio wiring`

---

## Phase 5 — UI（クラシック iTunes 風）

### Task 7: モードメニュー復活

**Files:**
- Modify: `internal/server/ui.go`（musicIntentButton にキャレット＋menu 復活。パーティーは入れない）
- Modify: `internal/server/ui_script_music_controller.go`（fae44cf 以前の menu 制御を単純化して復活、mode='single'|'playlist'）
- Modify: `internal/server/ui_script_domrefs.go`
- Modify: `internal/server/ui_media_test.go`（`TestMusicWorkspaceUIRestoresSingleModeOnly` を差し替え: シングル/プレイリスト2択を強制、party を禁止リストへ）

- [x] commit `feat(ui): restore single/playlist music mode menu`

### Task 8: iTunes 風プレイリストパネル

**Files:**
- Modify: `internal/server/ui.go`（`musicPlaylistPanel` セクション新設 — 旧モックは復活させない）
- Modify: `internal/server/ui_css_upload.go`（旧 `.music-playlist-*` 残骸を削除して新規スタイル）
- Create: `internal/server/ui_script_playlist_controller.go`
- Modify: `internal/server/ui_scripts.go`（結合順に追加）
- Test: `internal/server/ui_media_test.go`

構成（上から）:
1. **LCD ディスプレイ** — 淡いグリーングレー（クラシック iTunes LCD）。曲名/アーティスト中央表示、下に進行バー＋経過/残り時間（duration はサーバー state から補間）
2. **トランスポート** — ⏮ ▶/⏹ ⏭ ＋ 右端に シャッフル / ループ トグル（active 状態は青ハイライト）
3. **統合入力バー** — テキスト入力1本（URL でもファイルパスでも可、パネル全体がドロップターゲット）＋「追加」「今すぐ再生」＋ 隠し file input（クリップアイコン）
4. **曲テーブル** — ヘッダ（# / 曲名 / アーティスト / 時間 / 追加元）、偶数行ストライプ、再生中行は ♪＋ハイライト、行ドラッグで並べ替え（HTML5 DnD）、行ホバーで 削除 / 今すぐ ボタン
5. **共有 URL ボックス** — RTSP / HLS 切替＋コピー（既存 share URL UI パターンに合わせる）

JS コントローラ:
- [x] 状態は `/api/music/playlist` を 2 秒ポーリング（プレイリストモード表示中のみ）＋操作後即時再取得
- [x] DnD 並べ替え → `/reorder`、ドロップファイル → `/add`（multipart）
- [x] commit `feat(ui): classic-iTunes playlist panel`

### Task 9: 保存・読み込み UI

- [x] LCD 右肩に「プレイリスト▾」メニュー: 保存（名前入力）/ 読み込み / 削除
- [x] commit `feat(ui): playlist save/load menu`

---

## Phase 6 — 仕上げ

### Task 10: ゲート＆ドキュメント＆dev ビルド

- [ ] `go build ./...` / `go test ./...` green
- [ ] ROADMAP_v1.5.md の v1.6.1 に進捗注記、ROADMAP.md 更新
- [ ] CLAUDE.md ルールどおり `chore: cut v1.3.0-devN`←※現行の実バージョン系列に合わせて `v1.5.x-devN` を確認して採番、about.go + winres.json + syso 再生成、**ビルドとコミットを同一ステップで**

---

## Self-Review メモ

- 仕様網羅: リスト UI(Task 8) / D&D 並べ替え(8) / 行削除・割り込み(8) / 再生中ハイライト(8) / 統合入力(6,8) / 追加=末尾(6) / 今すぐ再生(6,8) / 自動連続(5) / シャッフル・ループ(2,8) / JSON 保存(3,9) — 全項目にタスクあり
- 音声のみで映像なし: ビジュアライザー映像を含む TS を push するため VRChat 動画プレイヤーでそのまま視聴可
- ffmpeg 再接続間のギャップ: mediamtx は publisher 再接続を許容。RTSP 視聴側は数百 ms のギャップ、LL-HLS はセグメント境界で吸収
- 曲間で解像度/fps が揃うよう、レンダリングは常に musicQualityPreset の高さで統一
