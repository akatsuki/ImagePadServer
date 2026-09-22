# ニコニコ書き出し：NVENC後段高速化の統合実装計画

作成日: 2026-09-22。状態: T0〜T6完了（T6: 候補不採用）。GPU候補は現環境でunavailable。tee候補は昇格CPU20%実workerの3条件40ペア統計で全条件が採用閾値未達となり、既定separateを維持する。

> 実装担当: 本書を共通仕様とし、`parallel-task`で依存が解消した作業だけを実行する。実装時は`superpowers:executing-plans`または`superpowers:subagent-driven-development`を使用する。性能試験は直列実行する。

**Goal:** VRChat併用・書き出しCPU約20%の条件で、コメント、画質、音声、出力形式を保ちながら現行NVENC経路をさらに高速化する。

**Architecture:** 小規模改善は一度の符号化からMP4/HLSを同時生成する。大規模改善はコメント合成・色変換・NVENC入力を同じnativeプロセスのD3D11リソースでつなぎ、完成画像のCPU読み戻しとrawvideoパイプを省く。両者は個別に評価し、品質・速度・VRChat負荷の採用条件を満たしたものだけ統合する。

**Tech Stack:** Go、Windows Job Object、FFmpeg/libavcodec/libavformat、D3D11、NVENC、既存NPS3スプライトストリーム、Python診断ハーネス。

**Spec:** 本書の「採用する設計」「共通制約・採用条件」、`docs/RTSP_H264_COMPATIBILITY_CONTRACT.md`、`nico-native-integration-plan.md`のNPS3契約。

## 1. 1〜4の調査結果

| 元の案 | 確認結果 | 本計画での扱い |
|---|---|---|
| 1. RGBAをYUV420/NV12へ変更 | 現行RGBAは透明なコメント層。YUV420P/NV12にはalphaがなく、単純置換では背景との合成が成立しない | 単独採用しない。2と統合し、不透明な完成画像になった後のNV12化を試作する |
| 2. GPUでoverlay・色変換 | 実装可能なAPI経路はある。ただし既存の別プロセスRGBA背景入力方式は過去に18.2%遅くなり、色差も残った | 同一プロセス・同一GPUリソースを使用する新しい経路だけを候補にする。大幅改善の本命だが、効果は未計測 |
| 3. NVENC p1/p2 | 今回CPU20%でp1は2.6%、p2は1.4%短縮。容量は6.8%/6.5%増加。画質同等性は未検査 | 既定p4を維持。今回の実装対象から外し、GPU経路成立後にencoderが再び律速になった場合のみ再評価 |
| 4. MP4/HLS同時生成 | teeで実行可能。今回CPU20%で2.7%、制限なしで13.1%短縮。出力の基本検証も成立 | 小規模な条件付き候補として先行。実worker・長尺で効果を再確認して採否を決める |

### 説明・測定上の訂正

- `frame_pipe_write`は`frames.Next()`、読み込み・割当、pipe書き込み、FFmpeg下流の処理待ちを含む。0.732秒を純粋なメモリー転送時間と断定しない。
- 4 byte/pixel→1.5 byte/pixelの62.5%減少は、合成済みフレームを比較した理論値。処理時間の短縮率ではない。透明度を保持するYUVA420Pは2.5 byte/pixelで、RGBAからの減少は37.5%。さらに色差の間引きが合成前に入る。
- `scale_cuda`はRGB↔YUV変換を提供しない。FFmpeg 8.1.1の`overlay_cuda`はNV12同士、またはYUV420P基底＋YUV420P/YUVA420Pに限定される。RGBAの既存グラフにCUDAのフィルター名を代入するだけでは移行できない。
- 製品のnative compositorはD3D11 **WARP**で、ハードウェアGPUではない。既存のnative使用実績を新しいGPU経路の成功扱いにしない。
- 先の20%対制限なし比較にはFFmpeg 9.0.1 sharedと8.1.1 fullの差もある。過去の短縮率は参考値とし、今後は同じバイナリ・hashで対照を取り直す。
- アプリのbuild成功、`obs-relay-config`応答、保存済みRGBAハーネスの成功は、描画を含む製品workerの完走を証明しない。以前のChrome GPU起動失敗・native `truncated scene`を解消した実workerの基準値が必要。

## 2. 今回の追加測定

製品ソースは変更せず、既存のRGBA入力を使用した診断。1920×1080、30fps、180フレーム、6秒、NVENC CQ29/HQ、B=3、1 AU=1 slice、AAC160k、同一FFmpeg。各条件warmup 1回＋反転順序3回、以下はwarmupを除く中央値。VRChatプロセスは測定準備時に不在。VRChat同時動作の採用証拠ではない。

