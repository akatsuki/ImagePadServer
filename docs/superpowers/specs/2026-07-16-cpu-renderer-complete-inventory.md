# CPU Music Renderer Complete Inventory

Status: frozen inventory for zero-based GPU renderer redesign (2026-07-16)

This document enumerates the CPU renderer's observable behavior. It is a
reference inventory only; it does not authorize implementation or replacement
of the current production route.

## 1. Input and analysis

- Audio source, sample rate, channel count, duration, RMS, peak, 24-band
  spectrum, 1000-point loudness envelope, smoothed trend, frame index, 30fps
  PTS, filtered-signal analysis, per-track reset, and monotonic PTS.

## 2. Artwork and background

- Embedded Front Cover selection, largest-area/byte fallback, SoundCloud
  source gating, RGBA normalization, alpha preservation, 32px color analysis,
  3x3 analysis blur, cover-scaled blurred background, chroma/neutral clamp,
  readability foreground selection, overlay color/opacity cap, cover-fit tile,
  rounded corners, shadow, and deterministic no-artwork note/fingerprint tile.

## 3. Canvas and layout

- Canonical 1280x720 16:9 canvas; 360p/720p/1080p integer scaling; even output
  dimensions; artwork, title, artist, album, spectrum, loudness, progress,
  time, waveform and clipping rectangles; rounded coordinate scaling.

## 4. Metadata and typography

- Independently optional title/artist/album; normalized metadata; elapsed and
  total time; Unicode/Japanese; font fallback; title 48px/weight 600, artist
  28px/weight 500, album 24px/weight 400; FFmpeg/libass measurement;
  antialiasing, alpha and fade.

## 5. Scrolling text

- Three-second pause, 40 canonical px/s, measured text width, right-edge clamp,
  leftward movement, cycle reset, clip padding, fade in/out, and independent
  title/artist/album scroll state.

## 6. Spectrum and waveform

- 24 logarithmic spectrum bars, frame-clock sampling, RMS/peak coupling, fixed
  bottom fade, bar gaps, bar color/opacity/antialiasing, FFmpeg `showwaves`
  line mode at 30fps, canonical waveform rectangle, 0.55 opacity, filtered
  audio source, and waveform tail behavior.

## 7. Loudness and playback UI

- Loudness rectangle; 1000-point envelope; relative normalization; smoothed
  trend; four guide lines; envelope/trend line width, color, opacity and clip;
  progress rail, thumb, elapsed/total formatting, edge fade, and start/mid/end
  states.

## 8. Layer order

1. Background image
2. Strong blur
3. Brightness/saturation correction
4. Readability overlay
5. Artwork tile
6. Artwork shadow
7. Spectrum
8. Waveform
9. Loudness
10. Title
11. Artist
12. Album
13. Time
14. Progress rail
15. Progress thumb
16. Edge fade

## 9. Output and lifecycle

- HLS event playlist with 4s independent segments, AAC 48kHz stereo, H.264,
  30fps, and synchronized audio/video; playlist MP4 with H.264/AAC,
  `+faststart`, GOP/latency profile, optional 0.7s audio/video fades and track
  duration limits.
- Fail-closed GPU errors, FFmpeg/analysis/font/artwork errors, malformed frame
  and PTS validation, cancellation of both child processes, partial-output
  cleanup, sidecar session reset, and `gpu_required`/
  `gpu_renderer_unavailable` mapping.

## 10. Non-negotiable parity

Geometry, layer order, artwork/fallback choice, palette/blur/overlay, feature
normalization, waveform/loudness values, text content and motion, time/progress
state, fade windows, frame count, PTS order, color/alpha contract, and mux
parameters must match the CPU reference. GPU antialiasing and one-code-value
color rounding are the only intended raster tolerances.

## Evidence sources

- `internal/video/audio_visualizer.go`
- `internal/video/audio_analysis.go`
- `internal/video/audio_artwork.go`
- `internal/video/audio_metadata.go`
- `internal/video/fallback_artwork.go`
- `internal/video/visualizer_ass.go`
- `internal/video/visualizer_color.go`
- `internal/video/visualizer_background.go`
- `internal/video/visualizer_layout.go`
- `internal/video/visualizer_loudness.go`
- `internal/video/gpu_contracts.go`
- `internal/video/gpu_music_renderer.go`
- `docs/superpowers/specs/2026-07-15-unified-music-scene-behavior-matrix.md`

