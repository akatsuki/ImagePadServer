# DeepSeek V4 Flash Visualizer Correction Leaf Tickets

> **Dispatch rule:** AV-801 through AV-812 are parent epics. Never give a parent epic directly to DeepSeek V4 Flash. Dispatch exactly one leaf ticket below, in dependency order, using `00-dispatch-contract.md`.

## Leaf-ticket limits

- One behavioral concern per ticket.
- At most two production files and two test files.
- One behavior-specific RED command, one focused GREEN command, package GREEN, and one commit.
- Workers may read the full repository but may write only the listed paths.
- A contract mismatch is `BLOCKED_CONTRACT_MISMATCH`; the worker must not invent a replacement.
- The active AI supplies the exact merged base commit before every dispatch and independently reruns GREEN before merge.

## Dependency chain

```text
Text:       AV-821 -> AV-822 -> AV-823 -> AV-824
Spectrum:   AV-831 -> AV-832 -> AV-833 -> AV-834 -> AV-835
Loudness:   AV-841 -> AV-842
Trend:      AV-843 -> AV-844 -> AV-845 -> AV-846
Color:      AV-851 -> AV-852
Fallback:   AV-853 -> AV-854 -> AV-855
Bar fade:   AV-861 -> AV-862
Integration: all chains -> AV-871 -> AV-872 -> AV-873 -> AV-874 -> AV-875
```

AV-821, AV-831, AV-841, AV-843, AV-851, AV-853, and AV-861 have disjoint write sets and may start in parallel.

## Text leaves

### AV-821: Fix ASS vertical alignment and clip padding

**Writes:** `internal/video/visualizer_ass.go`, `internal/video/visualizer_ass_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestASSMiddleAlignmentAndClipPadding$' -count=1 -v`  
**Required result:** metadata alignment `4`, time alignment `5`; clip expands by `round(2*width/1280)` with minimum 1. No scrolling change.  
**Commit:** `fix: correct visualizer text alignment`

### AV-822: Make scroll distance and cycle depend on rendered overflow

**Depends:** AV-821.  
**Writes:** `internal/video/visualizer_ass.go`, `internal/video/visualizer_ass_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestASSScrollOverflowCycle$' -count=1 -v`  
**Required seam:** `func scrollCycle(textWidth, viewportWidth, outputWidth float64) (overflow, hold, move, total float64)`  
**Required result:** hold 3s; speed `40*width/1280`; distance `T-V`; next hold begins exactly when move ends; no blank event.  
**Commit:** `fix: derive metadata scroll from overflow`

### AV-823: Resolve ASS font names from bundled OTF files

**Depends:** AV-822.  
**Writes:** create `internal/video/visualizer_text.go`, create `internal/video/visualizer_text_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestResolveVisualizerFontFaces$' -count=1 -v`  
**Required seam:** `VisualizerFontFace{FilePath, ASSFamily string}` and `ResolveVisualizerFontFaces(FontSet)`; parse the OTF name table, reject empty names and path-like ASS names.  
**Commit:** `feat: resolve visualizer font identities`

### AV-824: Use resolved font identity for ASS and measurement

**Depends:** AV-823.  
**Writes:** `internal/video/visualizer_ass.go`, `internal/video/visualizer_text_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestMeasuredAndEncodedTextWidthsDifferByAtMostOnePixel$' -count=1 -v`  
**Required result:** ASS receives `ASSFamily`; drawtext receives `FilePath`; Japanese, Latin, mixed widths differ by at most one decoded pixel.  
**Commit:** `fix: unify libass and drawtext font metrics`

## Spectrum-analysis leaves

### AV-831: Map FFT bins into all 24 logarithmic bands

**Writes:** create `internal/video/spectrum_math.go`, create `internal/video/spectrum_math_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestFractionalLogBands' -count=1 -v`  
**Required seam:** `func fractionalLogBandEnergies(coeff []complex128, sampleRate int) [24]float64`  
**Required result:** 8192-point FFT, 20Hz–20kHz, fractional bin overlap, DC excluded; each of 24 in-band sine fixtures yields finite target energy `>0`.  
**Commit:** `feat: map fractional spectrum bands`

### AV-832: Normalize spectrum with one track-level display reference

**Depends:** AV-831.  
**Writes:** `internal/video/spectrum_math.go`, `internal/video/spectrum_math_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestNormalizeSpectrumTrack' -count=1 -v`  
**Required seam:** `func normalizeSpectrumTrack(raw [][24]float64) [][24]float64`  
**Required result:** global nearest-rank 95th percentile; 60dB range; floor is median of lowest 10% finite dB values; values `<=floor+6` become exact zero; constant-gain invariant.  
**Commit:** `feat: normalize spectrum display gain`

### AV-833: Implement 15ms attack and one-second curved release

**Depends:** AV-832.  
**Writes:** `internal/video/spectrum_math.go`, `internal/video/spectrum_math_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestSpectrumMotion|^TestReleaseFraction' -count=1 -v`  
**Required seams:** `releaseFraction(seconds float64)` and `applySpectrumMotion(raw [][24]float64, fps float64)`; verify fractions at 0/.25/.5/.75/1 and exact zero at one second.  
**Commit:** `feat: add spectrum attack and release`