| 条件 | CPU20%総時間 | 対p4 | 制限なし総時間 | 対p4 | MP4 bytes |
|---|---:|---:|---:|---:|---:|
| p4＋後段HLS | 1.669秒 | 基準 | 0.932秒 | 基準 | 6,375,297 |
| p1＋後段HLS | 1.626秒 | 2.6%短縮 | 0.894秒 | 4.0%短縮 | 6,809,790 |
| p2＋後段HLS | 1.646秒 | 1.4%短縮 | 0.877秒 | 5.9%短縮 | 6,786,558 |
| p4＋同時HLS | 1.624秒 | 2.7%短縮 | 0.809秒 | 13.1%短縮 | 6,375,297 |

CPU20%基準は1.641〜2.002秒、teeは1.557〜1.633秒。制限なし基準は0.908〜1.027秒、teeは0.801〜0.810秒。n=3の探索的結果であり、統計的に確定した改善率ではない。Pythonの入力供給・FFmpeg起動からMP4/HLS終了までを測っており、Go製品経路の値と混ぜない。

各4方式のCPU20%代表出力を検査し、180frame、PTS、MP4/HLS decode、AUあたり1スライス、HLS 2 segment・先頭keyframeを確認した。p4分離/teeのHLSは音声283packetがすべてpayload一致、先頭で正規化したPTSも一致した。絶対PTSに1.4秒の共通オフセット差があるが、両方とも音声開始は映像開始より21.333ms早い。全画素・全素材・実プレイヤーの同等性は別途検査する。

segment単位のffprobe推定durationから計算した音声長は分離5.952秒、tee6.037333秒となったが、実packet列は一致した。採用検査ではこの推定durationのみで欠落・同期不良を判定せず、packet PTS/durationと復号音声を比較する。

証拠:

- `build/nico-cpu20/four-options-audit-20260922/probe.py`
- `build/nico-cpu20/four-options-audit-20260922/runs/summary.json`（32run、24計測＋8warmup）
- 同ディレクトリの`rows.json`、各runの`argv.json`、`encode.stderr`、CPU20%の`job.json`、代表`validation.json`
- `build/nico-cpu20/four-options-audit-20260922/runs/audio-packet-comparison.json`
- FFmpeg: `8.1.1-full_build-www.gyan.dev`、SHA256 `09948d4cdd0650da6ff5a87577469f2a218dc2615ae379f8f734d24c49de0f73`
- GPU: RTX 5070 Ti、driver 610.47、VRAM 16,303MiB。今回の短いrunではVRChat frame-timeを計測していない。

## 3. 共通制約・採用条件

### 変更しない契約

- 通常利用はVRChat併用、書き出し関連プロセス全体をCPU約20%のJob配下に置く。無制限測定は補助診断。
- 既定encoderは現在のx264、NVENCは明示指定のまま。今回の計画だけを理由に既定を変更しない。
- NVENC候補の対照はp4/HQ/CQ29/B=3。サーバーは現在`preset.CRF`を渡しており、常にCQ29ではない。UIの各品質プリセットとworker実効値をT0で記録し、CRF=CQ同品質という仮定をしない。
- frame clockは`N=ceil(duration*fps)`、PTSは有理数で`index*den/num`。コメント省略、解像度・fps低下、低bit字形量子化は行わない。
- コメントのRGBA・PMA（premultiplied alpha）、重なり順、縁取り、clip、論理キャンバス、16:9/4:3/1:1の幾何を保持。
- 色行列、range、chroma位置、補間、alpha解釈を同じFFmpeg基準に固定する。metadataだけ同じにして画素検査を省略しない。
- H.264は全AUでVCL NALが厳密に1つ。x264既存ポリシーを保持し、NVENCも実ビットストリームで確認。
- AAC、4秒境界keyframe、MP4 faststart、HLS VOD/ENDLIST、音声なし入力、短い音声末尾、キャンセルを維持。
- 成功通知とライブラリー公開はMP4・HLS両方が検証済みになってから。片方だけ完成した状態を成功扱いしない。
- 共有dirty checkoutを保持。commit/push/release、稼働中アプリの差し替え、他の配信の停止は本計画の実施に含めない。

### 計画上の採用基準（性能目標であって実測済みの保証ではない）