## Detailed normative contract

This section turns the inventory into an implementation and test contract.
Values below are taken from the cited Go code; when a value is not yet
observable in a fixture it is explicitly marked as an open point rather than
being inferred.

### A. Analysis contract

`AnalyzeAudioForKind` decodes signed little-endian 16-bit stereo PCM through
FFmpeg, resampling to 48,000 Hz and averaging the two channels to mono. Music
sources use `loudnorm=I=-14.0:TP=-1.0:LRA=11.0` both for analysis and render.
The analysis result is `{FPS:30, Duration: monoSamples/48000, Frames, Features}`.
The analyzer uses an 8,192-sample Hann-window FFT, advances by 1,600 samples,
and emits `ceil(Duration*30)` frames, padding the final windows with zeroes.
Each frame contains 24 log bands. Band `b` covers
`20*1000^(b/24)` through `20*1000^((b+1)/24)` Hz, averaged over FFT bins.
Track normalization and attack/release motion smoothing are applied in
`finalizeSpectrumFrames`; these operations, including their clamping, are
part of the fixture rather than shader-side policy.

Loudness blocks are RMS of normalized mono samples (`sample/32768`) over each
10 ms (480-sample) block, linearly resampled to exactly 1,000 samples.
`normalizeRelativeLoudness` and `SmoothLoudnessTrend` are applied at render
time. BPM uses positive onset differences at 100 Hz and normalized
autocorrelation for 60–200 BPM. Integrated LUFS is extracted separately via
FFmpeg `ebur128`; absent output falls back to -70 LUFS. Feature clamps are BPM
0..300, LUFS -70..0, low-frequency ratio 0..1, and non-negative centroid.
Invariant tests: frame count equals `ceil(Duration*30)`, frame PTS is
`round(frame/30 seconds)` (nanoseconds in sidecar protocol), and no frame
references an out-of-range analysis index.

### B. Artwork/background contract

The source image is normalized to RGBA before any sampling. Cover selection
and SoundCloud gating must be recorded as explicit metadata in a fixture:
`sourceKind`, selected image index, encoded format, dimensions, and alpha
presence. The background path is: analysis image preparation (32-pixel color
sampling and 3x3 box blur), cover scale/crop to the full 1280x720 canvas,
FFmpeg `gblur=sigma=64`, then adaptive foreground selection. The latter tests
metadata and graph rectangles independently, searches overlay opacity in
5-percentage-point increments up to 60%, and requires WCAG contrast >=4.5:1;
the selected foreground and overlay RGBA values are serialized, not
recomputed by a consumer.

The artwork tile is cover-fit into the scaled Artwork rect, clipped to its
rounded rectangle, and composited with the CPU shadow routine. If no artwork
exists, `PaletteForFeatures` deterministically derives start/end colors from
BPM, low-frequency ratio, spectral centroid, and LUFS; the tile contains the
gradient, 64 fingerprint rays, and note glyph. Fixture assertions must cover
both real-artwork and no-artwork paths, including alpha=0, transparent edges,
and odd aspect ratios.

### C. Layout/typography contract

At 1280x720 the exact rects are: Artwork (96,152,288,288), Title
(432,152,752,58), Artist (432,224,752,34), Album (432,264,752,30), Spectrum
(432,320,752,168), Loudness (64,548,1000,80), Progress (64,650,1000,8), and
Time (1088,632,128,32). `LayoutForSize` independently computes sx=w/1280
and sy=h/720 and rounds every coordinate with `math.Round`; it rejects any
non-positive dimension. Output dimensions must be even before YUV420 encoding.

Text is independently omitted when its normalized value is empty. Canonical
font sizes/weights are Title 48/600, Artist 28/500, Album 24/400. Width and
height are measured by FFmpeg drawtext alpha bounds using the actual selected
font, not a character-count estimate except when no alpha is produced. ASS
uses the same measured widths and font identity. Unicode and Japanese require
the configured fallback chain; missing fonts are a hard fixture failure for a
declared font, while the renderer's documented fallback behavior is tested
separately.

