# GPU音楽映像生成シェーダー 要件定義（歴史資料・凍結）

> **Status (2026-08-11):** These are the requirements for a possible future
> GPU production restart, not the current production contract. The current
> production renderer is CPU-first; `production_ready=false` remains in force.
> The measured decision and GPU evidence are recorded in
> [`CPU_MUSIC_RENDERER_PRODUCTION.md`](CPU_MUSIC_RENDERER_PRODUCTION.md).

## 1. 最上位原則

### R0: 外部データ受信後のGPU完結

動画・音源をFFmpeg/FFprobeで抽出・解析し、PCMと各種データをGPUへ引き渡した後の
音楽映像生成処理は、GPU側で完結させる。

```text
動画 / 音源
    ↓
FFmpeg: 音声・画像・必要な素材の抽出
    ↓
FFprobe: コンテナ・stream・duration・codec・metadata等の解析
    ↓
PCM + 各種データ
    ↓
GPU upload / VRAM resource化
    ↓
GPU shader: 解析、正規化、レイアウト、描画、合成、色変換
    ↓
VRAM内の完成映像データ
    ↓
GPU対応エンコーダー
    ↓
配信 / 保存
```

FFmpeg/FFprobeによる入力素材の抽出・probeはGPU処理前の前処理として許可する。
ただし、PCMと各種データがGPU境界を越えた後に、CPUで音楽特徴量・画像・文字・
画面を加工してはならない。

CPUへの差し戻しは原則禁止する。最終アーキテクチャでは、GPUで完成した映像を
VRAM上のままGPU対応エンコーダーへ渡す。CPU readbackは完成要件に含めない。

### 許可されるCPU処理

GPU境界より前に許可される処理：

- FFmpegによる音声・画像・素材の抽出
- FFprobeによるコンテナ、stream、duration、codec、metadata等の解析

PCM + 各種データをGPUへ引き渡した後にCPUで許可される処理は以下に限定する。

- JSONL/制御プロトコルの構文解析
- payloadのサイズ、stride、schema、hash、有限値、範囲の検証
- 入力バイト列をGPU staging bufferまたはGPU textureへコピーすること
- GPU command submission、fence、timeout、lifecycle管理
- GPU/adapter/backendの診断情報とreceiptの収集

### 禁止されるCPU処理

本番GPU経路では以下を禁止する。

- CPUでのPCM解析、FFT、RMS、peak、waveform、loudness計算
- CPUでのaudio feature正規化、palette計算、progress/fade計算
- CPUでのPNG/JPEG等の画像展開・resize・crop・blur・色変換
- CPUでのfallback artworkのgradient、fingerprint、note生成
- CPUでのglyph rasterization、ASS画面ラスタライズ、text overlay合成
- CPUでのspectrum、waveform、loudness、progress、fadeの描画
- CPUでのRGBA→YUV変換、chroma subsampling、limited-range matrix適用
- GPU frameのCPU readback
- GPUで完成したframeのCPU経由FFmpeg pipeへの引き渡し（最終構成）
- GPU frameのCPU readback後の再合成、補正、再エンコード
- sidecar失敗時のCPU rendererへの暗黙フォールバック

現行の別プロセスFFmpeg連携に残るGPU→CPU plane transportは移行用compatibility
経路に限る。正式な完成構成ではreadbackを発生させず、VRAM上の完成surfaceまたは
GPU-owned planeをGPU対応エンコーダーへ直接渡す。

## 2. 現在の実装に対する判定

