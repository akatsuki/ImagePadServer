# Audio Visualizer Rendering Corrections Design

Date: 2026-06-20  
Status: Approved

## 1. Scope

Correct six observed visualizer defects without replacing the current Go RGBA, FFmpeg, and libass rendering architecture:

1. Text glyphs are clipped across their upper portion.
2. Spectrum bars 1, 2, and 4 from the left remain static.
3. Overflowing metadata text remains off-screen too long before resetting.
4. White-or-black-only foreground selection is difficult to read and visually disconnected from the background.
5. Missing-art fallback artwork is not reliably composed with the final foreground mode.
6. Spectrum transparency consumes a percentage of each bar instead of a fixed bottom-edge region.

Also change the whole-track loudness graph from absolute amplitude to within-track relative loudness.

## 2. Confirmed causes

### 2.1 Text clipping

The ASS styles use alignment `1`, which means bottom-left, while text is positioned at the vertical center of each viewport. The clip rectangle then removes the portion rendered above the viewport. Time text uses alignment `8`, which is top-center rather than center-center.

### 2.2 Static low-frequency bars

The 2048-point FFT at 48 kHz has approximately 23.44 Hz bin spacing. Several logarithmic bands at the low end contain no FFT bin because their integer lower and upper indexes collapse to the same value. Those bands remain zero before smoothing.

### 2.3 Scroll reset mismatch

Text width is measured with FFmpeg `drawtext` using an explicit font file, but libass receives a file path in the ASS `Fontname` field, which expects a font family name. Font substitution can therefore make the measured width differ from the encoded width. Reset timing then uses the wrong overflow distance.

### 2.4 Foreground color limitation

`SelectForegroundMode` currently selects only black or white from regional luminance. It does not derive hue from the blurred background.

### 2.5 Fallback inconsistency

Fallback palette thresholds and colors still follow an older design. The fallback note and fingerprint are rendered with white before the final background foreground mode is known, so they can disagree with later text and graph colors.

### 2.6 Relative fade length

The spectrum fade length is `20%` of the current bar height. Tall and short bars therefore have visibly different fade regions.

### 2.7 Flat loudness graph on quiet tracks

The current envelope keeps absolute normalized RMS values. A consistently quiet master therefore remains close to the graph bottom even when its internal dynamics are meaningful.

## 3. Text geometry and font identity

- Use ASS alignment `4` (middle-left) for title, artist, and album.
- Use ASS alignment `5` (middle-center) for elapsed/total time.
- Keep the text anchor at each viewport's vertical center.
- Expand the ASS clip rectangle vertically by `2` canonical pixels above and below. Scale this padding with output resolution.
- Use the actual Noto Sans CJK JP family/PostScript name in ASS styles, not a filesystem path.
- Keep `fontsdir` pointed at the bundled font directory.
- Measure overflow using the same font face, weight, size, spacing, and renderer contract used by libass. A test must render measured and encoded text and require a maximum width difference of `1` output pixel.

## 4. Metadata scrolling

For each of title, artist, and album independently:

1. Let `T` be the verified rendered width and `V` the viewport width.
2. If `T <= V`, render one stationary event.
3. Otherwise set `D = T - V`.
4. Hold the text at the initial left edge for exactly `3.0` seconds.
5. Move from `viewportX` to `viewportX - D` at `40` canonical pixels per second.
6. The scroll phase duration is exactly `D / 40` seconds.
7. At the end of the scroll phase, the text's right edge equals the viewport's right edge. The text must not travel completely outside the viewport.
8. Reset to the initial position on the immediately following frame and begin the next 3-second hold. Do not insert an empty event or blank interval.

All distance, speed, and padding values scale by `outputWidth / 1280`.

## 5. Spectrum analysis and rendering

### 5.1 Analysis resolution

- Increase the analysis FFT window from `2048` to `8192` mono samples at 48 kHz.
- Keep the 30 fps analysis hop of `1600` samples.
- Apply the Hann window across all 8192 samples.
- Map 24 logarithmic bands across `20 Hz..20 kHz`.
- Weight partially intersecting FFT bins by their fractional overlap with each band boundary instead of truncating both boundaries to integers.
- Exclude DC from the first band.
- Associate each analysis frame with the center timestamp of its FFT window. For frame `i`, center the 8192-sample window at media time `i / 30`; pad only the portion before source start or after source end with zero.
- Do not associate a frame with the start timestamp of its FFT window. At 48 kHz that would allow up to 170.7 ms of systematic visual lead.