- tee: 実worker CPU20%で対照より中央値5%以上短縮、または155秒素材で0.25秒以上短縮し、95パーセンタイルとCPU総量が悪化しない。満たさなければ分離HLSを維持する。
- GPU直結: 実worker CPU20%で中央値10%以上短縮し、関連CPU総量を増やさない。6秒だけで決めず155秒・高密度を含める。
- 探索は短尺5ペア以上、長尺各3回とし、対照/候補をAB/BA交互に実行する。この回数ではp95非悪化や小さな効果を確定しない。最終判定は素材・起動条件ごとに40ペア以上へ増やし、中央値、範囲、ペア差、p95を保存する。途中結果を見て有利な時点だけで打ち切らず、回数を測定前に固定する。
- 改善の不確実性はペア単位bootstrap（10,000再標本、固定seed、95%区間）で評価する。teeの5%または0.25秒、GPUの10%という点推定の目標に加え、対応するペア差中央値の区間下限が0を超えることを必要とする。p95は線形補間のtype 7で算出し、ペアを再標本化した「候補p95−対照p95」の区間上限が0以下の場合のみ非悪化を確認済みとする。区間が広い、測定回数不足、負荷条件が揃わない場合は採用保留とし、差がない／高速化したとは断定しない。
- 合成RGBAと最終YUVの基準差は各成分最大1/255以内を初期gateとする。過去GPU案のU/V最大13差は合格にしない。文字ROI・色境界・半透明文字の差分も確認する。許容条件の変更が必要なら測定画像と理由を提示し、勝手に緩めない。
- 圧縮後は基準となる圧縮前映像との全画面/文字ROI SSIMを取り、基準p4から0.001を超えて低下しない。容量増加は5%以内を初期gateとする。SSIMだけでコメント欠落や同期を合格にしない。
- VRChatは同じワールド・描画条件で書き出しなし/対照/候補を比較。全体CPU、Job CPU・実CPU%、GPU 3D/Video Encode/Copy、VRAM、VRChat p95/p99 frame timeを記録。frame time悪化は5%以内を初期gateとし、VRAM不足・カクつきが発生する候補は不採用。
- Job設定`2000`と実CPU観測を分ける。短いサンプルの20%超過も記録し、長尺の定常区間でCPU約20%内を確認する。Jobの設定成功だけで全区間の負荷条件を満たしたとは書かない。

### Review Focus

1. 半透明・色つき細字・PMA境界がYUV化で変色する → T3/T4の全画素とROI検査。
2. 29.97/60fps/VFR・短い音声で末尾映像が欠ける → T2/T4/T6のPTS/packet/decode検査。
3. エンコーダーが保持中のD3D11フレームを再利用し、遅れて画面が化ける → T3の保持参照/EAGAIN/flush故障試験。
4. HLS片側失敗やキャンセルで不完全な出力を公開する → T2/T5のstaging/公開回帰試験。
5. GPUは速いがVRChatのフレーム時間が悪化する → T6の同時実機受入れ。

## 4. 採用する設計

### 小規模候補: 一度の符号化からMP4とHLSへ出力

既存`nicoEncodeArgs`の入力・filter・encoder設定を共有し、出力部分だけ切り替える。既定`separate`、明示`tee`の二つに限定する。teeは`use_fifo=0`の同期出力、両slaveとも`onfail=abort`。復旧queueや再エンコードを追加しない。

専用作業ディレクトリの中でASCIIの固定ファイル名を使い、入力は絶対パス、出力は相対パスにする。Windowsのdrive colonや日本語をteeの多重escape文字列へ埋め込まない。生成後のHLSは、既存validatorが受け入れるsegmentのbasename参照にする。

```text
-flags:v +global_header -f tee
[f=mp4:movflags=+faststart:onfail=abort]out.mp4|[f=hls:hls_time=4:hls_playlist_type=vod:hls_flags=independent_segments:start_number=0:hls_segment_filename=segment-%05d.ts:onfail=abort]playlist.m3u8
```

上記は今回成立した診断引数。製品化時はMPEG-TSのSPS/PPS・Annex Bを実出力で確認し、使用するFFmpegに追加bsfが必要なら動画だけへ適用する。AACにもH.264用bsfを掛けない。`+global_header`変更後のMP4も検査する。

### 大規模候補: GPUリソースを保持するnative処理

```text
Go/Chrome: 字形・NPS3描画指示 ───────────┐
                                         ↓
native 1 process: source demux/decode/scale → 背景upload
                  → D3D11 PMA合成 → NV12色変換 → h264_nvenc → MP4
                  → 音声decode/AAC ──────────────────────┘
                                                               → HLS
```

