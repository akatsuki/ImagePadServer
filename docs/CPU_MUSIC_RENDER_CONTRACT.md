# CPUモード音楽描画内容と契約

## 1. 文書の目的

この文書は、ImagePadServer に現在存在する **CPUモードの音楽描画**を、
実装・入力・描画内容・タイミング・FFmpeg出力・エラー条件・テスト根拠の
単一資料として固定する。

ここでいう CPUモードは、現在の本番GPU経路の代替ではなく、主に次のCPU
リファレンス経路を指す。

```text
RunAudioVisualizerHLSCPUReference
    -> runAudioVisualizerEncode
    -> writeVisualizerFrames
    -> CPU RGBA composition
    -> CPU RGBA -> BT.709 limited-range YUV420P
    -> FFmpeg HLS mux
```

現在の本番入口は、sidecar がない場合にCPUへフォールバックせず、
`ErrGPURequired` を返す。

- `RunAudioVisualizerHLS`: sidecarがあればGPU経路、なければ `ErrGPURequired`
  (`internal/video/audio_visualizer.go:576-581`)
- `RunAudioVisualizerHLSCPUReference`: CPU経路。ただし「production routesで使用しては
  ならない」と明記された比較・テスト用入口
  (`internal/video/audio_visualizer.go:583-600`)
- `RenderRadioTrack`: sidecarがなければ `ErrGPURequired`。現在のプレイリスト生成の
  本番CPUフォールバックではない (`internal/video/radio_render.go:64-77`)

## 2. CPU描画の責務分担

### CPUが所有するもの

- 音声解析済みデータからのフレーム描画
- 背景・アートワーク・可読性オーバーレイのベース画像生成
- スペクトラムバー
- 全曲ラウドネスグラフ
- 再生位置バーと丸形マーカー
- ASSによるタイトル、アーティスト、アルバム、時刻表示
- RGBAキャンバスからYUV420Pへの変換
- フレームを順序どおりFFmpegへ書き込むこと

### FFmpegが所有するもの

- CPUがpipeへ渡したrawvideoのエンコード
- `showwaves`によるCPUリファレンス経路の波形描画
- ASS/libassによる文字の焼き込み
- 音声入力のmuxとAACエンコード
- HLSまたはMP4のmux

CPUリファレンスでは、静的・動的な大半の描画をGoで行う一方、波形と文字は
FFmpeg filter graphにも残っている。GPU経路とは異なり、CPUリファレンスの
FFmpeg graphには `showwaves` と `ass` が存在する。

根拠: `internal/video/audio_visualizer.go:31-36, 62-144, 220-357, 360-390`

## 3. 入力契約

CPU描画への主入力は `AudioRenderInput`
(`internal/video/audio_types.go:59-109`)。

| フィールド | 契約 |
|---|---|
| `SourcePath` | 音声入力ファイル。FFmpegの音声入力として使う |
| `Kind` | `SourceMusic` 等。`SourceMusic` の場合は解析・出力ともloudnormを適用 |
| `Metadata` | `Title`, `Artist`, `Album`, `Uploader`。空文字のテキスト欄は描画しない |
| `ArtworkPath` | カバーアート。空または無効時はフォールバックアートワークを使う |
| `Analysis.FPS` | 解析フレームレート。描画の正規出力クロックは30fps |
| `Analysis.Duration` | 音声・映像の基準時間（秒） |
| `Analysis.Frames` | 各音楽フレームの `Spectrum24` |
| `Analysis.WaveformFrames` | 解析時のbounded Q16 waveform履歴。CPUの旧filter graphは主にFFmpeg `showwaves`を使用 |
| `Analysis.Features` | BPM、LUFS、低域比率、スペクトル重心、1000点ラウドネス包絡等 |

### 音声解析

`AnalyzeAudioForKind` は次のPCMを解析する。

- sample rate: 48,000 Hz
- channels: 2
- codec/output: `pcm_s16le`, `s16le`
- FFT window: 8192 samples
- frame advance: 1600 samples（30Hz相当）
- loudness envelope: 1000 samples
- music modeの解析filter: `loudnorm=I=-14.0:TP=-1.0:LRA=11.0`

根拠: `internal/video/audio_analysis.go:23-30, 47-82`

音楽モードでは、解析時と描画時で同じloudnorm済み信号を使う。描画時は
音声を一度filterに通し、`asplit=2[aud][wsrc]` で、同じ信号から波形用音声と
出力音声を分ける。

## 4. 正規キャンバスとレイアウト