Every band must respond to a synthesized sine tone whose frequency lies inside that band. Adjacent-band leakage is allowed, but the target band must be finite and greater than zero.

### 5.2 Track-relative spectrum gain and silence floor

Do not normalize every frame independently. Per-frame peak normalization can amplify codec residue in nominally silent sections into full-height bars.

1. Retain the unnormalized energy for all 24 bands and all analysis frames.
2. Convert positive band energies to dB.
3. Compute one display reference from the 95th percentile of all finite band-energy values across the complete track.
4. Use the same reference for all 24 bands. Never normalize each band independently because that would falsify the track's spectral balance.
5. Map `referenceDB` to `1.0` and `referenceDB - 60 dB` to `0.0`, with linear interpolation in dB and clamping to `0..1`.
6. Estimate the codec/noise floor from low-energy frames. Values within 6 dB of that floor are silence and become exactly `0` before smoothing.
7. This automatic display gain affects only visual analysis values. Do not change PCM samples, encoded AAC gain, integrated LUFS, or playback volume.
8. A uniformly low-volume track and an otherwise identical track multiplied by a constant gain that does not clip must produce equivalent bar heights within floating-point tolerance.

### 5.3 Attack and release motion

- Attack uses a 15 ms time constant and is calculated from elapsed media time, not a hard-coded per-frame coefficient.
- A new value above the displayed bar must reach practical full height within two 30 fps frames.
- Release lasts exactly `1.0` second from the most recent displayed peak when the incoming value remains lower.
- Release follows this normalized exponential curve, where `t` is elapsed release time in seconds clamped to `0..1`:

```text
release(t) = peak * (exp(-3.5 * t) - exp(-3.5)) / (1 - exp(-3.5))
```

- Expected remaining fractions are approximately 39% at 0.25 seconds, 15% at 0.50 seconds, 4.5% at 0.75 seconds, and exactly 0 at 1.0 second.
- Never release below the current unsmoothed input value. If a new peak arrives, cancel the active release and apply Attack immediately.
- When the source remains below the silence floor, every bar must be exactly zero after one second.

### 5.4 Playback synchronization

- Spectrum frame `i`, real-time waveform frame `i`, progress position, and elapsed-time text must all represent media time `i / 30`.
- Use synthetic fixtures containing timestamped bursts and trailing silence. The first visible rise and the final raw-energy fall must be within one frame (`33.34 ms`) of their expected media timestamps.
- For a fixture with two seconds of trailing digital silence, all 24 bars must be exactly zero throughout the final one second.

### 5.5 Fixed fade region

- Use a bottom fade height of `10` canonical pixels at 720p.
- Scale to `15` pixels at 1080p and `5` pixels at 360p.
- Alpha is `0` on the common bottom row and reaches the normal bar opacity at the top edge of the fade region.
- Pixels above the fade region keep the normal bar opacity.
- For bars shorter than the fade height, apply the gradient across the complete bar without division by zero.

## 6. Complementary foreground color

### 6.1 Background sample

- Compute the foreground only from the completed blurred full-frame background before the readability overlay, artwork, text, or graphs are drawn.
- Sample both the metadata and bottom-graph regions.
- Convert the sampled average sRGB color to OKLCH.

### 6.2 Complement generation

1. Rotate OKLCH hue by exactly `180` degrees.
2. Clamp chroma to `0.05..0.18` to avoid gray or neon results.
3. Search OKLCH lightness in deterministic `0.01` increments for a candidate reaching WCAG contrast `4.5:1` in both sampled regions after the readability overlay.
4. Prefer the candidate whose lightness is closest to the direct complement's original lightness.
5. If no chromatic candidate passes, fall back to whichever of white or black has higher minimum regional contrast.

The selected RGB color is global for the entire frame. Use it for metadata, time, spectrum, waveform, loudness curve and guides, progress track and marker, fallback fingerprint, and fallback music note. Existing per-element opacity values remain unchanged.

## 7. Fallback artwork

- Use the approved five palette definitions and thresholds from the main visualizer specification.
- Generate the deterministic palette background first.
- Derive the global complementary foreground from the blurred version of that palette background.
- Render the final 64-line fingerprint and centered `♪` with the selected global foreground.
- Reuse the completed final tile as the blurred full-frame background source.
- Keep the same square crop, 24px corner radius, shadow, and no-reflection rules as normal artwork.
- Treat an empty, missing, zero-sized, or undecodable artwork path as missing artwork and continue through fallback generation instead of failing the complete render.