- 既存WARP ABIへ機能を混ぜず、独立helperで試作する。同じD3D11 hardware deviceでcompositorとencoderを初期化し、adapter LUIDを照合する。WARPをGPU成功として扱わない。
- 最初は既存と同じCPU decode/scaleを**helper内**で実行し、背景をuploadする。HW decodeは今回の必須作業にしない。背景をrawvideoで別processから送る経路は作らない。
- 合成後NV12を`AV_PIX_FMT_D3D11`のhardware frameとしてlibavcodecの`h264_nvenc`に渡す。`hw_frames_ctx`のformat=D3D11、sw_format=NV12。完成フレームを`hwdownload`/staging `Map`してCPUパイプへ戻す経路は性能候補に数えない。
- NV12 planeへの書き込みはadapterの実対応を短いself-testで確認する。shaderで中間Y/UV面を作る場合も最終NV12までのコピーをGPU内に限定し、全コピーbyte数を計測する。
- poolを8面固定にはしない。固定SDKの実効`surfaces`・`delay/async_depth`・lookahead・B-frame保持・producer保有数を記録して必要面数を算定する。FFmpeg 8.1.1ではB=3で内部16面になり得るため、「B+1面あれば十分」とは扱わない。外部poolは保守的に実効surfaces＋並べ替え保持数＋producer保有数を確保し、初期上限32面とする。計算値が上限を超えたら明示的に候補非対応とし、品質設定を暗黙に変更しない。
- encoderが参照中のframeは再利用しない。空きなし時はpacket受信を試すが、必要な入力面数に届かず出力も出ない状態を無期限backpressureで待たない。self-testでpool容量の4倍以上をEOS前に連続送信し、進行と返却を確認する。面数不足が検出された場合の増設は、処理を停止・全参照を解放して再初期化する場合だけ許し、上限超過や再試験失敗は候補不成立とする。GPU/encoder完了、EAGAIN、drain、device lost時の所有権を明示する。
- PTS/DTS付きAVPacketを保持してlibavformatでmuxする。生H.264 pipeを後からfpsだけでremuxしてB-frameのPTSを再構築する案は採らない。MP4/HLSへ配るpacketは`av_packet_ref`し、各streamのtime_baseへ個別にrescaleする。
- 品質検査専用readbackは許すが速度測定から外し、byte counterにも検査用途を明記する。機能検査を省略して速度だけ出さない。
- FFmpeg SDK/DLLは1セットへ固定し、同じ版・同じ色変換設定のCLI基準で比較する。現在のCLI 8.1.1と既存shared SDK 9.0.1を無条件に混ぜない。helperに必要なDLL、ライセンス、source案内、hash/ABI、PATH非依存起動をT5で検査する。

## 5. タスクと依存関係

### T0: 基準・計測境界・実worker試験を固定

- **id:** T0
- **depends_on:** []
- **owner:** メイン
- **location:** `scripts/experiments/nico-native-probe/ffmpeg_diagnostic.py`、`worker_t11_stability.py`、`environment.py`、`internal/video/niconico_timing.go`、`internal/video/niconico_encode.go`、`internal/video/niconico_timing_test.go`
- **description:** 過去の値の混在を解消し、実workerで比較できる基準と明細計測を用意する。
- **status:** 完了（実worker比較は暫定。baseline manifest valid=true）

- [ ] 現在HEAD `71dc49cd`（`v1.8.0-4-g71dc49cd`）とdirty差分を再確認し、worker/compositor/FFmpeg・DLL・fixture/font/bundleのhashとbuild tagを保存。既存変更を含むsnapshotを基準とし、HEADだけのworktreeを同じ基準と呼ばない。
- [ ] 新規runディレクトリを排他的作成。mode/variant/repeatごとにMP4とHLSを分離し、既存出力があれば停止する。free space確認、timeout、子processだけの確実な終了を入れる。巨大rawは作らず既存fixtureを再利用。
- [ ] `frame_source_next`の累積時間と`frame_pipe_write_blocking`の累積時間を別計測し、旧`frame_pipe_write`はaggregateとして維持する。nativeではsprite/compositor/FFmpegの寿命が重なる旨をreportに保持する。
- [ ] timingテストは遅延frame sourceと失敗sourceで値・順序・失敗時保存を確認。write側も時間付きWriterで分離を確認し、テスト実行時間の絶対閾値を性能の正しさに使わない。
- [ ] 原因が未確定のChrome/native初期化失敗を、isolated cache/TEMP・固定helper・request・stderrで切り分け、同じアプリEXEの`nico-export-worker`で6秒を完走させる。権限に起因する失敗は必要な承認下で再検証し、fallbackやsaved RGBA成功で置換しない。
- [ ] 診断RGBA、native/browserフルworker、起動初回/warmを別グループにする。p4/separate CPU20%を主対照、無制限を副対照としてbaseline manifestを作成。
- **validation:** FFmpeg/worker同一hash、出力180frame、PTS・音声packet・single-slice・HLS成功、各runに総CPUとJob CPUを保存。T0の依存解消は「比較条件と実workerの成功／失敗理由を記録したこと」とし、成功を偽装しない。実workerが完走しなければT1〜T4の隔離作業は進められるが、性能判定は暫定、T6の製品採用は保留。
- **produces:** `baseline-manifest.json`（hash、argv、実効品質、build tag、stage定義、valid/invalid理由）と新規runごとの一意な出力。

