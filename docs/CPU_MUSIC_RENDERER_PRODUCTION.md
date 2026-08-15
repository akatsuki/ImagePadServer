# CPU Music Renderer Production Decision

## Status

**Decision:** CPU rendererを音楽visualizerのproduction経路として固定する。

**GPU status:** GPU direct H.264 / RenderV2 / surface-ring開発はproduction採用せず、技術実証・diagnostic routeとして凍結する。`production_ready=false`を維持し、GPU routeをproduction callerから自動選択しない。

**Scope of this decision:** この文書は、GPU開発を継続して完成させる計画ではない。CPU productionの回帰防止、GPU実証結果の保存、不要な一時物の整理、将来再開時の条件を定義する。

## Production contract

### CPU route

通常のproduction callerは次のCPU entrypointを使用する。

- `internal/video/radio_render.go:RenderRadioTrack`
  - `renderRadioTrackCPU`へ固定
  - canonical CPU visualizer pipeline (`runAudioVisualizerEncode`) を再利用
- `internal/video/audio_visualizer.go:RunAudioVisualizerHLS`
  - `RunAudioVisualizerHLSCPUReference`へ固定
  - CPU base artwork、spectrum/waveform/loudness、ASS、FFmpeg encode/muxを使用
- CPU visualizer pipelineのencoder選択
  - `CPUVideoEncoder`を明示使用
  - GPU capability、sidecar path、encoder modeによってproduction outputが変化しない

`IMAGEPAD_PLAYLIST_COMPOSITORD`が設定されていても、通常のHLS/playlist callerはsidecarを起動しない。`IMAGEPAD_GPU_H264_REQUIRED`や`IMAGEPAD_GPU_H264_DRAFT`はproduction callerの選択条件ではない。

### GPU diagnostic route

GPU処理は明示的な実験入口からのみ利用する。

- HLS diagnostic: `RunAudioVisualizerHLSGPUExperimental`
- direct H.264 diagnostic: GPU H.264 sidecarの明示実行経路
- GPU shader descriptor: `production_ready=false`
- GPU readiness / capability / asset receipt / pixel-readback-zero contractはfail-closedのまま保持

GPU diagnostic routeは、CPU productionの暗黙fallbackではない。診断で失敗してもproductionがGPUへ切り替わってはならない。

## CPU production improvements included

1. canonical scene、artwork decode、palette、glyph atlas、loudness、layout、metadataをframe loop外で再利用する既存改善を維持する。
2. `RenderRadioTrack`をGPU sidecar必須からCPU visualizer pipelineへ切り替える。
3. 通常HLS callerがstale/invalid GPU sidecar環境変数を無視する回帰テストを追加する。
4. CPU MP4 recipeへBT.709 limited-range metadataを追加する。
   - `pix_fmt=yuv420p`
   - `color_range=tv`
   - `color_space=bt709`
   - `color_transfer=bt709`
   - `color_primaries=bt709`
   - H.264 bitstream filterで`colour_primaries=1`、`transfer_characteristics=1`、`matrix_coefficients=1`、`video_full_range_flag=0`を明示
5. CPU production smokeが実際のaudio analysisを使い、output生成・ffprobe metadata検証まで行うようにする。
6. production entrypointがGPU sourceを読まないことを、実行テストとsource guardの両方で検証する。

## Confirmed GPU technical results

以下はGPUがproduction readyになったことを意味しない。今回のGPU実験で確認できた技術的事実だけを記録する。

### D3D12 surface to NVENC handoff

- wgpu/D3D12 queueがsurface accessに必要なD3D12 fenceをsignalする。
- NVENCが同じfenceをwaitしてからinput resourceを使用する。
- CPU側で取得するのはcompressed H.264 bitstreamであり、GPU textureのCPU map/readbackは行わない。
- handoffのresource lifetime、delayed output、全error pathのproduction-level proofは未完了。

### No GPU texture readback

direct H.264 contractで`pixel_readback_bytes=0`を検証した。これは「GPU textureをCPUへreadbackせず圧縮bitstreamを取得できる」ことの実証であり、CPU/GPU pixel parityやproduction品質を保証するものではない。

### Bounded batch hardware E2E

RTX 5070 Ti / DX12 / NVENCで、明示的なdiagnostic routeを使用して次を確認した。

- input: 正しい10秒PCM素材
- frame count: 301
- batch count: 38
- maximum batch size: 8
- resolution: 640x360
- codec/profile: H.264 High
- pixel format: `yuv420p`
- frame rate: 30/1 fps
- duration: 約10.033233秒
- packet PTS: first `0.000000`, last `9.999900`
- video/audio FFmpeg decode: pass
- GPU diagnostic artifact間のdecoded RGB24 SHA-256:
  `245ea89e4b127a675a0537cfdf481b9b1a44d4fd30d37b57a33c2f9c860312b7`

このbatchはbounded transport batchである。batch内frameのtrue multi-frame in-flight、surface ring、render/encode overlapを実証したものではない。

### CPU/GPU parity result

同一scene、PCM、metadata、artwork、解像度、fps、frame indexを揃えた比較では、CPUとGPUのpixel parityは失敗した。