基準キャンバスは **1280x720**。出力サイズに対してX方向・Y方向を個別に
比例スケールし、各座標を四捨五入する。

`internal/video/visualizer_layout.go:30-93`

| 要素 | 1280x720基準矩形 `x,y,w,h` |
|---|---:|
| Artwork | `96,152,288,288` |
| Title | `432,152,752,58` |
| Artist | `432,224,752,34` |
| Album | `432,264,752,30` |
| Spectrum | `432,320,752,168` |
| Loudness | `64,548,1000,80` |
| Progress | `64,650,1000,8` |
| Time | `1088,632,128,32` |

不正なwidthまたはheight（0以下）は `LayoutForSize` がエラーにする。

## 5. 静的な描画内容

### 5.1 背景とアートワーク

`prepareVisualizerBase` は次を1枚のimmutableなbase PNGにする。

1. アートワークをcover scaleでキャンバス全体に拡大・crop
2. `gblur=sigma=64` で全面背景をぼかす
3. アートワークをArtwork矩形へcover scale
4. Artwork矩形を角丸マスク
5. 影を付けて前景アートワークを背景に合成
6. 可読性オーバーレイを適用

アートワークが空または無効な場合、特徴量から生成したfallback artworkを
同じ処理に通す。根拠: `internal/video/visualizer_background.go:646-686`

### 5.2 前景色・アクセント色

`ForegroundMode` は以下を持つ。

- `PrimaryColor`: 文字等の主色
- `AccentColor`: 波形、グラフ、進捗表示等のアクセント色
- `Overlay`: 背景の可読性オーバーレイ

`AdaptiveForeground` は、実際に表示する背景に対してコントラストを検証し、
主色とオーバーレイを選ぶ。アートワーク由来のアクセントはOKLCHで分析し、
背景との可読性を満たす候補だけを採用する。

根拠: `internal/video/visualizer_background.go:233-283`、
`internal/video/visualizer_color.go:39-139`

fallback artwork用の特徴量ベースパレットには、次の優先順位がある。

1. BPM >= 120: オレンジ系から暗赤系
2. LowFrequencyRatio >= 0.4: 青系
3. SpectralCentroid >= 3000: 黄〜橙系
4. IntegratedLUFS >= -14: 緑系
5. その他: 紫系

根拠: `internal/video/audio_analysis.go:32-45`

## 6. フレームごとの動的描画

`renderVisualizerFrame` の合成順序は固定されている。

```text
base image
  -> 24-band spectrum
  -> cached loudness layer
  -> progress marker
```

根拠: `internal/video/audio_visualizer.go:360-372`

### 6.1 Spectrum

- 入力: `AudioFrame.Spectrum24`
- 要素数: 24 bands
- 配置: Spectrum矩形
- CPU描画関数: `drawSpectrumFixedFade`
- 前景色: `ForegroundMode` のアクセント色系
- フレームごとに更新

### 6.2 Loudness

全曲共通の静的レイヤーとして一度だけ生成し、各フレームへ再利用する。

- 入力: `AudioFeatures.LoudnessEnvelope` の1000点
- `normalizeRelativeLoudness` で曲自身のピークに対して相対正規化
- `SmoothLoudnessTrend` でトレンドを生成
- グラフ領域: Loudness矩形
- ガイド線: 4本
- ガイド線 alpha: 約0.22
- 曲線 alpha: 約0.80
- X方向: 1000点をグラフ幅へ均等配置
- Y方向: 正規化値をグラフ下端から上方向へ配置

根拠: `internal/video/audio_visualizer.go:426-494`

### 6.3 Progress

- 進捗レール: Progress矩形の角丸pill
- レール alpha: 約0.35
- マーカー alpha: 約0.88
- マーカー位置: `currentSeconds / duration`
- 位置はProgress矩形内へclamp
- マーカー半径: 基準幅1000に対して9pxを比例スケール

根拠: `internal/video/audio_visualizer.go:508-547`

CPUフレームの時刻は、フレーム番号と総フレーム数から次で求める。

```text
currentSeconds = frameIndex / totalFrames * duration
```

## 7. テキスト描画契約

テキストはASS/libassが担当し、GoはASSスクリプトと必要なfont metricを生成する。

根拠: `internal/video/audio_visualizer.go:970-1015`、
`internal/video/visualizer_ass.go:16-170`

### フォントとサイズ

- Title: SemiBold 600、基準48px
- Artist: Medium 500、基準28px
- Album: Regular 400、基準24px
- Time: Medium 500、基準22px
- サイズは出力widthに比例
- ASSのPlayResX/PlayResYは実出力サイズ