### AV-834: Emit FFT windows centered on 30fps timestamps

**Depends:** AV-833.  
**Writes:** `internal/video/audio_analysis.go`, create `internal/video/audio_spectrum_sync_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestSpectrumFramesUseCenteredMediaTime$' -count=1 -v`  
**Required result:** frame `i` uses `[i*1600-4096, i*1600+4096)`; zero-pad only source edges; retain bounded streaming overlap; do not normalize or smooth in this ticket.  
**Commit:** `fix: center spectrum FFT windows`

### AV-835: Apply track gain and motion after raw frame collection

**Depends:** AV-834.  
**Writes:** `internal/video/audio_analysis.go`, `internal/video/audio_analysis_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestAnalyzedSpectrum(ConstantGain|TrailingSilence|AllBands)' -count=1 -v`  
**Required result:** call AV-832 then AV-833 exactly once in `Finish`; preserve 30fps and `ceil(duration*30)` frames; final second is zero after two seconds trailing silence.  
**Commit:** `fix: finalize normalized spectrum frames`

## Relative-loudness leaves

### AV-841: Normalize 1000 RMS points relative to their track peak

**Writes:** create `internal/video/loudness_analysis.go`, create `internal/video/loudness_analysis_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestRelativeLoudness' -count=1 -v`  
**Required seam:** `func normalizeRelativeLoudness(rms [1000]float64) [1000]float64`; map exact peak to 1 and peak-36dB to 0; silence/nonfinite returns zeros; constant-gain invariant.  
**Commit:** `feat: normalize relative loudness`

### AV-842: Use relative loudness in streamAnalyzer Finish

**Depends:** AV-841 and AV-835.  
**Writes:** `internal/video/audio_analysis.go`, `internal/video/audio_analysis_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestAnalyzeAudioRelativeEnvelope' -count=1 -v`  
**Required result:** resample raw RMS to 1000 points, then normalize once; do not use LUFS or spectrum reference.  
**Commit:** `fix: wire relative loudness analysis`

## Smoothed-trend leaves

### AV-843: Gaussian-smooth the loudness envelope

**Writes:** create `internal/video/loudness_trend.go`, create `internal/video/loudness_trend_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^Test(TrendWindow|GaussianTrend)' -count=1 -v`  
**Required result:** 8s effective odd window; half-duration below 16s; duration-based point conversion; reflected endpoints; normalized Gaussian; finite `0..1`.  
**Commit:** `feat: smooth loudness trend`

### AV-844: Interpolate trend with monotone cubic Hermite curves

**Depends:** AV-843.  
**Writes:** `internal/video/loudness_trend.go`, `internal/video/loudness_trend_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestMonotoneHermite' -count=1 -v`  
**Required seam:** `func monotoneHermite(input []float64, outputCount int) []float64`; Fritsch-Carlson tangents; no segment overshoot.  
**Commit:** `feat: interpolate loudness trend curve`

### AV-845: Rasterize a 4x antialiased loudness layer

**Depends:** AV-844.  
**Writes:** create `internal/video/visualizer_loudness.go`, create `internal/video/visualizer_loudness_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestLoudnessLayerPixels$' -count=1 -v`  
**Required seam:** `renderLoudnessLayer(...) *image.RGBA`; detailed line 80%; trend 95%; 3px at 720p; 4x raster then Lanczos downsample; no frame-loop integration.  
**Commit:** `feat: rasterize loudness trend layer`

### AV-846: Cache one loudness layer per render job

**Depends:** AV-845.  
**Writes:** `internal/video/audio_visualizer.go`, `internal/video/audio_visualizer_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestLoudnessLayerRenderedOncePerJob$' -count=1 -v`  
**Required result:** build before frame loop, composite per frame, remove old per-frame `drawLoudness` call.  
**Commit:** `fix: cache loudness trend layer`

## Color and fallback leaves

### AV-851: Implement tested sRGB and OKLCH conversion

**Writes:** create `internal/video/visualizer_color.go`, create `internal/video/visualizer_color_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestOKLCH' -count=1 -v`  
**Required result:** sRGB round-trip channel error <=1; hue rotation exactly 180 modulo 360; chroma clamp .05–.18.  
**Commit:** `feat: add OKLCH color conversion`

### AV-852: Select one complementary foreground passing both regions

**Depends:** AV-851.  
**Writes:** `internal/video/visualizer_background.go`, `internal/video/visualizer_background_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestComplementaryForeground' -count=1 -v`  
**Required result:** sample blurred pre-overlay metadata and graph regions; deterministic .01 L search; WCAG >=4.5 in both; black/white only when no chromatic candidate passes.  
**Commit:** `feat: select complementary foreground`

### AV-853: Replace fallback palette thresholds and colors

**Writes:** `internal/video/fallback_artwork.go`, `internal/video/fallback_artwork_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestPaletteForFeatures' -count=1 -v`  
**Required result:** use the five exact palettes and first-match thresholds in section 14.1 of the main specification. No composition change.  
**Commit:** `fix: update fallback artwork palettes`