### T1: 出力方式・候補選択・stagingの共通契約

- **id:** T1
- **depends_on:** [T0]
- **owner:** メイン
- **location:** 新規`internal/video/niconico_output.go`、`internal/video/niconico_output_test.go`、`internal/nicoexportworker/protocol.go`、`internal/nicoexportworker/protocol_test.go`
- **description:** T2/T5が同じ出力/失敗契約で実装できるよう、追加optionとreportを限定する。
- **status:** 完了（契約・互換性・staging所有権を実装）

```go
type NicoOutputMode string
const (
    NicoOutputSeparate NicoOutputMode = "separate"
    NicoOutputTee NicoOutputMode = "tee"
)
type NicoExportOutput struct {
    Encode NicoEncodeReport
    HLS NicoHLSReport
    Mode NicoOutputMode
}
// Requestへ OutputMode string `json:"output_mode,omitempty"` を追加。
// 空はseparate。unknownは副作用前に拒否。既存呼出しの意味を変えない。
```

- [ ] 空/`separate`/`tee`とunknown拒否、旧requestの互換性をテストする。
- [ ] 出力はジョブが新しく作ったstagingに限定し、既存MP4、既存HLS、入力と出力の重複を拒否する。tee失敗時に既存ユーザーファイルを削除しない条件をテストで固定する。
- [ ] 新optionを既存設定へ暗黙反映しない。採用判定用requestからのみ指定可能にする。
- **validation:** protocol互換、無効指定はprocess起動前に拒否、既存成果物保存テストPASS。
- **produces:** 上記型・request fieldとstaging所有権契約。

### T2: 同時MP4/HLS出力を実workerへ追加

- **id:** T2
- **depends_on:** [T1]
- **owner:** Go/video担当（T3とファイル所有権を分離）
- **location:** `internal/video/niconico_output.go`、新規`internal/video/niconico_output_integration_test.go`、`internal/video/niconico_args.go`、`internal/video/niconico_args_test.go`、`internal/video/niconico_native.go`、`internal/video/niconico_pipeline.go`、`internal/nicoexportworker/worker.go`、`internal/nicoexportworker/worker_test.go`
- **description:** browser/native双方でencode処理と出力形式を共通化し、tee時の二重HLS生成を防止する。
- **status:** 完了・候補不採用（tee経路と共通Export/worker接続を実装。昇格CPU20%実workerで固定6秒・高密度10秒・実ソース155秒を各40ペア測定したが、全条件で採用閾値未達。既定separateは変更しない）

- [ ] 内部の入力/filter/encoder引数と出力sink引数を分離し、既存`nicoEncodeArgs`はseparateの互換wrapperとして残す。nativeのpending MP4名とteeの2成果物を混同しない。
- [ ] 共通export入口を追加する。戻り値のHLSが検証済みの場合だけworkerの`CreateNicoHLS`を省略し、`PrepareNicoHLSForID`は必ず実行する。

```go
func ExportNicoCommented(ctx context.Context, ffmpeg, source, output, hlsDir string,
    snapshot niconico.Snapshot, render nicorender.RenderOptions,
    enc NicoEncodeOptions, mode NicoOutputMode) (NicoExportOutput, nicorender.RenderReport, error)
```

- [ ] tee文字列へ任意のpathを埋め込まず、専用cwdの固定basenameで動作させる。stdoutは既存worker JSONL、FFmpegの診断はstderrへ限定する。
- [ ] `separate/tee`×browser/native×音声あり/なし・短音声、6秒/155秒、4秒境界前後、失敗・cancelを試験する。HLS slaveだけ失敗させたとき成功eventが出ないこと、部分出力が公開されないことを確認。
- [ ] 音声packet列・相対AV開始差・復号音声、MP4 faststart、HLS keyframe/SPS/PPS、全AU1sliceを確認する。絶対TS offsetの違いを音ズレと混同しない。
- [ ] T0条件で5ペアを探索測定し、teeを最終受入れへ進めるか記録する。不適合なら既定separateを維持し、T3を妨げない。最終回数・VRChat併用を含む採用判定はT6で行う。
- **validation:** artifactと失敗契約が成立し、探索で改善の見込みがあること。これは統合候補の判定であって製品採用ではない。経路失敗をonfail=ignoreで隠さない。
- **produces:** `ExportNicoCommented`、`tee_candidate=eligible/rejected/unavailable`の理由と証拠、検証した出力契約。