### 欠落値

- Title、Artist、Albumが空なら該当style/eventを出さない
- 空文字を測定しない
- Timeは常に生成する

### 配置とスクロール

- Title、Artist、Album: 各矩形の左寄せ・中央Y
- Time: Time矩形の中央
- 矩形外へ描画しないようclipを付ける
- テキスト幅がviewport以下なら固定表示
- viewportを超える場合は、3秒停止後、40 canonical px/sで左へスクロール
- 右端到達後はサイクル先頭へ戻る

根拠: `internal/video/visualizer_layout.go:100-128`、
`internal/video/visualizer_ass.go:97-168`

### 時刻表示

- 1秒ごとにASS eventを生成
- 表示形式: `経過時間 / 全体時間`
- 1時間未満: `M:SS`
- 1時間以上: `H:MM:SS`

## 8. フレーム・タイミング契約

CPU filter graphの正規クロックは30Hz。

```go
frames = ceil(duration * 30)
```

例外：durationから計算できない場合は `len(Analysis.Frames)` を使い、最小1にする。

根拠: `internal/video/audio_visualizer.go:843-855`

CPUレンダラーは各フレームを最大8 workerで並列生成するが、FFmpegへは必ず
frame index順で書き込む。先行しすぎないよう `workers*2+2` のin-flight windowを
持つ。

根拠: `internal/video/audio_visualizer.go:241-357`

フレームが存在しない場合、またはbase画像がnilの場合は描画を開始せずエラーにする。

## 9. CPUからFFmpegへのpixel契約

CPUの内部合成はRGBA。

- 通常のRGBA reference frame: `width * height * 4` bytes
- production-like CPU output: `yuv420p`, `width * height * 3 / 2` bytes
- plane order: I420/YUV420P
- colorspace: BT.709
- color range: limited / TV range
- black Y: 約16
- white Y: 約235
- neutral chroma: U/V 約128

`rgbaToYUV420p` はBT.709 limited-range matrixを使う。

根拠: `internal/video/audio_visualizer.go:157-193`

`writeVisualizerFrames(..., yuv=true)` は、各workerがRGBAからYUV420Pへ変換し、
ordered writerがraw bytesをFFmpeg pipeへ出力する。

## 10. HLS出力契約（CPUリファレンス）

`RunAudioVisualizerHLSCPUReference` のHLS tailは以下。

- input video: `pipe:0`
- pixel format: `yuv420p`
- resolution: preset heightから16:9で算出
- frame rate: 30fps
- audio input: `SourcePath`
- filter graph: `showwaves -> overlay -> ass`
- music audio filter: `loudnorm -> asplit`
- video codec: `QualityPreset` と選択encoderに従う
- audio codec: AAC
- audio sample rate: 48kHz
- audio channels: 2
- HLS segment duration: 4秒
- HLS list size: 0
- playlist type: event
- flags: `independent_segments`
- `-frames:v`: canonical 30Hz frame countを明示

根拠: `internal/video/audio_visualizer.go:23-46, 54-144, 583-600`

CPUリファレンスのfilter graphの基本形は次のとおり。

```text
[1:a]loudnorm,asplit=2[aud][wsrc];
[wsrc]showwaves=s=<waveW>x<waveH>:rate=30:mode=line:colors=<color>[wave];
[0:v][wave]overlay=<waveX>:<waveY>[vid];
[vid]ass=filename='<assPath>':fontsdir='<fontDir>'[out]
```

non-musicではloudnorm/asplitを挿入せず、`1:a`をそのまま使う。

## 11. プレイリストMP4のCPU契約

プレイリスト用のCPU recipeは `RadioRenderRecipe` に記録されている。
現在の本番 `RenderRadioTrack` はCPUへフォールバックしないが、recipeと
`audioVisualizerMP4ArgsWithEncoder` はCPUリファレンスおよび契約テストの根拠である。

### 共通出力

- container: MP4
- mux flag: `+faststart`
- video: H.264
- pixel format: `yuv420p`
- frame rate: 30fps
- audio: AAC
- audio: 48kHz / 2 channels
- audio filter: music modeではloudnorm
- waveform: 30fps / line mode
- waveform color: accentまたはwhite、opacity 0.55
- ASS renderer: libass

### エッジフェード

曲の長さが `2 * edgeSeconds` より長い場合にだけ有効。