| 項目 | 現在の実装 | R0への判定 |
|---|---|---|
| sidecarのGPU compute compositor | `gpu_render.rs`に実装 | 部分達成 |
| GPU artwork/background/blur | 実装済み。ただし入力textureの準備はCPU | 部分達成 |
| GPU spectrum/waveform/loudness/progress | shader実装あり | 部分達成 |
| GPU RGBA→YUV420P | compute shader実装あり | 部分達成（直接encoder handoff未接続） |
| GPU-only本番route | CPU fallbackを禁止 | 達成 |
| CPU `CanonicalMusicScene`でfeature生成 | Go側で実行 | 未達 |
| CPU `AnalyzeAudioForKind` | CPUでPCM解析 | 未達 |
| CPU glyph atlas生成 | `prepareExactGlyphAtlas`等で実行 | 未達 |
| CPU fallback note mask生成 | `fallbackNoteMaskMetadata`で実行 | 未達 |
| CPU palette/dynamics生成 | `canonicalScenePalette` / `musicSceneDynamics` | 未達 |
| `RenderV2` pass graph | job/descriptor validation後にshader readiness gateまで到達 | native execution未達 |
| GPU shader host boundary | binding layout、uniform packing、resource binding、dispatch planを実装 | sidecar execution未接続 |
| GPU-owned YUV encoder sink | 型・境界のみ | 未達 |
| Draft 2 shader/pass implementation | module interface経由で差し替え可能、diagnostic exporterで実GPU実行 | production未接続 |

### Draft 2の位置づけ

`gpu/playlist-compositord/src/music_v2_shader_draft.rs` は、シェーダーデザインを
修正しやすくするための**未接続Draft 2**である。production rendererや
`RenderV2` dispatchからは呼ばれないが、diagnostic exporterでは実GPUで実行される。
CPU版に寄せる最初のparity sliceとして、hostからCPU-like scene payloadを受け、
canonical layout/palette/24-band/loudness/progressを描画する。

Draft 2で固定するのは実装完成度ではなく、差し替え可能な境界だけである。

- PCMを`var<storage, read>`で受けるresource binding
- Scene uniform
- GPU-owned luma/chroma storage output
- wgpu portable storage physical formatはluma/chromaとも`Rgba8Unorm`。lumaはR、chromaはR/Gへ格納する
- `CANONICAL_ORDER`と一致する9 passの実処理
- CPU readback/`PackedBytes`/RGBA transportをshader契約へ持ち込まないこと

Draft 2で実装した処理:

- bounded PCM windowからのRMS/peak算出
- CPU canonical layout（artwork/title/artist/album/spectrum/loudness/progress/time rect）
- CPU-like paletteとfallback artwork（gradient/fingerprint/note）のGPU描画
- host payloadの24-band spectrum barと固定bottom fade
- PCM waveformをcanonical spectrum rect内へ描画
- host payloadの1000-point loudness graphと4本のguide line
- metadata領域のuniform codepoint列とfontdue由来metricsをGPU-readableなNoto Sans JP RGBA8 glyph atlasへlookupして描画（title=SemiBold、artist/time=Medium、album=Regular）、filtering samplerでalphaを合成、progress rail/thumb、vignette/fade
- luma pixelのRGB描画と2x2平均によるchroma生成

Draft V2のfont resource bindingは次の順序で固定する。

| Binding | Resource |
|---:|---|
| 0 | resident PCM storage |
| 1 | scene uniform（codepoint列・rect・palette・timing） |
| 2 | Noto Sans JP RGBA8 glyph atlas |
| 3 | fontdue glyph advance Q16 storage（style×codepoint） |
| 4 | glyph filtering sampler |
| 5 | luma output |
| 6 | chroma output |

CPU parity sliceはまだdiagnostic用の決定的fixtureであり、実曲sceneの完全接続ではない。
以下はまだ未完成で、次の設計修正対象である。

- 実artwork texture samplingとGPU asset upload
- production用のGPU FFT/24-band spectrum、GPU loudness normalization/trend
- Go側`CanonicalMusicScene`から渡される実title/artist/album/time、Unicode shaping、scroll、clip、missing glyph
- Go側`CanonicalMusicScene`からRust sidecarへのscene payload接続
- shader本文のvisual/numerical parity
- sidecarでのnative pass executionとGPU PCM resource upload
- GPU-owned完成planeからencoder sinkへのproduction接続

`R8Unorm`/`Rg8Unorm`は実GPU backendによってstorage textureとして拒否されるため、
physical storage formatは`Rgba8Unorm`に固定する。logical Y/U/V planeの契約は維持し、
diagnostic exporterだけがR/RG channelからpacked YUV420Pへ変換してMP4化する。

CPU parity sliceの実GPU確認例:

```text
GPU: NVIDIA GeForce RTX 5070 Ti
Backend: Vulkan
Resolution: 1280x720 (canonical 1:1 diagnostic output)
FPS: 12
Frames: 12
Pixel format: yuv420p
```

確認artifact: `artifacts/draft-shader-cpu-parity-slice.mp4` と
`artifacts/draft-shader-cpu-parity-slice-contact-sheet.png`。
これはCPU referenceとの差分計測完了を意味せず、canonical region構成が
shaderで描画可能になったことのdiagnostic evidenceである。

### Diagnostic MP4 export

サーバーを起動せず、Draft WGSLだけを実GPUでdispatchしてMP4と確認用frameを生成する。

```bash
python3 gpu/playlist-compositord/scripts/render_draft_shader_mp4.py \
  --width 640 --height 360 --fps 30 --duration 4 \
  --output artifacts/draft-shader-preview.mp4
```

このスクリプトは次を生成する。

- `draft-shader-preview.mp4`: FFmpegでmuxしたYUV420P MP4
- `draft-shader-preview_frames/`: 肉眼確認用PNG frame群
- `draft-shader-preview.json`: shader module、GPU実行、解像度、FPS、frame数、ffprobe結果
- `--keep-yuv`指定時のみ、diagnostic用packed YUV420P中間ファイル

Rust側の`--export-draft-shader-yuv`は、production rendererや常駐serverを通らず、
`DRAFT_SHADER_MODULE`をwgpuで直接コンパイル・dispatchする。readbackとCPU YUV packingは
このdiagnostic exportに限定し、本番GPU経路の完成条件とは別に扱う。

根拠:

- Go側のCPU scene生成: `internal/video/music_scene.go:22-85, 169-190, 246-358`
- CPU音声解析: `internal/video/audio_analysis.go`
- GPU compositor: `gpu/playlist-compositord/src/gpu_render.rs:13-647, 1016-1827`
- GPU YUV compute: `gpu/playlist-compositord/src/gpu_render.rs:666-685, 1393-1456`
- `RenderV2` shader readiness gate: `gpu/playlist-compositord/src/main.rs:120-165`
- shader module interface: `gpu/playlist-compositord/src/music_v2_shader_module.rs`
- shader host boundary: `gpu/playlist-compositord/src/music_v2_shader_host.rs`
- Draft 2 shader: `gpu/playlist-compositord/src/music_v2_shader_draft.rs`

## 3. 対象範囲

対象は以下のGPU音楽映像生成である。

- 単曲HLSの音楽visualizer
- playlist/radio trackのMP4
- 30fpsの動的音楽映像
- RGBAまたはGPU生成YUV420Pの出力
- artwork source/fallback
- title、artist、album、time
- spectrum、waveform、loudness、progress、fade

CPU reference rendererは、GPU parity比較のgolden sourceとして残すが、本番GPU
routeの入力データ生成元にはしない。

## 4. 目標データフロー

### 4.1 正式な本番目標

```text
動画 / 音源
    │
    ├─ FFmpeg: PCM・画像・必要素材の抽出
    │
    └─ FFprobe: stream・duration・codec・metadata等の解析
             │
             ▼
       PCM + 各種データ
             │
             ▼
       GPU upload / VRAM resource化
             │
             ├─ Audio ingest / feature compute
             │    ├─ PCM windowing
             │    ├─ FFT / 24-band spectrum
             │    ├─ RMS / peak
             │    ├─ waveform envelope
             │    ├─ 1000-sample loudness envelope
             │    └─ normalization
             │
             ├─ Asset preprocess
             │    ├─ GPU decodeまたはGPU-readable asset
             │    ├─ cover crop
             │    ├─ blur
             │    ├─ fallback gradient/fingerprint/note
             │    └─ palette/readability selection
             │
             ├─ Scene composition
             │    ├─ background
             │    ├─ artwork
             │    ├─ spectrum
             │    ├─ waveform
             │    ├─ loudness
             │    ├─ GPU text
             │    ├─ progress
             │    └─ fade
             │
             ├─ VRAM内完成映像
             │    ├─ RGBAまたはencoder-native surface
             │    └─ GPU生成YUV420P plane
             │
             └─ GPU対応エンコーダー
                    └─ 配信 / 保存
```