### T3: 同一プロセスD3D11→NVENCの隔離試作

- **id:** T3
- **depends_on:** [T0]
- **owner:** native/GPU担当
- **location:** 新規`scripts/experiments/nico-gpu-nvenc/main.cpp`、`frame_pool.h`、`compose.hlsl`、`build.ps1`、`run.py`。参照専用: `scripts/experiments/nico-gpu-compose/`、`scripts/experiments/nico-native-probe/`、`scripts/experiments/nico-mask-probe/`
- **description:** 本番WARPを変更せず、完成フレームのreadback/rawvideo pipeを省く経路の成立を確認する。
- **status:** unavailable（MSVC/NVENCヘッダ不足。隔離runnerで理由付き終了）

- [ ] 同版shared SDK/CLIのpairを固定し、DLL/version/hashをmanifestへ保存する。先に実texture登録・NVENC encodeをself-testし、実効surfaces/delay/lookaheadと外部pool面数を照合する。pool容量の4倍以上をEOS前に連続送信できることを確認し、不対応adapterや進行不能では明確なerrorを返す。
- [ ] NPS3と元動画をhelperで受け、既存相当CPU decode/scaleとPMA合成を同じプロセスへ配置する。stdoutはbounded status、メディア出力は専用ファイルとする。
- [ ] 同一deviceのNV12 textureを参照付きframe poolで管理する。初期設計の主要引数は次のとおり。

```cpp
codec->pix_fmt = AV_PIX_FMT_D3D11;
// AVHWFramesContext: format=D3D11, sw_format=NV12;
// AVFrame: data[0]=ID3D11Texture2D*, data[1]=array slice;
// hw_frames_ctxとAVBufferRefで所有権を保持し、受理後も解放まで再使用しない。
// EAGAINならpacketをdrainして同じframeを再送、EOSはnull frameでflush。
```

- [ ] 既存と同じGOP、p4、CQ、B=3、AACと有理数PTSでMP4を作成する。最初はHLS分離、T2が合格なら最終統合時にpacket fanoutを追加する。
- [ ] frame held/遅いencoder、EAGAIN、途中EOF、device lost、OOM、cancel、最後のB-frameをfault injectionし、全frameの寿命・順序・flush・解放を確認する。
- [ ] GPU→CPU image bytes、CPU pipe image bytes、background upload bytes、GPU内部copy bytesを別counterにする。前二つは性能runで0が必要。Chrome/NPS3の字形転送は残るため、システム全体をゼロコピーとは呼ばない。
- **validation:** pool容量超え連続encodeと180frame pipeline、PTS/1slice、bounded pool、resource保持試験。機能未成立ならGPU製品統合へ進まない。
- **produces:** hash固定の隔離helper、machine-readable countersと実出力。成立しなければ理由付き`rejected/unavailable`を成果物としてT3を閉じ、T4で非適用判定へ進む。GPU側の不成立だけでtee側を止めない。

### T4: GPU候補の品質・速度による統合可否判定

- **id:** T4
- **depends_on:** [T3]
- **owner:** メイン
- **location:** `scripts/experiments/nico-gpu-nvenc/run.py`、新規`verify.py`、`scripts/experiments/nico-native-probe/media_validation.py`、新規`docs/verification/niconico-comments/gpu-nvenc-resident-results.md`
- **description:** 前回のGPU合成不採用要因（転送増、色差）が解消したかを実測で判断する。
- **status:** 非適用（T3がunavailableのため、GPU品質・速度判定は実施不能。tee候補を妨げない）

- [ ] 同じsnapshot・背景・出力clockで、基準RGBA/最終YUVと候補を全frame比較。黒白・原色・色境界・半透明PMA・細字・高密度/CA・4:3/1:1を含める。
- [ ] 10bit/HDR・非対応色空間は自動的に8bitへ変換して通さず、候補適用対象外と記録。既存経路へ戻す条件を決める。
- [ ] 6秒5ペア、155秒各3回をCPU20%で実行し、次に無制限を補助測定。source準備、字形生成、helper/FFmpeg起動、音声、mux終了までを総時間に含める。
- [ ] 既存production経路と新helperの比較に加え、同じSDKを使うCPU参照の結果を保持し、FFmpeg版差と構造変更の寄与を混ぜない。
- [ ] この段階は隔離試作の品質・転送counterの成立と、CPU20%探索測定で10%以上の短縮の見込みを判定する。実worker統合後の速度、最終統計、VRChat併用はT6へ分離する。都合のよいfixtureだけを抽出しない。
- [ ] T3で機能不成立なら実施不能の理由を引き継いでGPU候補を`rejected/unavailable`として閉じる。これは合格ではないが、T5のteeだけの統合を妨げない。
- **validation:** 隔離品質/転送counterを満たし、速度の見込みがある場合だけ`eligible`。不確かな速度や未計測項目を製品採用の証拠にしない。品質比較器の`unmeasured`やSKIPは合格にしない。
- **produces:** `gpu_candidate=eligible/rejected/unavailable`、対応入力・adapter・SDKと証拠一覧。`eligible`はT5へ進める意味だけを持ち、製品採用を意味しない。