### D. Motion and dynamic layers

For a text width `T` and viewport `V`, no scroll occurs when `T<=V`. Otherwise
`overflow=T-V`, cycle duration is `3 + overflow/40`, phase is modulo cycle,
offset is zero for the first 3 seconds and `-40*(phase-3)` thereafter,
clamped to `-overflow`. Title, Artist, and Album each have an independent
state and clip padding (`ASSClipPadding`); test at t=0, 2.999, 3.0, cycle end,
and a second cycle.

Spectrum draws 24 bars in the Spectrum rect from the finalized frame values,
with the CPU bar gap, bottom fade, color, alpha, and antialiasing constants.
Waveform is not a derived spectrum: it is FFmpeg `showwaves=mode=line:rate=30`
over the *filtered* audio signal, opacity 0.55, composited at the canonical
waveform rect. The exact filter string and tail policy must be captured in
the fixture command line. Loudness is rasterized at 4x graph resolution,
with guide alpha .22, detail alpha .80/2px, trend alpha .95/3px (at 720p),
monotone cubic Hermite interpolation for trend, and Lanczos-3 downsampling.
Progress uses the clamped ratio `current/duration`, the Progress rect rail,
and the CPU thumb radius/color; time uses `FormatMediaTime`: `M:SS` below one
hour and `H:MM:SS` otherwise, with negative values clamped to zero.

### E. Output/lifecycle contract

The CPU HLS path must be compared from the generated FFmpeg arguments, not
from defaults: 30 fps, HLS event playlist, 4-second independent segments,
H.264, AAC 48 kHz stereo, and synchronized audio/video duration. MP4 must
retain H.264/AAC, `+faststart`, declared GOP/profile options, optional 0.7 s
audio/video fades, and per-track duration limits. Cancellation must terminate
FFmpeg and remove partial output. Every GPU protocol error maps to a stable
`gpu_required` or `gpu_renderer_unavailable` result; malformed RGBA length,
row stride, frame index, and PTS are rejected before muxing.

## Fixture and verification plan

Each fixture is a JSON manifest plus PNG/PCM artifacts and the exact FFmpeg
command line. Required fixtures are: (1) embedded opaque cover, (2)
transparent cover, (3) no artwork fallback, (4) long Japanese metadata that
scrolls, (5) short metadata that does not scroll, (6) silent/near-silent
audio, (7) high-BPM/high-LUFS audio, and (8) a multi-track playlist with
per-track reset. For each fixture record the analysis JSON, canonical layout,
foreground mode, per-frame feature index, and CPU frame hashes at start/mid/end.

Verification is layered: unit tests for formulas and boundary times; golden
tests for each isolated layer with alpha-aware pixel diffs; integration tests
for raw RGBA frame count/PTS and muxed duration; and end-to-end HLS/MP4 tests
that decode screenshots at start/mid/end. A parity result is valid only when
all non-raster invariants match and pixel differences are reported separately
for background, artwork, graph, text, and UI regions. A single aggregate RMSE
or timing pass is insufficient evidence.

## Dependency graph and open points

Analysis → artwork preparation → background/foreground mode → layout/text
metrics → static base image → per-frame spectrum/waveform/loudness/progress →
encode/mux. Waveform and audio mux must consume the same filtered signal.
Open points requiring an explicit decision before implementation: the exact
CPU waveform rectangle source (currently inferred from the filtergraph), the
font fallback order on each platform, the playlist transition/fade contract,
and whether HLS tail padding is a presentation requirement or only an encoder
workaround. These must not be silently approximated by a GPU implementation.

## Zero-based GPU execution plan

This is the implementation order for a new GPU renderer, not authorization to
change the production route. The CPU renderer remains the reference until all
waves pass and the owner approves the final switch.

### Operating rules

- Freeze CPU fixtures before implementation; fixtures are authoritative for
  values, geometry, layer order, and output lifecycle.
- Use one versioned canonical scene contract for CPU fixture generation, GPU
  packing, shaders, and comparison tooling; do not duplicate formulas in WGSL.