### 4.2 FFmpeg/FFprobeとGPUの境界

FFmpeg/FFprobeは、GPUへ渡す前の素材抽出・probe境界で停止する。GPU境界以降に
FFmpeg filterでwaveform・文字・映像合成を再実行してはならない。

GPUへ渡す最低限のデータは次のとおり。

- PCM sample buffer
- FFprobeで解析したduration、stream、codec、metadata
- artworkのGPU-readable payloadまたはGPU decoder descriptor
- text/fontのGPU-readable payloadまたはGPU font descriptor
- frame index、PTS、width、height、FPS
- scene schema、asset hash、ownership情報

FFprobeが返すmetadataと、GPUがPCMから計算するdynamic featureは区別する。
FFprobe/CPU側で計算済みのspectrum、RMS、waveform、loudnessをproductionの
唯一の入力にしてはならない。

### 4.3 Pass order

`gpu/playlist-compositord/src/music_v2_pass_graph.rs:1-34` にある順序を基本とする。

```text
ArtworkPreprocess
Background
Spectrum
Waveform
Loudness
TextUi
Progress
Fade
Yuv420
```

`Yuv420` またはencoder-native surfaceは唯一の最終sinkとする。完成映像をCPUへ
戻してからエンコーダーへ渡す経路は正式な完成構成に含めない。

## 5. 入力・所有権契約

### 5.1 制御plane

CPUからGPUへ渡す制御情報は、画面生成をCPUで完了させるためのラスタではなく、
GPUが処理するための bounded descriptor でなければならない。

最低限の制御情報：

- schema/version
- frame index
- PTS ns
- width / height / FPS
- artwork mode（SourceまたはFallback）
- audio source descriptor
- artwork source descriptor
- text/font descriptor
- canonical layout descriptor
- fade/progress timing descriptor
- asset hash / provenance

### 5.2 GPU入力資源

GPUは以下を自分のresourceとして所有・処理する。

- PCMまたはGPUが読める音声sample buffer
- artwork textureまたはGPU decoderの出力
- font dataまたはGPU glyph resource
- UTF-8 text metadata
- fingerprint/sample buffers
- frame timing
- scene constants

`MusicScenePayload` の現行 `spectrum_q16`、`rms_q15`、`peak_q15`、
`loudness_envelope`、`palette` は、最終的にはCPUで計算済みの値を本番の唯一の
真実としない。互換期間中は parity diagnostic input として扱い、GPUで再計算した
値をproduction outputに使用する。

### 5.3 CPU-final-raster禁止

以下のpayloadをproduction V2 requestへ渡してはならない。

- `screen_rgba` の画面全体
- CPUで合成済みのfull-frame `base_texture`
- CPUで合成済みのwaveform/loudness/spectrum texture
- CPUで焼き込んだASS/text overlay
- CPUで最終画素化したfallback artwork

現在の `MusicRenderV2Job.ValidateProduction` が `GoldenUpload` と
`screen_rgba` を拒否する方針を維持する。

根拠: `internal/video/music_render_v2_contract.go:1-77`

## 6. シェーダー段階別要件

### R1: Audio ingest / feature compute

GPU側で外部音声データから以下を計算する。

- 48kHz音声sampleへの対応
- 30fps render tickとの同期
- 8192-sample FFT window相当
- 24 spectrum bands
- RMS / peak
- waveform表示用のbounded sample history
- 1000-point relative loudness envelope
- loudness trend
- Q16/Q15相当の正規化

CPUの `AnalyzeAudioForKind` が生成した値をそのままproduction shader入力にする
経路は、R0の最終形では禁止する。

GPU上の音声bufferは以下を満たす。

- frameごとの再uploadを避ける
- bounded ringまたはresident storage bufferを使う
- frame index/PTSとfeature windowを同じGPU descriptorで識別する
- 旧frameのfeatureを誤って描画しない
- PTSの単調増加を検証する

### R2: Artwork preprocess