### T5: 適格な候補をworkerへ試験統合

- **id:** T5
- **depends_on:** [T1, T2, T4]
- **owner:** メイン（共有worker/videoファイルの統合を一人で担当）
- **location:** 新規`internal/video/niconico_gpu_export.go`、`internal/video/niconico_gpu_export_test.go`、`internal/nicoexportworker/worker.go`、`protocol.go`、`worker_test.go`、`internal/server/niconico_upload.go`、`internal/server/niconico_upload_test.go`、新規`native/nico-gpu-export/`、`scripts/build-nico-gpu-export.ps1`
- **description:** T2/T4でeligibleとなった方式だけを明示optionで呼び出し、budget・fail-closed・staging・公開を既存契約へ接続する。通常利用への採用はT6で決める。
- **status:** 完了（GPU統合は行わず、tee候補だけを明示opt-inで接続。T6で候補不採用を確定し、通常利用への自動昇格は行わない）

- [ ] T2/T4は合否にかかわらず理由付き判定が確定すれば依存解消とする。GPUがrejected/unavailableならGPU部分を非適用で閉じ、teeがeligibleの場合だけ統合する。両方非適格なら実装統合を行わず、既存経路維持という報告のためT6へ進む。
- [ ] GPU適用時はrequestへ`pipeline:"gpu-resident"`を追加し、旧値空/`legacy`は既存動作。encoder/render backendと矛盾する指定は拒否。新helperは別ABI・capability・manifestで管理し、既存WARP ABI成功と混同しない。
- [ ] helper、Chrome、必要ならHLS処理を同じCPU20% Jobに保持し、明示GPUでの失敗は結果へ原因を返す。renderer fallbackとencoder/pipeline fallbackを区別し、この段階では勝手な再レンダーを追加しない。
- [ ] 「開始→処理→MP4/HLS検証→成功event→公開」の順序を守り、取消・容量不足・旧revisionへの公開競合・GPU失敗で既存完成品が壊れないテストを追加する。
- [ ] 新SDK/DLLを使用する場合はライセンス通知・依存DLL・hash・ABI・PATHを除いた起動を検査し、既存配布への自動置換は行わない。
- **validation:** Go対象試験、native self-test、実worker完走、Job全子process収容、キャンセルで残存processなし、公開時だけ完成ファイルが現れる。
- **produces:** 明示選択できる候補ビルドと可逆なlegacy経路。

### T6: 最終回帰・VRChat併用受入れ

- **id:** T6
- **depends_on:** [T5]
- **owner:** メイン
- **location:** 既存`t11_*`/`worker_t11_stability.py`/`presentmon_report.py`、新規`docs/verification/niconico-comments/nvenc-next-acceptance.md`
- **description:** 単体ベンチマークから通常利用での採否へ進める。
- **status:** 完了・候補不採用（新形式のbaseline/candidateを昇格CPU20%で各40ペア再実行。3条件の統計が全て採用閾値未達となったため、計画の非適格分岐に従い既存経路維持を確定。通常利用のVRChat受入れPASSとは扱わない）

- [ ] 同じEXE・FFmpeg/SDK・source・snapshotでbaseline/candidateを比較。処理開始〜MP4/HLS準備完了を主時間として、ダウンロードは別記する。
- [ ] 720p/1080p、16:9/4:3/1:1、24/29.97/30/60/VFR、音声なし/短音声、空/通常/高密度/CA、cancel/再実行を対象試験で確認。
- [ ] VRChat起動中にbaseline→candidate→baselineの同じ負荷区間を記録し、CPU約20%、GPU/VRAM、p95/p99 frame-time、実プレイヤー表示・音声を確認。VRChat不在ならこの行だけ未実施として残す。
- [ ] 6秒の起動影響と155秒の定常区間を分ける。30分連続は候補が短尺/長尺gateを通った後に実施し、queue/VRAM増加と残存子processを検査。
- [ ] 適格候補がある場合は§3の最終反復数・信頼区間による判定を実施する。VRChat frame-timeは同条件の60秒以上の定常区間を各方式3区間以上取得し、フレーム数と区間別p95/p99も保存する。実行回数不足・VRChat不在・基準worker不成立は採用保留。候補が両方非適格なら不要な負荷試験をせず、見送り理由のみ確定する。
- [ ] 採用理由、未達項目、実効profile、バイナリhashを日本語で報告。従来の改善率に追加改善率を単純加算しない。
- **validation:** 共通gateすべてを満たす経路だけ採用可能。実機未実施なら試験ビルドとして報告し、通常利用合格とはしない。

