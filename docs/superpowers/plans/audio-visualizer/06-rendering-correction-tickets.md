# Audio Visualizer Rendering Corrections Implementation Plan

> **DeepSeek V4 Flash note:** AV-801 through AV-812 are parent epics and must not be dispatched directly. Use the smaller executable leaves in `07-deepseek-v4-flash-leaf-tickets.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correct text, spectrum, color, fallback-artwork, and whole-track loudness rendering so decoded H.264 frames satisfy the approved 2026-06-20 design.

**Architecture:** Keep the current Go RGBA + FFmpeg + libass pipeline. Move new numerical behavior into focused pure-Go files, test it independently, and integrate it through narrow follow-up tickets. Only the final ticket may claim end-to-end completion.

**Tech Stack:** Go, gonum FFT, `image.RGBA`, FFmpeg/libass/drawtext, HLS H.264/yuv420p, Go tests, PowerShell verifier.

---

## 1. Mandatory worker contract

Every worker must also follow `00-dispatch-contract.md`. The active AI replaces `BASE_COMMIT` and `WORKTREE` before dispatch. A worker must stop with `BLOCKED_CONTRACT_MISMATCH` if a named function, type, or path differs.

```text
You are implementing exactly one AV-8xx ticket from 06-rendering-correction-tickets.md.
Base commit: BASE_COMMIT. Stop if HEAD differs.
Worktree: WORKTREE. Stop if the current directory differs.
Read completely:
1. docs/superpowers/specs/2026-06-20-visualizer-rendering-corrections-design.md
2. docs/superpowers/plans/audio-visualizer/00-dispatch-contract.md
3. your complete AV-8xx section
Rules:
- Prefix every shell command with rtk.
- Write only the ticket allowlist.
- Do not edit internal/about/about.go, winres, README, release files, or unrelated formatting.
- Add the named test first and show behavior-specific RED.
- Implement only the named contracts; do not rename public types.
- Run focused GREEN, rtk go test ./internal/video -count=1, and rtk git diff --check.
- Create one logical commit with the exact ticket commit message.
- Return the required handoff block from 00-dispatch-contract.md with no blank fields.
```

Workers must not edit `ticket-status.md`. Only the active AI records verification and merge state.

## 2. File ownership map

| File | Responsibility | First owner |
| --- | --- | --- |
| `visualizer_ass.go` | ASS family names, alignment, clipping, scroll events | AV-801 |
| `visualizer_text.go` | font identity and text measurement contract | AV-802 |
| `spectrum_math.go` | fractional bands, track gain, silence, attack/release | AV-803 |
| `audio_analysis.go` | centered FFT stream integration and relative envelope wiring | AV-804 then AV-806 |
| `loudness_analysis.go` | dBFS relative envelope normalization | AV-805 |
| `loudness_trend.go` | Gaussian smoothing and monotone Hermite samples | AV-807 |
| `visualizer_loudness.go` | cached 4x trend raster and detailed curve | AV-808 |
| `visualizer_color.go` | sRGB/OKLCH complement and contrast search | AV-809 |
| `fallback_artwork.go` | approved palettes and two-phase fallback composition | AV-810 |
| `visualizer_spectrum.go` | fixed-pixel fade renderer | AV-811 |
| `audio_visualizer.go` | final composition only | AV-808, AV-810, AV-811, AV-812 sequentially |

## 3. Dependency and parallel execution

```text
Wave R1: AV-801   AV-803   AV-805   AV-807   AV-809
             |       |        |        |        |
Wave R2:   AV-802   AV-804 ----+      AV-808 ----+
                       |                 |
Wave R3:              AV-806           AV-810
                                           |
Wave R4:                                  AV-811
                 \        \                /