### AV-854: Separate palette base from final fallback decoration

**Depends:** AV-853 and AV-852.  
**Writes:** `internal/video/fallback_artwork.go`, `internal/video/fallback_artwork_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestFallbackUsesFinalComplement' -count=1 -v`  
**Required result:** base gradient first; caller-selected foreground for all 64 lines and centered literal `♪`; deterministic bytes; no background integration.  
**Commit:** `fix: decorate fallback with final color`

### AV-855: Route invalid artwork through completed fallback

**Depends:** AV-854 and AV-846.  
**Writes:** `internal/video/visualizer_background.go`, `internal/video/audio_visualizer.go`, tests only in `internal/video/visualizer_background_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestInvalidArtworkUsesCompletedFallback$' -count=1 -v`  
**Required result:** empty/missing/zero/undecodable art falls back; decorated tile is both visible tile and blur source; normal art behavior unchanged.  
**Commit:** `fix: recover invalid artwork with fallback`

## Spectrum-render leaves

### AV-861: Render a fixed-height bottom fade

**Writes:** create `internal/video/visualizer_spectrum.go`, create `internal/video/visualizer_spectrum_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestSpectrum(FadePixels|FixedBottomFade)' -count=1 -v`  
**Required result:** 5/10/15px at 360/720/1080; bottom alpha 0; normal opacity above fade; short bars use whole height without divide-by-zero.  
**Commit:** `feat: render fixed spectrum fade`

### AV-862: Replace the old spectrum renderer

**Depends:** AV-861 and AV-855.  
**Writes:** `internal/video/audio_visualizer.go`, `internal/video/audio_visualizer_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestVisualizerUsesFixedSpectrumFade$' -count=1 -v`  
**Required result:** call AV-861 renderer, delete duplicate 20%-height implementation, preserve 24-bar layout.  
**Commit:** `fix: integrate fixed spectrum fade`

## Integration and evidence leaves

### AV-871: Use media time i/30 for all Go-rendered elements

**Depends:** AV-824, AV-842, AV-862.  
**Writes:** `internal/video/audio_visualizer.go`, `internal/video/audio_visualizer_test.go`  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestVisualizerFrameTimeIsThirtyFPS$' -count=1 -v`  
**Required result:** `currentSeconds=float64(fi)/30`; same value drives progress and elapsed contract; remove `fi/totalFrames*duration`.  
**Commit:** `fix: unify visualizer media timestamps`

### AV-872: Verify encoded text, scroll reset, and waveform synchronization

**Depends:** AV-871.  
**Writes:** create `internal/video/visualizer_correction_runtime_test.go` only  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestEncoded(TextGeometry|WaveformSync)$' -count=1 -v`  
**Required result:** decoded H.264 frames show no upper clipping, correct hold/move/reset, no blank interval, and waveform burst timing within one frame.  
**Commit:** `test: verify encoded text and waveform timing`

### AV-873: Verify encoded color, fallback, trend, and fade at three resolutions

**Depends:** AV-872.  
**Writes:** `internal/video/visualizer_correction_runtime_test.go` only  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestEncodedVisualizerAt(360|720|1080)p$' -count=1 -v`  
**Required result:** decoded 360p/720p/1080p frames prove complementary RGB, fallback note/fingerprint, detailed+trend lines, and fixed fades.  
**Commit:** `test: verify encoded visualizer scaling`

### AV-874: Verify quiet-track gain invariance and trailing silence

**Depends:** AV-873.  
**Writes:** `internal/video/visualizer_correction_runtime_test.go` only  
**RED/GREEN:** `rtk go test ./internal/video -run '^TestEncodedVisualizer(GainInvariant|TrailingSilence)$' -count=1 -v`  
**Required result:** quiet and constant-gain copies match within pixel tolerance; final second after two seconds silence contains no bars.  
**Commit:** `test: verify visualizer gain and silence`

### AV-875: Run complete regression and live GUNPEI gate

**Depends:** AV-874. **Owner:** active AI; do not delegate this leaf to DeepSeek.  
**Writes:** `scripts/verify-audio-visualizer.ps1`, `docs/superpowers/plans/audio-visualizer/deepseek-ticket-status.md`  
**Commands:** `rtk go test ./... -count=3`; `rtk go build ./...`; `rtk go vet ./...`; `rtk powershell -ExecutionPolicy Bypass -File scripts/verify-audio-visualizer.ps1`; live `TestIntegrationGUNPEI`.  
**Required result:** all tests/build pass; no new vet warning; H.264/yuv420p/30fps + AAC/48kHz/stereo; full playlist decode; live generic GUNPEI path passes.  
**Commit:** `test: close visualizer correction gate`

## Worker completion block

Every DeepSeek response must end with the exact handoff block from `00-dispatch-contract.md`. The active AI rejects `REVIEW` when RED evidence is missing, a write is outside the allowlist, package tests fail, or the commit contains more than the assigned leaf.