GPU側で以下を行う。

- source artworkのcover scale/crop
- artwork rectangleへの配置
- 背景用のcover texture生成
- `gblur=sigma=64`相当のblur
- rounded artwork mask
- shadow
- artworkからのpalette/readability判定
- artworkなし時のfallback gradient/fingerprint/note

CPUの `fallbackNoteMaskMetadata` は現行ではglyph mask生成をCPUで行うため、
strict GPU要件では移行対象である。fallback noteは、GPUでvector/SDF/analytical
shapeとして生成するか、GPUが解釈できる非ラスタdescriptorで渡す。

### R3: Background

- palette.backgroundを基礎色にする
- blur結果とreadability overlayを合成する
- fixed cyan等の診断色をproduction出力に残さない
- opaque outputを生成する
- sRGB/linearの境界を明示する
- artwork textureとglyph textureでsampler policyを混同しない

現行実装ではartworkはlinear sampling、Draft V2のglyph atlasもfiltering samplerで
alphaをlinear samplingする。glyph cell境界のpaddingを維持し、atlas隣接glyphの
bleedingを避ける。根拠: `gpu/playlist-compositord/src/gpu_render.rs:2797-2817`

### R4: Spectrum

- `Spectrum24` を24 bandとして扱う
- Q16を0..1へ変換する
- Spectrum矩形へbar/line/glowを配置する
- RMS/peakをglow強度へ反映する
- frame indexと一致するfeatureのみを使う
- spectrum-only probeをproduction branchに混入させない

現行の `internal/video/shaders/music_visualizer.wgsl` は最小のspectrum/glow probe
であり、production shaderの完成形ではない。

### R5: Waveform

- CPU `showwaves`のhistory semanticsと一致させる
- 30fpsのwaveform geometryを維持する
- line modeを維持する
- signed center、min/max、raw stereoの各入力形式を明示的に区別する
- CPUでwaveform textureを生成しない
- Q16 waveform bufferをGPU storage bufferとして読む

現行Rust shaderには、legacy waveform textureとbounded `waveform_q16` storage
bufferの両方があり、feature bitsで経路を切り替えている。
根拠: `gpu/playlist-compositord/src/gpu_render.rs:1263-1301, 1665-1682`

### R6: Loudness

- 1000サンプルのrelative loudnessを使用する
- CPUと同じ曲内ピーク基準の正規化を再現する
- trendを補間する
- 4本のguide lineを同じ位置へ描画する
- supersampled/Lanczos parityの差分を測定可能にする
- loudness layerをGPU上で生成し、CPU textureとして渡さない

現行実装ではhost側でtrend sampleをbuffer化し、shader内でsupersampled loudnessを
再構成する部分がある。R0の最終形では、normalization/trend生成自体もGPUへ移す。
根拠: `gpu/playlist-compositord/src/gpu_render.rs:1640-1662, 3336-3341`

### R7: Text UI

- title、artist、album、timeをGPUで配置・合成する
- 3秒停止後40 canonical px/sのscrollをGPUで計算する
- clip rectangleをGPUで適用する
- font fallback orderを保持する
- missing glyphをdeterministicに表示する
- repeated glyphをunique atlas countで切り捨てない
- CPUで画面全体をラスタライズしない

現行のglyph atlasは一部がCPU側でrasterize済みのsource textureであり、最終位置と
blendはGPUで行う設計である。しかしR0のstrict解釈では、font受信後のglyph生成も
GPU化が必要となる。移行中のCPU atlasはdiagnostic/parity専用とし、production
V2のinputにはしない。

### R8: Progress / fade

- progress railをProgress矩形へ配置する
- thumb位置をGPUで `progress_ratio` から計算する
- rail/thumbはCPU referenceが観測するopaque RGB結果と一致させる
- 0.7秒のedge fade-in/outをGPUで適用する
- fadeはvideo pixelとYUV出力の双方に一貫して反映する
- 曲境界を跨いだalpha状態を次曲へ漏らさない

根拠: `internal/video/music_scene.go:207-231`、
`gpu/playlist-compositord/src/gpu_render.rs:3322-3333`