- CPU reference decoded RGB24 SHA-256:
  `fd8056a1cb01bdb8e9f49ff807505f24b2be120295b3ea84da13b89a56bb46f6`
- GPU decoded RGB24 SHA-256:
  `245ea89e4b127a675a0537cfdf481b9b1a44d4fd30d37b57a33c2f9c860312b7`
- differing frames: `301/301`
- MAE: `135.529303152`
- RMSE: `145.553927436`
- PSNR: `4.870325041 dB`
- maximum absolute error: `255`

これは圧縮誤差だけでは説明できない描画内容差分であり、GPU routeをproduction quality-equivalentとは扱わない。

## ROI decision

測定値は同一scene/PCM/metadata/artwork条件の比較として保存する。ただし`go run`のwall-clockをshader性能と解釈せず、OneDrive、Defender、process initialization、GPU timestamp query分離の限界を併記する。

| Path | Whole wall-clock |
|---|---:|
| CPU HLS reference | `2.014923 s` |
| GPU direct H.264 sequential | `4.928091 s` |
| GPU bounded transport batch comparison | `3.8662985 s` |
| Latest diagnostic draft run | `3.383525300 s` |

Batch化の単一比較ではRPC回数が`301 -> 38`、全体wall-clockが`4.9280905 s -> 3.8662985 s`となった。しかしinit時間・storage・Defender等の変動が分離されておらず、GPUがCPUより高速である根拠にはしない。

判定は次の通り。

- GPUはD3D12/NVENC interopの技術実証として価値がある。
- 現状のGPUはCPUよりproduction品質が高くなく、速くもなく、保守対象が大きい。
- parity修正、true in-flight ring、RenderV2 caller、asset recovery、partial retry、EOS、cleanup proofを追加しても、現時点のproduction価値に対するROIが低い。
- production engineeringの境界は、**handoffが可能か**ではなく、**同一出力品質、総合wall-clock、再現性、failure recovery、保守コストを含めてCPUを上回れるか**で判定する。

## GPU work intentionally frozen

以下は今回の停止作業では実装しない。再開時の課題として記録する。

- true D3D12 surface ring
- per-slot independent fence
- delayed NVENC output中のresource reuse禁止のformal proof
- render/encode overlap
- RenderV2 production caller
- asset invalidation/reupload/resync
- frame-level partial failure and retry
- aggregate memory/backpressure proof
- full Flush/EOS hardware integration proof
- NVENC全error path fault-injection/leak proof
- CPU/GPU pixel parity修正
- GPU hardware smokeで判明したGo `nil` slice -> JSON `null` とRust empty sequence `[]`のwire defect修正

GPU再開は、次の条件を満たす新しい意思決定なしには行わない。

1. CPU baselineとGPU candidateを同一canonical fixtureで再生成できる。
2. pixel parityの受入基準を先に定義し、全frameで達成する。
3. GPU総合wall-clockがCPUを複数回の実測で上回る。
4. ring、fence、delayed output、EOS、resource lifetime、retryを実機で証明する。
5. production caller、CI、release、support/maintenance costを含む工数が妥当である。
6. `production_ready=true`を別途レビュー・承認する。

## Evidence and artifact policy

artifactそのものはcommitしない。削除・整理前に、次の値をこの文書へ転記済みとする。

- codec/profile/resolution/pixel format/fps/frame count/duration
- first/last PTS
- video/audio decode result
- CPU/GPU decoded hash
- parity metrics
- CPU/GPU timing
- GPU hardware/backend/driver条件
- diagnostic envとproduction routeの区別

対象となる一時物は、現treeを確認してから個別に整理する。

- `cmd/pcm-h264-e2e/main.go`
- generated `gpu/playlist-compositord/target/release/playlist-compositord.exe`
- `artifacts/soundcloud-odd/h264-sidecar-e2e-*`
- `artifacts/soundcloud-odd/parity-debug-*`
- 一時stage log / raw encode output

重要証拠を文書へ転記した後も、artifactを残す必要がある場合はgit管理外のdiagnostic保存物として扱い、commit対象にはしない。secret、credential、token、cookie、PCM sourceの権利制限素材を文書へコピーしない。

## Verification contract

CPU production cutover後のcanonical verificationは次を必須とする。

1. GPU関連envを外した`go test ./... -count=1`
2. CPU production smoke
   - output file exists and is non-empty
   - ffprobe codec/pixel/color metadata
   - expected frame count/fps/duration
   - video/audio decode pass
   - PTS first/last
3. `cargo fmt --manifest-path gpu/playlist-compositord/Cargo.toml`
4. `cargo test --release --locked --manifest-path gpu/playlist-compositord/Cargo.toml --all-targets`
5. `cargo build --release --locked --manifest-path gpu/playlist-compositord/Cargo.toml`
6. `git diff --check`
7. `git status --short`で未追跡artifact、生成exe、temporary harnessの扱いを確認

focused testの成功は全体suite greenとは表現しない。GPU diagnostic E2Eの成功はCPU production greenとは別の証拠として扱う。
