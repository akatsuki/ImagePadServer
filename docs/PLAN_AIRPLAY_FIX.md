# AirPlay 修正・改善 手順書（wave 分割）

> 対象: internal/airplay/* / internal/obsrtmp/manager.go / internal/server/*
> 目的: 静的解析＋実機ログで洗い出した全 24 件（改善 12 + 映像品質 12）を、巻き戻し可能な wave 単位で修正する
> 作成: 2026-08-25

## 1. 目的と全体像

iPhone/iPad の AirPlay 画面共有入力を「映像・音声とも安定して配信できる」状態に引き上げる。

優先順（依存・リスク順）:

1. A/V 同期（約3.8秒ずれ）を解消 <- 実機ログで確定済み・最重要
2. H.264 回復経路・フレームレート・音声リレーの堅牢化
3. 音声二重圧縮・色域・縦画面などの映像/音質パラメータ

全修正を wave 0〜10 に分割。各 wave は独立にチェックし、1 wave = 1 コミットで記録する。任意の wave まで巻き戻せる。

## 2. 前提（現状）

- ブランチ: main（origin/main より ahead 4）
- 未コミットの AirPlay 仮実装あり: 22 modified + 6 untracked の .go ファイル
- コミットしないもの: .hermes/（添付ファイル）、youtube_cookies.txt 等のシークレット
- 実機ログで確定済みの不具合: A/V ずれ（映像 PTS 15.63 秒 vs 音声 PTS 19.47 秒 ≒ 3.8 秒、音声が先行）

## 3. 全体方針

### 3.1 wave 単位のコミットと巻き戻し

- 1 wave = 1 コミット（線形履歴）。メッセージは type(airplay): 概要 形式。
- 巻き戻し: git revert <SHA>（逆コミットを作成）。後続 wave と同一ファイルを編集していて revert が競合する場合は、その場で解決するか、該当ファイルのみ git checkout <SHA>^ -- <file> で戻す。
- 破壊的に戻す場合（ローカルのみ）: git reset --hard <SHA>。
- 各 wave は「この wave 単体を revert してもビルド/テストが通る」ことを目標に、機能単位で閉じる。
- 新しいロジックは既存ファイルに詰め込まず、1機能=1ファイルで新規 .go を切る（例: fps_detector.go）。

### 3.2 検証の共通手順

プロジェクトルートで実行:

```
cd "C:/Users/masah/OneDrive/ドキュメント/GitHub/ImagePadServer"

# FFmpeg/FFprobe の実体を固定（未指定だと go test が ~100MB を DL して flake する）
export IMAGEPAD_FFMPEG="C:/Users/masah/AppData/Local/Microsoft/WinGet/Packages/Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe/ffmpeg-8.1.1-full_build/bin/ffmpeg.exe"
export IMAGEPAD_FFPROBE="C:/Users/masah/AppData/Local/Microsoft/WinGet/Packages/Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe/ffmpeg-8.1.1-full_build/bin/ffprobe.exe"

# ビルド / テスト / 差分の空白チェック
go build ./...
go test ./...
git diff --check
```

- テスト前に残留子プロセスを停止:
  ```
  MSYS_NO_PATHCONV=1 taskkill /F /IM imagepadserver.exe 2>/dev/null
  MSYS_NO_PATHCONV=1 taskkill /F /IM uxplay.exe 2>/dev/null
  MSYS_NO_PATHCONV=1 taskkill /F /IM ffmpeg.exe 2>/dev/null
  ```
- 実機テスト: IMAGEPAD_AIRPLAY=1 IMAGEPAD_AIRPLAY_RTP_DEBUG=1 で起動し、X タイムライン / 動画全画面 / 回転 を目視。パケット単位の詳細ログが必要なときのみ IMAGEPAD_AIRPLAY_RTP_PACKET_DEBUG=1 を追加。

## 4. 問題の全件リストと wave 対応

A=改善 / B=映像品質。分類: 実測（実機ログ確定）/ 推定（コード追跡）/ 未証明（要実機確認）。

| ID | 内容 | 分類 | wave |
|----|------|------|------|
| B1 | A/V同期の共通基準なし（約3.8秒ずれ） | 実測 | 1 |
| A1 | IDR回復の穴（dropUntilMarker が回復IDRを破棄） | 推定 | 2 |
| A2 | タイムスタンプ不連続時に状態リセットされない | 推定 | 2 |
| B8 | パケット損失回復がIDR依存（PLI/FIR未送信） | 推定 | 2 |
| A3 | 固定30fpsタイムスタンプ合成 | 実測 | 3 |
| B2 | 高fpsソース劣化 | 実測 | 3 |
| A5 | 音声クロックのドリフト補正なし | 推定 | 4 |
| A6 | 音声キュー溢れのフレーム境界無視 | 実測 | 4 |
| A7 | 音声マーカービット先頭のみ | 実測 | 4 |
| B11 | 映像開始前音声の欠落 | 実測 | 4 |
| A9 | 音声コーデックL16決め打ち | 未証明 | 5 |
| A4 | ブリッジ無制限リスポーン | 実測 | 6 |
| A8 | ポート予約TOCTOU/RTCP死予約 | 実測 | 6 |
| A10 | デコーダリフレッシュ固定インターバルのみ | 実測 | 6 |
| A11 | UxPlay起動同期がログ文字列待ち | 推定 | 6 |
| A12 | 再接続ループ中の状態報告停滞 | 実測 | 6 |
| B3 | 音声のみ二重圧縮 | 実測 | 7 |
| B4 | サンプルレート不一致 | 実測 | 7 |
| B5 | 色域・色空間シグナリング欠如 | 推定 | 8 |
| B6 | ビットレート/VBV上限なし | 実測 | 8 |
| B7 | VFR正規化なし | 実測 | 8 |
| B10 | AirPlay固有画質設定なし | 実測 | 8 |
| B12 | IDR間隔制御がパススルー任せ | 実測 | 8 |
| B9 | 縦画面レターボックス | 実測 | 9 |

## 5. Wave 詳細

### Wave 0 — ベースライン確定

- 目的: 以後の全 wave の巻き戻し基点を固定。現状の AirPlay 仮実装（未コミット）と本手順書をコミットする。
- 対象: 22 modified + 6 untracked の .go ファイル。.hermes/ は除外。
- 変更内容: コード変更なし。コミットのみ（git add は対象 .go と本手順書を明示指定し、git add . は使わない）。
- チェック: go build ./... / go test ./... / git diff --check
- コミット: chore(airplay): establish AirPlay implementation baseline
- 巻き戻し: この wave 自体は revert しない（基点）。以降の全 wave はこの SHA を起点に revert 可能。

### Wave 1 — A/V 同期の原点正規化（最重要）

- 目的: 映像と音声の時間原点を揃え、約3.8秒のずれを解消。
- 対象: internal/airplay/h264_rtp_relay.go（normalizeTimestamp）、h264_rtp_relay_test.go
- 変更内容:
  - normalizeTimestamp で初回入力 RTP タイムスタンプを記録し、以後 outputTimestamp = inputTimestamp - firstInputTimestamp（0 基準化）。初回出力は 0 起点。
  - 音声側は既に 0 基準（asetpts=N/SR/TB）なので、これで時間原点が共有される。
  - 回帰テスト: 任意オフセットの入力シーケンスに対し、出力が 0 から始まり単調増加することを検証。
- チェック:
  - go test ./internal/airplay/
  - 実機: X 動画全画面で口元と音の一致（約3.8秒ずれ解消を FLV pts_time ログで確認）。
- コミット: fix(airplay): normalize video RTP timestamps to a shared zero origin
- 巻き戻し: git revert <SHA>

### Wave 2 — H.264 回復経路の修正

- 目的: シーケンスギャップ/パケット損失後の回復を確実にする。
- 対象: internal/airplay/h264_rtp_relay.go（dropUntilMarker / waitingForIDR / resetDecoderInput）、test
- 変更内容:
  - ギャップ後に届いた回復用 IDR を dropUntilMarker が破棄しないよう修正（IDR はマーカー/境界判定に関わらず受理する）。
  - resetDecoderInput が outputTimestamp / lastInputTimestamp / previousMarker / timestampInitialized もリセットする。
  - 損失時の次 IDR 待ちに加え、可能なら PLI/FIR（キーフレーム要求）を送る。実装が及ばない場合は、この wave のコミットメッセージに「現状は次IDR待ちのまま」と明記して分離する。
- チェック:
  - go test ./internal/airplay/（ギャップ注入テスト: IDR→損失→回復IDR で出力が継続すること）。
  - 実機: 遮蔽/電波不安定時に画面が数秒フリーズしないこと。
- コミット: fix(airplay): recover H.264 after gap and reset timestamp state
- 巻き戻し: git revert <SHA>

### Wave 3 — フレームレート検出（30fps 固定の解消）

- 目的: 固定 30fps をやめ、実ソースの fps に追従する。
- 対象: 新規 internal/airplay/fps_detector.go（1機能1ファイル）、h264_rtp_relay.go の呼び出し、test
- 変更内容:
  - 到着パケットの RTP タイムスタンプ間隔から fps を計測（移動平均/EMA）。フォールバックは 30fps。
  - defaultH264RTPFrameTimestampStep（90000/30）を実測 fps ベースの step に置換。
  - UxPlay が実タイムスタンプを供給する場合はそれを優先し、定数合成時のみ fps 推定を使う。
- チェック:
  - go test ./internal/airplay/（fps 推定の単体テスト）。
  - 実機: 60fps ゲーム等で半速にならないこと。
- コミット: feat(airplay): derive RTP frame timestamp step from measured fps
- 巻き戻し: git revert <SHA>

### Wave 4 — 音声リレー堅牢化

- 目的: 音声のノイズ/ドリフト/冒頭欠落を減らす。
- 対象: internal/airplay/audio_rtp_relay.go、test
- 変更内容:
  - キュー溢れ時の破棄をフレーム境界でアライン（L16 = 1サンプル2byte、ステレオ = 4byte）。
  - 入力 RTP タイムスタンプを参照したドリフト補正（固定 ticker ではなく入力クロックに追従）。
  - マーカービットをトークスパート境界（無音→有音の切替）で通知。
  - 映像開始前の音声をバッファし、videoStarted 後に先頭から再生（冒頭音声欠落の解消）。
- チェック:
  - go test ./internal/airplay/（境界アライン・ドリフトの単体テスト）。
  - 実機: クリック/ノイズがない、長時間で音ズレしない、冒頭音声が切れない。
- コミット: fix(airplay): harden audio relay (frame alignment, drift, marker, lead-in)
- 巻き戻し: git revert <SHA>

### Wave 5 — 音声コーデック検証

- 目的: L16 以外を誤転送しない。
- 対象: internal/airplay/audio_rtp_relay.go、test
- 変更内容:
  - payload type から L16（pcm_s16be）以外を検出し、未対応コーデックとして警告/拒否（fail-closed）。
  - 検出結果をステータスに反映。
- チェック: go test ./internal/airplay/
- コミット: feat(airplay): detect unsupported audio codecs instead of mis-transcoding
- 巻き戻し: git revert <SHA>

### Wave 6 — ブリッジ耐障害性

- 目的: FFmpeg ブリッジの異常時の無限リスポーンと状態停滞を直す。
- 対象: internal/airplay/manager.go、test
- 変更内容:
  - リスポーンに指数バックオフ＋回数上限（上限到達で失敗を status に反映）。
  - 再接続ループ中の status.Message 更新（「再接続中（n回目）」等）。
  - scheduleBridgeDecoderRefresh に再試行（空振り時）。
  - ポート予約の TOCTOU 解消と RTCP 予約の整理。
  - UxPlay 起動同期のログ文字列待ちにタイムアウトを追加（起動失敗を検出）。
- チェック:
  - go test ./internal/airplay/（異常系: 即時終了するブリッジを注入してバックオフ/上限を検証）。
  - 実機: FFmpeg が落ちても有限回で停止し、UI にエラーが出る。
- コミット: fix(airplay): add bridge respawn backoff, status reporting, refresh retry
- 巻き戻し: git revert <SHA>

### Wave 7 — 音声二重圧縮・サンプルレート統一

- 目的: 音声の世代損失を減らし、リサンプルを削減。
- 対象: internal/airplay/manager.go（ブリッジ引数）、internal/obsrtmp/manager.go
- 変更内容:
  - ブリッジの -ar 44100 を OBS 側の 48000 に統一（または OBS 側を 44100 に）。
  - 二重エンコード解消: 可能なら OBS 側で AAC をパススルー、不可ならリサンプル回数を 1 回に。
- チェック:
  - go test ./...
  - 実機: 音質劣化の聴感確認。
- コミット: fix(airplay): unify audio sample rate and avoid double encode
- 巻き戻し: git revert <SHA>

### Wave 8 — 映像品質パラメータ

- 目的: 色ずれ・詰まり・VFR ジャダー・IDR 間隔を改善。
- 対象: internal/airplay/manager.go（ブリッジ引数）、internal/obsrtmp/manager.go、可能なら新規 internal/airplay/video_quality.go
- 変更内容:
  - 色域/色空間シグナリング（bt709 等）を明示。
  - ビットレート/VBV 上限（-maxrate/-bufsize 相当）。
  - VFR→CFR 正規化（-vsync cfr / fps フィルタ）。
  - IDR 間隔制御（config-interval の適正値、静止画面でも定期 IDR）。
  - AirPlay 固有の解像度上限/ビットレート設定の追加（B10）。
- チェック:
  - go test ./...
  - 実機: 色ずれなし、高ビットレート送信でも詰まらない、静止画面からの復帰が速い。
- コミット: feat(airplay): add colorspace signaling, VBV cap, CFR, IDR interval
- 巻き戻し: git revert <SHA>

### Wave 9 — 縦画面対応

- 目的: 9:16 ミラーリングの黒帯と解像度浪費を解消。
- 対象: internal/obsrtmp/manager.go（stableOBSVideoFilter）、test
- 変更内容:
  - 16:9 固定の scale/pad をアスペクト比可変に（ソース解像度/回転メタデータに追従）。
  - 回転メタデータの伝播。
- チェック:
  - go test ./...
  - 実機: 縦画面で黒帯なし・解像度を活かす。
- コミット: fix(obs): support portrait mirroring without letterbox
- 巻き戻し: git revert <SHA>

### Wave 10 — 総合 E2E 検証と production_ready 判定

- 目的: 全 wave の実機確認とリリース可否判定。
- 対象: 特になし（E2E で見つかった最終修正のみ）。
- チェック: 実機 E2E（§6 の受け入れ基準）。
- コミット: E2E で必要な最終修正（あれば）。dev ビルドを切る場合は chore: cut v1.7.0-devN を別途 1 コミットに（about.go と winres/winres.json のバージョン更新＋rsrc_windows_amd64.syso 再生成を含める）。
- 判定: production_ready は E2E 全項目パスまで false。
- リリース: 明示指示まで保留（merge と release を分離）。

## 6. 実機 E2E 受け入れ基準

以下すべてが目視で確認できるまで production_ready=false。

1. X タイムライン連続スクロールが映像に反映される。
2. X 内動画を全画面にしても映像・音声が継続する。
3. 全画面から戻っても freeze しない。
4. FLV output time が実時間どおり進む（-debug_ts ログで確認）。
5. A/V 同期: 口元と音が一致する（Wave 1 後、3.8 秒ずれが解消）。
6. 縦横回転・ホーム・アプリ切替後も古い画面・緑画面・freeze が出ない。
7. 音声にクリック/ノイズがなく、長時間（10 分以上）で音ズレしない。
8. 色ずれがない（肌色等の自然な色）。
9. 縦画面ミラーリングで黒帯が出ない。
10. OBS publisher 接続が維持される。

## 7. リリース手順（merge / release 分離）

- 各 wave のコミットは main に積む。全 wave + E2E パス後に merge のみ実施（ff-merge 相当）。
- release は明示指示まで保留。release 時は CLAUDE.md の「dev ビルドは必ずコミットとペア」ルールに従い、バージョン更新＋リソース再生成＋コミットを 1 セットで行う。

## 8. 注意

- 各 wave の行番号は修正が進むとずれるため、コード参照は関数名/シンボル名で行う。
- 実機検証ができない wave は「go test のみ green」を確認してコミットし、E2E は Wave 10 でまとめて行う。
- クレデンシャル・.hermes/・youtube_cookies.txt は一切コミットしない。