### R9: YUV420P

- RGBA合成結果をGPU computeでYUV420Pへ変換する
- BT.709 limited-range matrixを使用する
- CPU `rgbaToYUV420p` と係数・roundingを一致させる
- odd width/heightは `(width+1)/2`, `(height+1)/2` のchroma dimensions
- Y/U/V plane strideとpayload lengthを検証する
- YUV化後のplane bytesはCPUで変更しない
- output metadataのsequence、PTS、width、heightを一致させる

根拠: `docs/MUSIC_RENDER_V2_YUV_TRANSPORT.md:1-22`、
`gpu/playlist-compositord/src/gpu_render.rs:3106-3123`

## 7. Resource binding要件

現行のcompute compositorが使用するbindingの意味を固定し、V2 pass graphでも同じ
ownershipを維持する。

| Binding | 現行用途 | V2要件 |
|---:|---|---|
| 0 | output storage buffer | GPU-only output。CPU入力textureを置かない |
| 1 | scene uniform | geometry、timing、flags、palette、rects |
| 2/3 | glyph atlas + sampler | GPU-owned glyph resource。CPU screen rasterは禁止 |
| 4 | glyph instances | bounded instance storage |
| 5/6 | artwork + sampler | cover/background入力 |
| 7 | dynamics samples | loudness/progress等。最終形ではGPU compute生成 |
| 8/9 | text overlay + sampler | productionではCPU screen overlayを禁止。移行中はdiagnosticのみ |
| 10/11 | base texture + sampler | full-frame CPU baseは禁止。GPU生成baseのみ |
| 12-14 | waveform/loudness/spectrum texture | CPU生成textureは禁止。GPU storage/texture computeへ移行 |
| 15 | waveform Q16 storage | resident bounded audio data |
| 16 | fingerprint Q16 storage | GPU fallback artwork/feature input |
| 17 | blur storage | GPU blur intermediate |

binding追加・変更時は、WGSL、Rust bind group layout、host packing、snapshot testを
同一変更で更新する。uniform配列はWGSLの16-byte strideを壊さない。

## 8. `RenderV2` 接続要件

### 必須

- `type: "render_v2"` をsidecarが受け付ける
- `MusicRenderV2Job.ValidateProduction` を通過したrequestだけ実行する
- 8 layerすべてをNativeGPUとしてreceipt化する
- `music_render_v2_not_implemented` を返さない
- pass graphを `CANONICAL_ORDER` どおりに実行する
- CPU final rasterをrequest、intermediate、responseのいずれにも含めない
- GPU生成RGBAまたはGPU生成YUV420Pを明示的なoutput contractで返す
- failure時はCPUへフォールバックせず、診断付きのGPUエラーを返す

### 本番で禁止する旧経路

- `RenderScene` のlegacy RGBA readbackを本番GPU-only music routeに使う
- `RenderSceneYUV` の結果をCPUで再合成する
- `GPUSidecarBridge.RenderYUV` のRGBA→CPU YUV変換を本番で使う
- FFmpeg `showwaves`、`showfreqs`、ASSをGPU音楽routeへ追加する

### 移行期間のtransport互換

現行実装ではFFmpegが別プロセスであり、GPU生成Y/U/V planeをCPU側で受け取って
raw `yuv420p` pipeへ渡すcompatibility transportが残っている。この経路は、GPU内で
映像を完成させた後の値を変更しない移行用transportであり、正式な完成構成ではない。

正式な構成は、GPU完成surfaceまたはGPU-owned YUV planeを外部メモリhandle等で
GPU対応エンコーダーへ直接渡す。compatibility transportを使用する場合も、
CPU側は以下を一切実施してはならない。

- planeの再サンプリング
- matrix適用
- alpha合成
- pixel補正
- RGBAへの戻し
- CPU codec入力用の再変換

compatibility transportが残っている状態は、R0の「VRAM内完成映像データから
エンコーダーへ直接渡す」という最終要件を満たしたとは判定しない。

## 9. Failure / security / boundedness

GPU shaderは不正入力で無限処理や巨大allocationを起こしてはならない。