Wave R5:                         AV-812
```

Parallel work is allowed only for disjoint allowlists. AV-804 and AV-806 are sequential because both modify `audio_analysis.go`. AV-808, AV-810, AV-811, and AV-812 are an explicit sequence because all integrate `audio_visualizer.go`.

### AV-801: Correct ASS alignment, clip padding, and scroll event geometry

**Dependencies:** `bab353e`. **Parallel wave:** R1.

**Write allowlist:**
- `internal/video/visualizer_ass.go`
- `internal/video/visualizer_ass_test.go`

Required helpers:

```go
func assClip(rect Rect, width int) string
func scrollCycle(textWidth, viewportWidth, outputWidth float64) (overflow, hold, move, total float64)
func buildScrollingDialogue(b *strings.Builder, totalDuration float64, style, text string, textWidth, viewportW, viewportX, viewportY, viewportH, outputWidth float64)
```

- [ ] Add `TestASSUsesMiddleAlignmentAndScaledClipPadding`: title/artist/album styles must use alignment `4`, time must use `5`, and a 720p metadata clip must be `Y-2..Y+H+2`.
- [ ] Add `TestASSScrollUsesOnlyOverflowDistance`: for `T=900`, `V=752`, and width `1280`, assert hold `3`, distance `148`, move duration `3.7`, final X `viewportX-148`, and no event gap at reset.
- [ ] Add a 640-wide scaling case: speed `20 px/s` and clip padding `1 px`.
- [ ] Run `rtk go test ./internal/video -run '^TestASS(UsesMiddle|ScrollUses)' -count=1 -v`; expected RED because current defaults are alignment 1/8 and unpadded clips.
- [ ] Change `writeStyle` callers to pass `4,4,4,5`. Generate every metadata clip through `assClip`; scale padding as `round(2*width/1280)` with minimum 1.
- [ ] In `buildScrollingDialogue`, scale speed as `40*outputWidth/1280`, emit one hold event and one `\move` event per cycle, and set the next hold start exactly equal to the prior move end. Never move by `textWidth`; move only by `textWidth-viewportWidth`.
- [ ] Run focused tests, `rtk go test ./internal/video -count=1`, and `rtk git diff --check`.
- [ ] Commit `fix: correct visualizer ASS geometry`.

### AV-802: Use one font identity and prove encoded text width

**Dependencies:** AV-801. **Parallel:** prohibited with AV-801 or AV-812.

**Write allowlist:**
- Create `internal/video/visualizer_text.go`
- Create `internal/video/visualizer_text_test.go`
- Modify `internal/video/visualizer_ass.go`

Required types and helpers:

```go
type VisualizerFontFace struct { FilePath, ASSFamily string }
type VisualizerFontFaces struct { Regular400, Medium500, SemiBold600 VisualizerFontFace }
func ResolveVisualizerFontFaces(fonts FontSet) (VisualizerFontFaces, error)
func MeasureVisualizerText(ctx context.Context, ffmpeg string, face VisualizerFontFace, text string, size int) (TextMetrics, error)
```

- [ ] Add `TestResolveVisualizerFontFacesRejectsPathAsASSName` and assert all three `ASSFamily` values are non-empty, contain no slash, and resolve back to their expected Noto Sans CJK JP file.
- [ ] Add FFmpeg-gated `TestMeasuredAndEncodedTextWidthsDifferByAtMostOnePixel`. Render Japanese, Latin, and mixed strings once through drawtext measurement and once through libass, scan alpha bounds, and require `abs(widthA-widthB) <= 1`.
- [ ] Run `rtk go test ./internal/video -run '^Test(ResolveVisualizerFontFaces|MeasuredAndEncoded)' -count=1 -v`; expected RED because ASS currently receives a file path.
- [ ] Resolve each actual family/PostScript name from the bundled OTF name table. Pass the resolved name to ASS and the file path to drawtext; the AV-812 integration keeps `fontsdir=filepath.Dir(face.FilePath)`.
- [ ] Make `MeasureVisualizerText` return an error for any non-empty string it cannot render. Do not add a silent zero-width fallback.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `fix: unify visualizer font metrics`.

### AV-803: Implement deterministic spectrum mathematics

**Dependencies:** `bab353e`. **Parallel wave:** R1. This ticket does not modify streaming integration.

**Write allowlist:**
- Create `internal/video/spectrum_math.go`
- Create `internal/video/spectrum_math_test.go`

Required constants and helpers:

```go
const spectrumFFTSize = 8192
const spectrumBandCount = 24
func fractionalLogBandEnergies(coeff []complex128, sampleRate int) [24]float64
func normalizeSpectrumTrack(raw [][24]float64) [][24]float64
func applySpectrumMotion(raw [][24]float64, fps float64) [][24]float64
func releaseFraction(seconds float64) float64
```

- [ ] Add 24 subtests that synthesize a bin-centered sine inside each logarithmic `20..20000 Hz` band and require its target energy to be finite and `> 0`. Explicitly name the first four subtests.
- [ ] Add `TestSpectrumNormalizationIsConstantGainInvariant`, comparing one fixture with a `0.01` multiplier to the same unclipped fixture at `1.0`; tolerance `1e-9`.
- [ ] Add `TestSpectrumNoiseFloorBecomesExactZero`: low-energy frames within `6 dB` of the estimated floor must be exactly zero.
- [ ] Add release table tests at `0,.25,.5,.75,1` for `1,.39,.15,.045,0` with tolerance `0.015`, plus a two-frame attack test at 30 fps and a one-second exact-zero test.
- [ ] Run `rtk go test ./internal/video -run '^TestSpectrum|^TestReleaseFraction' -count=1 -v`; expected RED because the file and contracts do not exist.
- [ ] Weight each positive FFT bin by overlap between its frequency interval and each logarithmic band. Exclude DC. Store positive energy before normalization.
- [ ] Convert energy to dB; choose the finite global 95th percentile with deterministic sorted nearest-rank selection; map `[referenceDB-60, referenceDB]` to `[0,1]`. Define `noiseFloorDB` as the median of the lowest 10% of sorted finite dB values (minimum one value), then zero values `<= noiseFloorDB+6`.
- [ ] Compute attack coefficient from `dt=1/fps` and 15 ms; release each remembered peak with `(exp(-3.5*t)-exp(-3.5))/(1-exp(-3.5))`, clamped to one second and never below current raw input.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `feat: add spectrum display mathematics`.

### AV-804: Center 8192-sample FFT frames on media timestamps

**Dependencies:** AV-803. **Parallel:** prohibited with AV-806.

**Write allowlist:**
- Modify `internal/video/audio_analysis.go`
- Modify `internal/video/audio_analysis_test.go`
- Create `internal/video/audio_spectrum_sync_test.go`

- [ ] Add `TestAllSpectrumBandsRespond`, using the AV-803 sine generator through `streamAnalyzer` rather than calling the pure helper directly.
- [ ] Add `TestSpectrumFramesUseCenteredMediaTime` with bursts beginning at exact 30 fps boundaries; first raw rise and last raw fall must be within one frame.
- [ ] Add `TestSpectrumTrailingSilenceIsZero`: a fixture with two seconds of trailing digital silence must have all bars exactly zero for its final 30 frames.
- [ ] Run `rtk go test ./internal/video -run '^Test(AllSpectrum|SpectrumFramesUse|SpectrumTrailing)' -count=1 -v`; expected RED from the old 2048 start-aligned windows and static low bands.
- [ ] Replace `fftWindowSize` with `spectrumFFTSize`. For output frame `i`, form a window centered on sample `i*1600`; read `[center-4096, center+4096)` and zero-pad before source start and after source end.
- [ ] Keep enough rolling PCM to build centered windows without retaining the whole track. Do not emit the first frame until its right half is available. At `Finish`, zero-pad only missing tail samples.
- [ ] Collect all raw `[24]float64` energies, then call `normalizeSpectrumTrack` and `applySpectrumMotion` once. Keep `AudioAnalysis.FPS == 30` and `ceil(duration*30)` frames.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `fix: synchronize spectrum analysis to playback`.

### AV-805: Normalize whole-track loudness relative to its own peak

**Dependencies:** `bab353e`. **Parallel wave:** R1.

**Write allowlist:**
- Create `internal/video/loudness_analysis.go`
- Create `internal/video/loudness_analysis_test.go`

Required helper:

```go
func normalizeRelativeLoudness(rms [1000]float64) [1000]float64
```

- [ ] Add table tests for quiet-to-loud, uniformly quiet, uniformly loud, one transient, all-zero, NaN, and infinity inputs.
- [ ] Add `TestRelativeLoudnessIsConstantGainInvariant` with unclipped gain factors `0.01`, `0.1`, and `1`; tolerance `1e-9`.
- [ ] Run `rtk go test ./internal/video -run '^TestRelativeLoudness' -count=1 -v`; expected RED because the helper does not exist.
- [ ] Convert positive finite RMS values with `20*log10(rms)`. Let `peakDB` be the exact maximum and map `clamp((pointDB-(peakDB-36))/36,0,1)`. Return 1000 zeros if no finite positive point exists.
- [ ] Explicitly force at least one maximum point to `1.0` to prevent round-off below the graph top.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `feat: normalize track loudness envelope`.

### AV-806: Wire relative loudness into streaming analysis

**Dependencies:** AV-804 and AV-805. **Parallel:** prohibited with AV-804.

**Write allowlist:**
- Modify `internal/video/audio_analysis.go`
- Modify `internal/video/audio_analysis_test.go`

- [ ] Add `TestAnalyzeAudioRelativeEnvelopeQuietTrack` and `TestAnalyzeAudioRelativeEnvelopeSilence`; use streamed stereo samples and assert max `1` for non-silence and all zeros for silence.
- [ ] Run `rtk go test ./internal/video -run '^TestAnalyzeAudioRelativeEnvelope' -count=1 -v`; expected RED because `resampleEnvelope` currently returns absolute RMS.
- [ ] Resample the short-time RMS blocks to exactly 1000 points first, then call `normalizeRelativeLoudness`. Do not reuse spectrum reference dB or LUFS.
- [ ] Preserve the raw RMS blocks until `Finish`; do not normalize chunks independently.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `fix: use relative loudness in analysis`.

### AV-807: Compute the smoothed loudness trend

**Dependencies:** `bab353e`. **Parallel wave:** R1.

**Write allowlist:**
- Create `internal/video/loudness_trend.go`
- Create `internal/video/loudness_trend_test.go`

Required helpers:

```go
func trendWindowSamples(duration float64, pointCount int) int
func gaussianTrend(input []float64, duration float64) []float64
func monotoneHermite(input []float64, outputCount int) []float64
```

- [ ] Test that the effective window is eight seconds, or half duration below 16 seconds, converted with `round(windowSeconds*pointCount/duration)` and forced to an odd positive size.
- [ ] Test reflected boundaries with an impulse at each endpoint; zero-padding behavior must fail the assertion.
- [ ] Test constant, short, zero, NaN-containing, rising, falling, and single-peak inputs. Output must remain finite in `0..1` and monotone segments must not overshoot their endpoint range.
- [ ] Run `rtk go test ./internal/video -run '^Test(TrendWindow|GaussianTrend|MonotoneHermite)' -count=1 -v`; expected RED because these helpers do not exist.
- [ ] Build a normalized Gaussian kernel whose full effective width is `trendWindowSamples`; reflect out-of-range indexes with repeated mirror mapping.
- [ ] Implement monotone cubic Hermite interpolation with Fritsch-Carlson tangents. Clamp numerical noise only after interpolation.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `feat: compute smooth loudness trend`.

### AV-808: Render and cache the antialiased trend layer

**Dependencies:** AV-807. **Parallel:** prohibited with AV-810, AV-811, or AV-812.

**Write allowlist:**
- Create `internal/video/visualizer_loudness.go`
- Create `internal/video/visualizer_loudness_test.go`
- Modify `internal/video/audio_visualizer.go`
- Modify `internal/video/audio_visualizer_test.go`

Required helper:

```go
func renderLoudnessLayer(envelope [1000]float64, duration float64, mode ForegroundMode, layout VisualizerLayout, width, height int) *image.RGBA
```

- [ ] Add pixel tests for detailed line opacity 80%, trend opacity 95%, and trend widths `1.5/3/4.5` canonical pixels at 360p/720p/1080p within antialiasing tolerance.
- [ ] Add `TestLoudnessTrendUsesSupersampledCurve`, requiring non-binary edge alpha and rejecting the old one-pixel point plot.
- [ ] Add `TestLoudnessLayerRenderedOncePerJob` with an injected counter around layer construction.
- [ ] Run `rtk go test ./internal/video -run '^TestLoudness(TrendUses|LayerRendered|LayerPixels)' -count=1 -v`; expected RED because only the detailed point plot exists.
- [ ] Draw guides and detailed curve first. Generate the Hermite trend at four times graph width, rasterize on a 4x transparent layer, then Lanczos-downsample to output size.
- [ ] Build this static layer once before the frame loop. Composite it each frame; do not recompute Gaussian smoothing or rasterization inside `for fi`.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `feat: render cached loudness trend`.

### AV-809: Select a contrast-safe OKLCH complementary foreground

**Dependencies:** `bab353e`. **Parallel wave:** R1.

**Write allowlist:**
- Create `internal/video/visualizer_color.go`
- Create `internal/video/visualizer_color_test.go`
- Modify `internal/video/visualizer_background.go`
- Modify `internal/video/visualizer_background_test.go`

Required internal type and helper:

```go
type oklch struct { L, C, H float64 }
func SelectComplementaryForeground(background image.Image, metadataRect, graphRect image.Rectangle) ForegroundMode
```

- [ ] Add round-trip sRGB/OKLCH tests with maximum channel error 1, hue rotation exactly 180 degrees modulo 360, and chroma clamping to `0.05..0.18`.
- [ ] Add saturated dark, saturated light, mixed-region, deterministic-repeat, and no-chromatic-candidate tests. Every accepted mode must reach minimum regional WCAG contrast `>=4.5` after overlay.
- [ ] Run `rtk go test ./internal/video -run '^Test(OKLCH|ComplementaryForeground)' -count=1 -v`; expected RED because current selection is black/white-only.
- [ ] Average sRGB separately over the metadata and graph regions of the blurred pre-overlay background. Derive the direct complement from their deterministic combined average.
- [ ] Search lightness `0.00..1.00` in `0.01` steps, order candidates by distance from direct L then by lower L, and choose the first passing both regions. Use black or white only if no chromatic candidate passes.
- [ ] Keep `ForegroundMode.Color` global. If foreground relative luminance is at least `0.5`, search a black overlay; otherwise search white. Increase overlay alpha from the existing start value in `0.05` steps through `0.60`, and require both regions to pass before accepting the mode.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `feat: select complementary visualizer color`.

### AV-810: Rebuild fallback artwork after foreground selection

**Dependencies:** AV-808 and AV-809. **Parallel:** prohibited with AV-811 or AV-812.

**Write allowlist:**
- Modify `internal/video/fallback_artwork.go`
- Modify `internal/video/fallback_artwork_test.go`
- Modify `internal/video/visualizer_background.go`
- Modify `internal/video/visualizer_background_test.go`
- Modify `internal/video/audio_visualizer.go`

Approved palettes, in first-match order:

```text
high: BPM >= 130 OR LUFS >= -11       #7A1D4F -> #FF6B35
bass: LowFrequencyRatio >= 0.45       #24103F -> #7C3AED
bright: SpectralCentroid >= 3500      #0B5563 -> #20C7C9
calm: BPM < 95 AND LUFS <= -16        #1F2A44 -> #5E5CE6
default                                #173B57 -> #3A86FF
```

- [ ] Update palette boundary tests to the exact thresholds and colors above.
- [ ] Add `TestFallbackUsesFinalComplementForNoteAndFingerprint`, `TestFallbackBytesAreDeterministic`, and invalid/missing/zero-sized/undecodable artwork cases.
- [ ] Run `rtk go test ./internal/video -run '^Test(Fallback|PaletteForFeatures|InvalidArtwork)' -count=1 -v`; expected RED because current thresholds/colors are old and glyph composition occurs before color selection.
- [ ] Split fallback generation into palette-only tile and decorated final tile. Blur the palette tile, select the global complement, then draw all 64 fingerprint lines and centered `♪` using that exact RGB.
- [ ] If normal artwork cannot be opened or decoded, continue through fallback generation. Use the decorated final tile both as visible artwork and the source for the full-frame blurred background.
- [ ] Preserve square cover crop, scaled 24px corner radius, shadow, and no reflection. Correct the mojibake glyph literal to `♪`.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `fix: compose deterministic fallback artwork`.

### AV-811: Apply a fixed bottom fade to every spectrum bar

**Dependencies:** AV-810. **Parallel:** prohibited with AV-810 or AV-812.

**Write allowlist:**
- Create `internal/video/visualizer_spectrum.go`
- Create `internal/video/visualizer_spectrum_test.go`
- Modify `internal/video/audio_visualizer.go`
- Modify `internal/video/audio_visualizer_test.go`

Required helper:

```go
func spectrumFadePixels(outputHeight int) int
```

- [ ] Add exact scaling cases `360->5`, `720->10`, `1080->15` and a one-pixel minimum case.
- [ ] Add pixel tests for a tall and a shorter-than-fade bar. Bottom alpha must be zero, the fade top must reach normal 82% opacity, and pixels above the fixed region must keep normal opacity.
- [ ] Run `rtk go test ./internal/video -run '^TestSpectrum(FadePixels|FixedBottomFade)' -count=1 -v`; expected RED because current fade is 20% of bar height.
- [ ] Move `drawSpectrum` into `visualizer_spectrum.go`. Use `round(10*height/720)`, not bar height. For short bars, span the complete bar and avoid a zero denominator.
- [ ] Delete the old implementation from `audio_visualizer.go`; do not leave duplicate renderers.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `fix: use fixed spectrum fade height`.

### AV-812: Prove full synchronization and decoded-frame behavior

**Dependencies:** AV-802, AV-804, AV-806, AV-808, AV-810, AV-811. **Parallel:** prohibited. **Owner:** active AI or one dedicated integration worker.

**Write allowlist:**
- Modify `internal/video/audio_visualizer.go`
- Modify `internal/video/audio_visualizer_test.go`
- Modify `internal/video/audio_runtime_test.go`
- Modify `scripts/verify-audio-visualizer.ps1`
- Create `internal/video/visualizer_correction_runtime_test.go`
- Modify `docs/superpowers/plans/audio-visualizer/ticket-status.md` only by active AI after verification

- [ ] Add a timestamp-burst fixture and assert spectrum frame, progress marker, and elapsed text represent `i/30`; tolerance one frame.
- [ ] Decode the FFmpeg real-time waveform layer from the same burst fixture and require its first visible rise and final fall within one frame of spectrum and media timestamps.
- [ ] Encode long Japanese metadata and extract frames during hold, mid-scroll, scroll end, and immediate reset. Assert no upper glyph clipping and no blank reset interval.
- [ ] Encode 360p, 720p, and 1080p fixtures with normal art and missing art. Pixel-check complementary global RGB, fallback note/fingerprint, fixed fades, detailed loudness, and smooth trend.
- [ ] Encode a quiet track and constant-gain copy; bar and loudness geometry must match within pixel tolerance. Encode two seconds of trailing silence; all bars must be absent for the final second.
- [ ] Run focused runtime tests. Expected initial RED is any missing integration or decoded-frame assertion; environment/tool absence is not valid RED.
- [ ] Resolve `VisualizerFontFaces` once, use family names in ASS, file paths in measurement, and fail when any non-empty metadata measurement fails. Keep `fontsdir` at the bundled font directory.
- [ ] Derive `currentSeconds := float64(fi)/30.0` everywhere. Do not use `fi/totalFrames*duration`. Use the same value for progress and ASS elapsed time; clamp only at media duration.
- [ ] Update the verifier to extract and retain evidence only under an ignored temp directory. Require H.264, yuv420p, 30 fps, AAC stereo 48 kHz, and full playlist decode exit code zero.
- [ ] Run `rtk go test ./internal/video -count=1`, then `rtk go test ./... -count=3`, `rtk go build ./...`, `rtk go vet ./...`, and `rtk git diff --check`. Record the pre-existing SteamVR vet warning separately; no new warning is accepted.
- [ ] Run `rtk powershell -ExecutionPolicy Bypass -File scripts/verify-audio-visualizer.ps1` and live GUNPEI generic HLS verification. A network failure is `WAITING_EXTERNAL`, not success.
- [ ] Commit `fix: complete visualizer rendering corrections`.

## 4. Active-AI merge gate

For every ticket, the active AI must:

- [ ] Verify the worker HEAD descends from the recorded base commit.
- [ ] Reject any path outside the allowlist, especially `internal/about/about.go`.
- [ ] Inspect RED evidence and rerun focused GREEN in the worker worktree.
- [ ] Run `rtk git diff --check` before merge.
- [ ] Merge in dependency order and rerun `rtk go test ./internal/video -count=1` after each merge.
- [ ] Mark a ticket `VERIFIED` only after independently rerunning its command; mark `MERGED` only after integration passes.
- [ ] Keep AV-710 blocked until AV-812 passes. Do not bump or build a release from intermediate tickets.

## 5. Completion definition

The correction wave is complete only when AV-801 through AV-812 are `MERGED`, three full test runs pass, build passes, no new vet warning exists, decoded H.264 frames satisfy every visual assertion, and the live GUNPEI generic pipeline succeeds. Source inspection, ASS text inspection, or pre-encode PNG output alone is insufficient.