## 6. 作業順・所有権

| Wave | 作業 | 次へ進む条件 |
|---|---|---|
| 0 | T0 | 比較条件・artifact所有権・実workerの状態が確定 |
| 1 | T1とT3 | T1はGo契約、T3は隔離nativeのみ。共通ファイルを編集しない |
| 2 | T2とT4 | T1/T3の各依存完了。GPU/FFmpegの重い計測は同時に実行しない |
| 3 | T5 | T2/T4がeligible/rejected/unavailableとして確定。非適格候補は統合しない |
| 4 | T6 | 適格候補の統合ビルド完成、または両候補見送りの判定が確定 |

`parallel-task`は計画受理後に未ブロックwaveだけを実行する。メインが各diff・証拠を確認し、次waveの開始を判断する。診断・品質・性能測定を並列実行して負荷を混ぜない。

## 7. 実行・検証上の注意

対象testは既存のpackage全体FAILを隠す手段にはせず、変更した契約の成否を先に確定する。以前の広範囲testではAppData台帳権限・一時ファイル占有・空き不足が発生しているため、専用cache/TEMP/設定ディレクトリとtimeoutを用意して実行する。起動済みの共有アプリ設定を試験で上書きしない。

```powershell
rtk proxy pwsh -NoProfile -Command '$env:GOCACHE=Join-Path (Get-Location).Path ".gocache-nico-next"; $env:GOTELEMETRY="off"; & ".tools/go-sdk/go/bin/go.exe" test -count=1 -timeout=120s ./internal/video -run "TestNico|TestEncodeNico|TestCreateNicoHLS|TestExportNico"; exit $LASTEXITCODE'
rtk proxy pwsh -NoProfile -Command '$env:GOCACHE=Join-Path (Get-Location).Path ".gocache-nico-next"; $env:GOTELEMETRY="off"; & ".tools/go-sdk/go/bin/go.exe" test -count=1 -timeout=120s ./internal/nicoexportworker ./internal/nicoexportbudget; exit $LASTEXITCODE'
rtk proxy pwsh -NoProfile -Command '$env:GOCACHE=Join-Path (Get-Location).Path ".gocache-nico-next"; $env:GOTELEMETRY="off"; & ".tools/go-sdk/go/bin/go.exe" test -count=1 -timeout=120s ./internal/server -run "TestNico|TestRunNico|TestCleanupAbandonedNico"; exit $LASTEXITCODE'
```

上記に加え、native変更はT3/T5の実self-test・fault injection・固定SDKビルドを必須とする。GoのmockだけでGPU共有を合格としない。FFmpeg/Chromeを要する任意integration testは環境指定を記録し、skipをPASSに含めない。

## 8. 一次資料・既存の根拠

- FFmpeg pixel formats: https://raw.githubusercontent.com/FFmpeg/FFmpeg/n8.1.1/libavutil/pixfmt.h
- FFmpeg CUDA overlayの対応形式: https://raw.githubusercontent.com/FFmpeg/FFmpeg/n8.1.1/libavfilter/vf_overlay_cuda.c
- FFmpeg scale_cudaのRGB/YUV制約: https://ffmpeg.org/ffmpeg-filters.html#scale_005fcuda
- FFmpeg tee、slave失敗・global header・escaping: https://ffmpeg.org/ffmpeg-formats.html#tee
- FFmpeg D3D11 hardware frame→NVENC: https://raw.githubusercontent.com/FFmpeg/FFmpeg/n8.1.1/libavcodec/nvenc.c
- NVIDIA D3D11入力・外部resource登録・寿命: https://docs.nvidia.com/video-technologies/video-codec-sdk/13.0/nvenc-video-encoder-api-prog-guide/index.html
- 過去GPU案の不採用根拠: `docs/verification/niconico-comments/gpu-compose-2026-09-17.md`
- FFmpeg各段階の既存診断: `docs/verification/niconico-comments/ffmpeg-bottleneck-investigation-2026-09-22.md`

本書は一つの計画として、4を小規模候補、1+2を大規模候補に集約した。3は測定結果により除外した。GPU直結の改善率は未測定であり、何倍になるかは約束しない。

独立レビューの指摘（NVENC poolと非同期出力の循環待ち、試作／製品採用条件の依存循環、少数標本でのp95判定）を確認し、本書へ反映した。実装・採用結果ではなく計画の検査である。