- Prohibit `len+6` tail padding, spectrum-derived waveform synthesis, guessed
  glyph bitmaps, aggregate-only RMSE, and timing-only acceptance. Waveform
  samples must come from the exact filtered `showwaves` signal.
- Every wave is independently reversible; a failed gate stops downstream work
  and leaves production unchanged.

### Dependency-ordered waves

| Wave | Tier ownership | Primary scope | Acceptance / stop condition |
| --- | --- | --- | --- |
| 0. Fixture freeze | Tier4 designs, Tier2 implements, Tier5 audits | `internal/video/*visualizer*.go`, `cmd/music-render-compare`, `testdata/music-render/` | Eight required fixtures contain analysis JSON, canonical rects, exact FFmpeg commands, CPU PNG/hash snapshots, and per-region masks. Stop on inferred or non-deterministic values. |
| 1. Canonical contract | Tier4 schema, Tier3 adapters, Tier5 review | New `internal/video/music_scene_contract.go` and tests | A versioned contract serializes metadata, normalized features, artwork/background, layout, text metrics/motion, samples, frame/PTS, and output recipe. Stop on duplicate formulas or out-of-range indices. |
| 2. Static base | Tier3 implementation, Tier4 review | `fallback_artwork.go`, `visualizer_background.go`, `visualizer_color.go`, Rust scene/fragment shaders | Sampling/blur/overlay, cover-fit artwork, rounded clip, shadow, and fallback match isolated CPU goldens. Stop on palette, alpha, or geometry mismatch. |
| 3. Glyph/text | Tier3 implementation, Tier4 font decision, Tier5 audit | `visualizer_ass.go`, atlas packing, Rust text shader | Actual font/fallback, measured widths, content, independent scroll, clipping, alpha and fade match goldens. Stop on placeholder glyphs or guessed widths. |
| 4. Spectrum/waveform | Tier3 implementation, Tier4 signal review | `audio_visualizer.go`, waveform filtergraph, Rust graph shaders/buffers | 24 finalized bands match; waveform comes from exact filtered `showwaves`, never spectrum values. Stop on synthetic waveform, guessed tail, or `len+6`. |
| 5. Loudness/UI | Tier3 implementation, Tier4 raster review | `visualizer_loudness.go`, `visualizer_layout.go`, Rust UI shaders | Envelope/trend, guides, 4x+Lanczos result, progress/time/edge fade and rects match isolated goldens. Stop on aggregate-only metrics or geometry drift. |
| 6. Encode/mux | Tier3 implementation, Tier4 architecture, Tier5 release gate | GPU sidecar, HLS/MP4 FFmpeg builders | Raw frame count/PTS and HLS/MP4 codec, fades, durations, cancellation and cleanup match CPU recipe. Stop on duration padding hiding mismatch. |
| 7. Per-region gates | Tier2 harness, Tier3 fixes, Tier5 sign-off | `cmd/music-render-compare`, parity tests, fixture reports | Background, artwork, spectrum, waveform, loudness, text, UI each report pixel/alpha diff, geometry, frame count and PTS. Single RMSE or timing pass is insufficient. |
| 8. Adapter matrix | Tier3 executes, Tier4 interprets, Tier5 audits | wgpu selection, CI/scripts/docs | NVIDIA, AMD, macOS Metal, and unsupported/error paths have captured adapter/features and short/long/playlist evidence. Missing hardware is unverified, never passed by assumption. |

### Handoff and review cadence

Tier4 first resolves the font, waveform rectangle, playlist transition, and
tail-policy decisions and writes the architecture. Tier5 performs a read-only
contract audit. Tier3 then implements waves in order and updates the evidence
ledger after each wave. Tier5 performs a second audit. The owner alone decides
whether to execute or switch the production route.

### Rollback and stop conditions

Land each wave on an isolated commit with fixture and test commands. Revert the
wave, rather than patching around it, if a CPU invariant changes, a fixture is
regenerated without a contract-version change, a shader duplicates a formula,
PTS diverges, a region gate fails, or an adapter is silently substituted.
Production remains unchanged until owner approval.