- width/height: `1..16384`
- row alignment: 256 bytes
- scene payload: 256 MiB以下
- artwork: 4096px以下、16 MiB以下
- waveform: 16384 samples以下
- loudness: 1000 samples以下
- glyphs: 4096以下
- glyph runs: 256以下
- text: 64 KiB以下
- dispatch dimensionsは入力からboundedに計算する
- NaN/InfはGPUへ渡さずrejectまたはclamp
- PTSは単調増加を要求
- shader validation failure、device loss、adapter不在、timeoutはfail-closed
- stderr/diagnosticはboundedに保持する

根拠: `gpu/playlist-compositord/src/contracts.rs:20-58, 280-470`

## 10. Acceptance criteria

### A. 所有権

- [ ] 外部PCM受信後のfeature計算がGPUで行われる
- [ ] 外部artwork受信後のdecode/resize/crop/blurがGPUで行われるか、GPU-readable入力だけを受理する
- [ ] 外部text/font受信後のlayout/glyph生成/合成がGPUで行われる
- [ ] CPU screen raster、CPU base、CPU YUVがproduction requestに存在しない
- [ ] sidecar失敗時にCPU rendererへ切り替わらない

### B. 映像内容

- [ ] CPU referenceと同じ1280x720 canonical layout
- [ ] 30fps、zero-origin、単調PTS
- [ ] 24 spectrum bands
- [ ] showwaves historyと一致するwaveform
- [ ] 1000点loudness、trend、4 guide lines
- [ ] artwork cover、blur、shadow、rounded mask
- [ ] fallback gradient/fingerprint/note
- [ ] title/artist/album/time、scroll、clip、missing glyph
- [ ] progress rail/thumb
- [ ] 0.7秒 edge fade

### C. 出力

- [ ] GPU生成RGBAまたはGPU生成YUV420P
- [ ] BT.709 limited-range
- [ ] odd dimension対応
- [ ] Y/U/V plane length、stride、PTS、sequence検証
- [ ] CPUはtransport以外で出力planeを変更しない

### D. 品質ゲート

- [ ] quiet / loud / bass-heavy / high-centroid / missing-artwork / long-text / unicode-textのfixture
- [ ] 360p / 720p / 1080pで比較
- [ ] HLSとplaylist MP4で同一sceneのframe内容を比較
- [ ] CPU referenceとの差分をregion別に計測
- [ ] GPU-only ownership receiptを保存
- [ ] adapter/backend/toolchain fingerprintを保存
- [ ] shader hashとscene fingerprintを保存
- [ ] device smoke testを実GPUで実行
- [ ] Go全テストとRust全テストが通過

## 11. 実装順序

1. production入力を「CPU計算済みfeature」から「GPUが処理するbounded raw input」へ分離する
2. GPU audio ingest / feature computeを追加する
3. GPU artwork preprocessとfallback procedural pathを追加する
4. GPU text/font pathを追加し、CPU glyph atlasをdiagnostic専用へ降格する
5. `CANONICAL_ORDER` の各passを実装済みpipelineとして接続する
6. `RenderV2` request/responseをsidecar mainへ接続する
7. GPU-owned output receiptとYUV encoder sinkを接続する
8. CPU readbackが本番routeに存在しないこと、GPU direct encoder handoffであることをinstrumentationで検証する
9. CPU parity comparatorで全fixtureを比較する
10. production routeを`RenderV2`へ切り替え、legacy `RenderScene`をcompatibility/diagnosticへ限定する

## 12. 完了判定

この要件におけるGPU版の完成とは、シェーダーが動くことだけではない。

次の全条件を満たした状態を完成とする。

```text
動画 / 音源
  -> FFmpegによる抽出
  -> FFprobeによる解析
  -> PCM + 各種データ
  -> GPU shader
  -> VRAM内完成映像データ
  -> GPU対応エンコーダー
  -> 配信 / 保存
```

PCM + 各種データのGPU引き渡し後に、CPUへ差し戻して補正・再合成・色変換・
ラスタ化・encoder入力化する経路が1つでも本番routeに残っている場合、GPU版は
完成扱いにしない。