Tests must verify a visible centered note, nonzero fingerprint marks, deterministic bytes for identical features, and successful fallback after an invalid artwork path.

## 8. Relative whole-track loudness

The graph communicates dynamics within one track; it is not an absolute comparison between tracks.

1. Compute the 1000 short-time RMS envelope points for the complete track.
2. Convert each positive RMS value to dBFS using `20 * log10(rms)`.
3. Let `peakDB` be the highest finite envelope value in that track.
4. Map each point with:

```text
normalized = clamp((pointDB - (peakDB - 36)) / 36, 0, 1)
```

5. The track's highest point is therefore exactly `1.0`; values at least 36 dB below it are `0.0`.
6. An all-silent or non-finite envelope becomes 1000 zero values.
7. Do not use integrated LUFS or another track's peak for this graph.
8. This normalization is independent from spectrum display gain. The loudness graph uses its exact maximum point, while the animated spectrum uses a robust 95th-percentile reference to avoid one transient suppressing the rest of the bars.

Tests must cover quiet-to-loud, uniformly quiet, uniformly loud, one transient peak, and silence. Multiplying every PCM sample by a constant that does not clip must produce the same relative graph within floating-point tolerance.

### 8.1 Smoothed loudness trend line

Keep the existing 1000-point relative loudness line and add a second line that communicates the slower loudness flow across phrases and chorus sections.

1. Smooth the normalized 1000-point envelope with a Gaussian kernel whose full effective window represents `8.0` seconds of media time.
2. For tracks shorter than `16` seconds, reduce the effective window to half the track duration so the trend does not collapse into one average value.
3. Convert the time window to envelope samples using the exact media duration. Do not use a fixed number of graph points independent of duration.
4. Use reflected samples at both boundaries. Do not zero-pad because that would create false downward slopes at the beginning and end.
5. Clamp the smoothed values to `0..1`.
6. Draw the original detailed loudness line first at its existing width and 80% opacity.
7. Draw the smoothed trend line above it using the global complementary foreground RGB, `95%` opacity, and width `3` canonical pixels at 720p. Scale the width with output resolution.
8. Do not connect trend samples with visibly angular one-pixel segments. Construct a monotone cubic Hermite curve so local extrema do not overshoot outside the source sample range.
9. Rasterize the trend curve at `4x` the target graph resolution with antialiasing, then downsample with Lanczos to the final frame.
10. The trend layer is static for the complete track. Render and cache it once per visualizer job instead of recomputing it for every video frame.

The detailed line remains the source for moment-to-moment changes. The trend line is explanatory only and must not replace, delay, or alter the detailed line.

## 9. Verification

- Unit tests for ASS alignment, clip padding, same-renderer width, scroll endpoints, cycle duration, and frame-contiguous reset.
- Synthetic-tone tests for all 24 spectrum bands, including the first four.
- Timestamped burst tests proving FFT center-time alignment within one frame.
- Constant-gain invariance tests for spectrum display normalization.
- Codec-residue and two-second trailing-silence tests proving all bars reach exact zero for the final second.
- Attack tests at 15 ms and release-curve samples at 0, 0.25, 0.50, 0.75, and 1.0 seconds.
- Pixel tests for fixed 10/15/5px fades.
- Color tests for hue rotation, chroma clamp, contrast, determinism, and white/black fallback.
- Golden tests for fallback artwork with dark, light, and saturated palettes.
- Loudness invariance and silence tests.
- Gaussian trend tests for duration-to-window conversion, reflected boundaries, short tracks, and constant input.
- Trend-curve tests proving finite `0..1` output, no monotone-segment overshoot, and correct 3px/4.5px/1.5px scaled widths at 720p/1080p/360p.
- Pixel-level trend QA after 4x rasterization and Lanczos downsampling; direct one-pixel polyline output is not acceptable.
- Encoded-frame extraction at hold, mid-scroll, scroll end, and reset timestamps.
- 360p, 720p, and 1080p frame checks.
- Full package tests, build, vet baseline comparison, and live GUNPEI HLS verification.

No issue is complete from ASS text inspection or pre-encode PNG tests alone. Final evidence must include decoded H.264/yuv420p frames.
