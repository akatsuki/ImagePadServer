# RTSP H.264 映像互換性契約

契約ID: `rtsp-h264-single-slice-v1` / 制定: 2026-09-15

## 根拠と適用範囲

PC版VRChatで「上部だけ表示」「下端が画面下まで引き延ばされる」「極端な低fps」が発生した。
正常に映る友人の配信は1フレーム1スライス、問題のAirPlay配信は8スライスだった。
AirPlayを1スライスに変更した後、ユーザーが同じ環境で「ちゃんと映った」と確認した。
これは今回の環境での実機成功であり、全GPU・全ワールド・全入力の互換性保証ではない。

この契約はアプリが生成してPC向けRTSPに渡すH.264へ適用する。
受信するiPhone/OBSのH.264には複数スライスを許容し、入力を誤って拒否しない。
SPS/PPS/SEI/AUDは映像スライスに数えない。
Redditアプリの外部画面描画の不具合は送信アプリ側の別問題で、この契約の対象外。

## 変更不可の出力条件

| 条件 | 必須ルール |
|---|---|
| 映像スライス数 | 各アクセスユニットにVCL NAL（type 1または5）が厳密に1つ |
| x264の並列化 | `sliced-threads=0`。CPU数に応じたスライス分割は禁止。フレーム単位の並列化は可能 |
| スライス分割 | `slices=1:slice-max-size=0:slice-max-mbs=0`を明示。MTU対策に映像スライス分割を使わない |
| 適用順序 | preset/tune・診断上書き後の実効値を維持。設定文字列の存在だけで合格にしない |
| 境界処理 | 既存のSPS/PPS、IDR待ち、AU単位の欠損破棄、RTP timestamp/marker/sequence処理を保持 |
| RTP断片化 | 1スライスが複数のFU-Aパケットになることは許容。1スライスと1パケットを混同しない |
| 再生成 | 初回・再接続・回転・解像度変更・CPUフォールバックでも同じエンコーダー契約を適用 |

ソフトウェア設定の定義元:

- native: `native/airplay-gstreamer-bridge/rtsp_h264_contract.h`
- OBS RTSP: `internal/obsrtmp/rtsp_encoder_policy.go` の `rtspX264SingleSliceOptions`

現在のsource-clockと旧direct CPU経路は共通ヘッダーを使う。
OBS RTSPのCPU経路は診断tuneを変更しても契約を保持する。
NVENCなどGPU経路、圧縮映像のcopy経路を変更・昇格する場合も、実際の出力で1スライスを確認する。
今回追加した自動エンコード検査の対象はx264であり、GPUの実測合格を代用しない。

## 固定しない要素

ビットレート・CRF・preset・GOP・profile・CABAC・参照枚数は、スライス数と独立して扱う。
現在のAirPlayはsuperfast、OBS RTSP CPUはfastを維持する。
正常な友人の配信でもCABAC・複数参照・0.5秒GOPを使用していたため、これらを根拠なく禁止しない。
画質や負荷の調整時も上表の契約を緩めない。

## 必須の自動検証

1. `go test ./internal/obsrtmp -run TestRTSP -count=1`。
   FFmpegの実エンコード検査では、横720p・縦720p・1080p、threads=4/8/自動、通常/診断tuneで
   36フレームずつAUD区切りの映像スライス数を数える。FFmpegがない環境のSKIPは配布合格に数えない。
2. `scripts/build-airplay-gstreamer-bridge.ps1`でnativeをビルドする。
   `airplay_source_clock_single_slice`をCTestの必須項目とし、存在確認と実行成功の両方を要求する。
   source-clockおよび旧direct CPUの実際のfactoryを使用し、360p/720p/1080p・縦横・30/60fps・
   threads=1/4/8/自動を組み合わせて圧縮出力を検査する。
3. 検査成功後だけ`imagepad-airplay-gstreamer-bridge-build.json`を生成する。
   `videoContract`とbridge exeのSHA-256を記録する。失敗前の記録を再利用しない。
4. `scripts/package-airplay-gstreamer-runtime.ps1`は、この記録とexeハッシュの一致を必須とする。
   旧記録・記録なし・複数スライス・検査失敗・ハッシュ不一致は例外で停止する。
5. `scripts/build-release.sh`はexeに内蔵する**実ZIP**を
   `scripts/verify-airplay-h264-archive.py`で検査してから埋め込む。
   ZIPの外側のハッシュが正しくても、内部bridgeに検査記録がなければ停止する。

手動ビルド、独自のZIP差し替え、`go build -tags airplay_runtime_embedded`によって検査を迂回した成果物は配布不可。
回帰テスト削除・除外、multi-slice許容への期待値変更、検査記録の手書きは禁止。

## ランタイム更新と実機受入れ

- 成功した配布物のexe・bridge・ZIPのハッシュとruntimeSetIDを記録する。別内容に同じIDを使わない。
- HTTPのhealthだけで受信を開始しない。期待するruntimeSetIDが`ready`になってから開始し、
  実際のreceiver/bridgeプロセスのパスとbridgeハッシュを照合する。
- 今回成功した構成のfixed PCM/UDPフラグを、映像設定変更に混ぜて変えない。
- エンコーダー、GStreamer/x264、MediaMTX、RTP処理、配布ランタイムの更新では、上記自動検査に加えて
  問題が出たPC版VRChatの同じワールドで、初回/途中参加・縦横回転・再接続・音声を確認する。
- AVPro単体ハーネスは今回の不具合を再現できなかった。ハーネスPASSだけで実機確認を省略しない。
- 全面表示・操作追従・通常fps・音声の確認結果を、入力方式（OBS/AirPlay）、runtimeSetID、実行ファイルに紐付ける。
- 失敗時は成功済み配布物に戻し、条件を1つずつ比較する。原因候補を同時に変更しない。

この契約は同じ設定退行を防止するもの。別原因による全ての表示不具合が起こらないという保証にはしない。