- `radioEdgeFadeSeconds = 0.7`
- video fade-in: 0.00秒から0.70秒
- video fade-out: duration - 0.70秒から0.70秒
- audio fade-in/outも同じ区間
- 短すぎる曲ではfadeを無効化

### エンコード

CPUのlow-latency radio profileでは通常次を使う。

- codec: `libx264`
- preset: `ultrafast`
- GOP: profileに応じる。`rtsp-ultra`は30 frames
- `-keyint_min`: GOPと同じ
- `-bf 0`
- software encoderでは `-sc_threshold 0`
- software encoderでは `-x264-params aud=1:repeat-headers=1`

根拠: `internal/video/radio_recipe.go:107-195, 269-295`、
`internal/video/radio_render_test.go:23-103`

## 12. エラー・フォールバック契約

### CPU描画内部

以下はエラー終了条件。

- layoutのwidth/heightが不正
- font解決失敗
- artwork/base生成失敗
- ASS font face解決失敗
- テキスト幅測定失敗
- `Analysis.Frames` が空
- base画像がnil
- FFmpeg pipeへのshort writeまたはwrite失敗
- FFmpeg encode失敗

失敗時は部分HLS出力を削除する。根拠:
`internal/video/audio_visualizer.go:917-1058`

### 本番経路との分離

CPUリファレンスは、GPU sidecarの不在を理由に本番へ自動昇格してはならない。
本番入口のsidecar不在時は `ErrGPURequired` とする。

これは「性能低下時にCPUへ逃がす」契約ではなく、GPU専用の本番契約を守るための
fail-closedである。

## 13. 契約を守る既存テスト

主なテスト根拠は次のとおり。

### CPU HLS / filter graph

- `TestAudioVisualizerFFmpegArgsBasic`
- `TestAudioVisualizerFFmpegArgsContainsShowwaves`
- `TestAudioVisualizerFFmpegArgsPipeInput`
- `TestAudioVisualizerFFmpegArgsHLS`
- `TestAudioVisualizerFFmpegArgsUsesValidHLSOptions`
- `TestAudioVisualizerArgsInlineLoudnorm`
- `TestAudioVisualizerFFmpegArgsCompressionSettings`
- `TestAudioVisualizerFFmpegArgsUsesPresetResolution`
- `TestAudioVisualizerFFmpegArgsQuotesASSPaths`

根拠: `internal/video/audio_visualizer_test.go:92-257`

### pixel・frame・解析

- `TestRGBAToYUV420pLimitedRange`
- `TestWriteVisualizerFramesYUVSize`
- `TestCanonicalMusicVideoFrameCountIncludesPartialTailTick`
- `TestWriteVisualizerRGBAFramesFPS30`
- zero-frame入力のエラー検証

根拠: `internal/video/audio_visualizer_test.go:143-183, 480-520`

### radio recipe / MP4契約

- `TestRadioRenderRecipeCanonicalJSONAndFingerprintAreDeterministic`
- `TestRadioRenderRecipeFingerprintCoversEveryReceiverVisibleField`
- `TestRadioRecipeTypedSemanticsDriveArgsAndFingerprints`
- `TestRadioRenderContractIncludesActualLayoutWaveformASSAndFadeRules`
- `TestRadioRenderRecipeCPUAndGPUEquivalentContractsMatch`
- `TestLegacyRadioRecipeGOPMatrixIsPreserved`
- `TestAudioVisualizerMP4ArgsWithEdgeFades`
- `TestAudioVisualizerMP4ArgsUseAVProSafeCPUEncoding`

根拠: `internal/video/radio_recipe_test.go:22-185`、
`internal/video/radio_render_test.go:23-103`

## 14. 現在の実装状態

| 項目 | 状態 |
|---|---|
| CPU reference HLS renderer | 実装済み |
| CPU base/artwork/background | 実装済み |
| CPU spectrum/loudness/progress | 実装済み |
| ASS text rendering | 実装済み |
| CPU RGBA -> limited BT.709 YUV420P | 実装済み |
| HLS/MP4 recipe契約 | 実装済み |
| CPU reference用テスト | 実装済み |
| 本番HLSのCPUフォールバック | なし。`ErrGPURequired` |
| 本番playlistのCPUフォールバック | なし。`ErrGPURequired` |
| GPU `RenderV2`との実行時共有 | 未接続 |
| GPU-owned YUV handleとの接続 | 未接続 |

したがって、この文書のCPU描画内容は **比較基準・回帰検証用の契約**として扱い、
本番でCPU経路が自動選択されることを意味しない。
