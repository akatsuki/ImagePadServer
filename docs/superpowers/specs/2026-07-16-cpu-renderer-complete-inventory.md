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

## Dependency graph and resolved Tier4 decisions

Analysis → artwork preparation → background/foreground mode → layout/text
metrics → static base image → per-frame spectrum/waveform/loudness/progress →
encode/mux. Waveform and audio mux must consume the same filtered signal.
The following decisions close the Tier5 P0/P1 questions. A decision is still
subject to the read-only Tier5 recheck; it does not authorize implementation.

### D1. Numeric region gates (owner: Tier4; verifier: Tier5)

All comparisons use decoded *linear-light* RGBA8 frames after the same
BT.709 conversion. Regions are the canonical rectangles in §C, expanded by
one pixel for antialiasing. Required samples are frame 0, `round(0.10N)`,
`round(0.25N)`, `round(0.50N)`, `round(0.75N)`, `round(0.90N)`, and `N-1`;
scroll/fade boundaries add the samples in D8. For each region and sample,
the gate reports RGB MAE, RGB RMSE, RGB max, alpha MAE, and changed-alpha
coverage (pixels with |A_cpu-A_gpu|>1). Pass thresholds are:

| Region | MAE | RMSE | Max | alpha MAE | changed-alpha |
|---|---:|---:|---:|---:|---:|
| background/overlay | <=2.0 | <=6.0 | <=32 | <=1.0 | <=2.0% |
| artwork/fallback | <=1.5 | <=5.0 | <=24 | <=1.0 | <=1.0% |
| spectrum/waveform/loudness | <=2.0 | <=7.0 | <=40 | <=1.5 | <=2.0% |
| text | <=1.0 | <=4.0 | <=24 | <=1.0 | <=1.0% |
| progress/time/edge fade | <=1.0 | <=4.0 | <=24 | <=1.0 | <=1.0% |

Every sampled frame must pass every applicable region; no aggregate score may
substitute for a failed region. A max error above the limit or a NaN is an
immediate failure. Thresholds are intentionally measured against CPU goldens,
not guessed from the current GPU output. Evidence command:
`go test ./cmd/music-render-compare -run Test.*Region -count=1` (Tier2).

### D2. Waveform, fonts, transitions, and tail (owner: Tier4)

The waveform source is the filtered branch created in
`internal/video/audio_visualizer.go`: `[1:a]loudnorm=I=-14.0:TP=-1.0:LRA=11.0,
asplit=2[aud][wsrc];[wsrc]showwaves=s=752x168:rate=30:mode=line:colors=...`.
The canonical rect is `(432,320,752,168)` and opacity is 0.55. The exact
filter string, color, and FFmpeg build fingerprint are serialized in every
fixture; GPU receives the decoded showwaves RGBA samples, never spectrum data.

Font resolution is pinned per platform: Windows `C:/Windows/Fonts/segoeui.ttf`
then `meiryo.ttc`; macOS `/System/Library/Fonts/SFNS.ttf` then
`Hiragino Sans`; Linux `/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf` then
`NotoSansCJK-Regular.ttc`. The first font that contains every shaped glyph is
selected; mixed-run fallback is forbidden in a single golden unless recorded
as separate runs. Fixtures include Latin, Japanese, combining marks, and RTL.
The fixture stores SHA-256, FreeType/fontTools version, glyph IDs, advances,
clusters, and atlas bounds. Missing declared fonts fail the fixture.

Playlist transitions use the existing radio contract: edge fades are disabled
unless `EdgeFadeSeconds>0` and duration is greater than twice that value; when
enabled both audio and video use the same 0.7-second fade (smootherstep only in
radio fallback). Tracks reset scene state at their first frame; crossfade is
not implicit. HLS has no presentation padding: frame count is exactly
`ceil(Duration*30)`, PTS is `i/30`, and the final segment may be shorter than
4 seconds. Any extra tail frame is an encoder defect, not a workaround.

### D3. Pixel and color contract (owner: Tier4; Tier3 adapter)

Sidecar input is `rgba` (straight alpha, row stride `width*4`) at 1280x720 and
30fps. FFmpeg output is `yuv420p`, BT.709 primaries/transfer/matrix,
`tv` (limited) range, 8-bit 4:2:0 chroma with MPEG2 chroma location, and
explicit `scale=in_range=full:out_range=tv:in_color_matrix=bt709:out_color_matrix=bt709`.
Alpha is composited against the CPU background before conversion; no alpha
channel reaches H.264. `-colorspace bt709 -color_primaries bt709
-color_trc bt709 -color_range tv` are mandatory and ffprobe must confirm them.

### D4. Static raster constants (owner: Tier4; source evidence required)

Cover fit is centered, preserving aspect ratio; crop uses floor origin and
`xdraw.CatmullRom.Scale` as implemented by `visualizer_background.go`.
Background analysis is 32x32 then a 3x3 box kernel; full-canvas blur is
`gblur=sigma=64` with edge pixels clamped. Tile corners use the canonical
radius from `RoundedRect` (12 px at 720p, scaled and rounded); shadow is the
CPU `renderShadow` routine with its source-defined offset, blur radius, and
20% opacity (all three values are serialized in the fixture). Fallback uses `PaletteForFeatures`, 64 rays, width 3 px,
foreground alpha 0.26, and the note glyph constants in
`internal/video/fallback_artwork.go`. Any differing constant must be added to
the fixture rather than shader-local. Evidence: the cited source files plus
`go test ./internal/video -run 'Test.*(Fallback|Background|Artwork)'` (Tier2).

### D5. Version and adapter matrix (owner: Tier4; executor: Tier3)

Fixtures record `ffmpeg -version`, `ffprobe -show_versions`, Go version,
wgpu version, shader compiler version, and font file hashes. A fixture is
portable only when all fingerprints are present; version drift requires a new
contract version. Adapter matrix:

| Adapter | Required backend/features | Evidence | Unsupported result |
|---|---|---|---|
| NVIDIA | Vulkan/DX12, storage buffers, RGBA8 render target | adapter info + short/long/playlist | `gpu_renderer_unavailable` |
| AMD iGPU | Vulkan/DX12, same limits | same | `gpu_renderer_unavailable` |
| macOS Apple GPU | Metal, storage buffers, same limits | same | `gpu_renderer_unavailable` |
| no adapter/remote software | none | hello failure log | `gpu_required` |

No backend substitution is silently accepted. Evidence command is the sidecar
hello plus `cmd/music-render-compare` short/long/playlist runs (Tier3).

### D6. Cancellation, cleanup, and error precedence (owner: Tier4)

Cancellation must close the raw-frame pipe and terminate both child processes
within 2 seconds, remove playlist segments/temp MPEG-TS/partial MP4, and leave
no sidecar child after 5 seconds. A malformed frame (size/stride/index/PTS)
has precedence over mux failure; explicit context cancellation has precedence
over child exit; adapter/hello failure maps to `gpu_renderer_unavailable`, and
`gpu_required` is returned only when GPU was explicitly required. Tests must
assert error class, cleanup, and the 2s/5s bounds using temporary directories.

### D7. CPU evidence completeness (owner: Tier4; audit: Tier5)

The call-path evidence set is the source files listed above plus
`audio_visualizer_test.go`, `radio_recipe.go`, `radio_render.go`, and
`gpu_*_test.go`; each fixture stores the exact generated argument vector and
the source commit. A missing call-path file or inferred value is a P1 failure.

### D8. Boundary samples (owner: Tier3; audit: Tier5)

For each scrolling field sample `t={0,2.999,3.000,3.001,cycle-0.001,
cycle,cycle+0.001}`. For edge fades sample `t={0,0.001,0.699,0.700,
duration-0.700,duration-0.001,duration}`. For progress sample frame 0,
first/middle/last and exact duration clamp. These samples are mandatory in
the JSON report and must pass the text/UI gates in D1.

### D9. Shaping fixtures (owner: Tier4; implementation: Tier3)

The font fixture set contains `Café`, `日本語タイトル`, Arabic RTL, emoji,
and mixed Latin/CJK strings, with expected grapheme clusters, glyph IDs,
advances, baseline, and fallback run boundaries. HarfBuzz/FreeType shaping
versions and font hashes are recorded. A placeholder bitmap, character-count
width, or platform-default fallback without a recorded hash fails Wave 3.

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

## Tier4 closeout addendum (2026-07-16)

This addendum closes the remaining specification questions without changing
code or authorizing the production route.

### Fixture set and manifest schema

The frozen fixture directory is `testdata/music-render/<fixture-id>/`. It must
contain `manifest.json`, `analysis.json`, `cpu/start.png`, `cpu/mid.png`,
`cpu/end.png`, `cpu/frame-hashes.json`, and (when applicable) `audio.pcm` and
`artwork.*`. The eight required IDs are:

1. `embedded-opaque-cover`
2. `transparent-cover`
3. `no-artwork-fallback`
4. `long-japanese-scroll`
5. `short-metadata-no-scroll`
6. `silent-near-silent`
7. `high-bpm-high-lufs`
8. `playlist-track-reset`

Every manifest is versioned and has this shape (unknown fields are rejected):

```json
{
  "schemaVersion": "music-render-fixture.v1",
  "id": "embedded-opaque-cover",
  "source": {"audio":"audio.opus", "artwork":"artwork.png", "metadata":{"title":"...","artist":"...","album":"..."}},
  "expected": {"fps":30, "width":1280, "height":720, "frameCount":0, "durationSeconds":0},
  "analysis": {"sha256":"", "json":"analysis.json", "filterGraph":""},
  "artwork": {"sourceKind":"embedded", "selectedIndex":0, "format":"png", "width":0, "height":0, "hasAlpha":true},
  "layout": {"contract":"canonical-1280x720-v1", "rects":{}},
  "text": {"fontPath":"", "fontSha256":"", "runs":[]},
  "samples": {"frames":[], "times":[], "regions":{}},
  "cpu": {"commit":"", "frameHashes":"cpu/frame-hashes.json", "images":{"start":"cpu/start.png","mid":"cpu/mid.png","end":"cpu/end.png"}},
  "toolchain": {"ffmpeg":"", "ffprobe":"", "go":"", "wgpu":"", "shaderCompiler":"", "fontRasterizer":""},
  "commands": {"analyze":[], "renderCpu":[], "renderGpu":[], "compare":[]}
}
```

`expected.frameCount` is `ceil(durationSeconds*30)` and must be populated by
the generator, never hand-edited. Generation is deterministic and uses:
`go run ./cmd/music-render-fixture -id <fixture-id> -out testdata/music-render/<fixture-id>`.
Validation is `go test ./cmd/music-render-compare -run 'TestFixture(Metadata|Schema|Region)' -count=1`.
The generator records command vectors, source commit, hashes, and toolchain
fingerprints in the manifest; a missing artifact is a P1 failure.

### Source-aligned raster decisions

The current CPU evidence is authoritative: artwork/background cover scaling
uses `xdraw.CatmullRom.Scale` in `visualizer_background.go` (not bilinear),
and the shadow constants are read from `renderShadow` at fixture generation
time rather than prescribed by prose. The fixture records corner radius,
blur radius, offset, and effective alpha. The embedded font contract is the
three committed `NotoSansJP-{Regular,Medium,SemiBold}.ttf` files in
`internal/video/fonts`; platform font paths are only an optional diagnostic,
not the CPU golden source. Their SHA-256 and `font.go` family mapping are
mandatory evidence. These decisions replace any conflicting values in D2/D4.

### Exact frame/tail policy

The canonical frame count is exactly `len(Analysis.Frames)` (equivalent to
`ceil(Duration*30)`). `music_scene.go` may clamp a lookup index to the final
analysis frame for defensive reads, but the renderer must never emit a frame
outside that count. The current `audio_visualizer.go` `len(Frames)+6` path is a
P1 implementation defect and is explicitly assigned to Wave 4/6: remove it,
then assert raw frame count, PTS `i/30`, and decoded HLS duration in a fixture.
No tail padding or seek workaround is permitted.

### Encode/color acceptance

Wave 6 must make the GPU mux command vector explicit: `-pix_fmt yuv420p`,
`-colorspace bt709`, `-color_primaries bt709`, `-color_trc bt709`,
`-color_range tv`, and the declared `scale=in_range=full:out_range=tv` graph.
The acceptance test runs `ffprobe -show_streams -show_format -of json` and
asserts each field, plus frame count/PTS; command presence alone is P1 evidence.

### Cancellation and cleanup tests

The required bounded tests are `TestMusicRenderCancelTerminatesChildren`,
`TestMusicRenderCancelCleansPartialHLS`, `TestMusicRenderCancelCleansTempTS`,
`TestMusicRenderMalformedFramePrecedesMuxError`, and
`TestMusicRenderNoSidecarAfterCancel`. Each uses a temporary output root,
injects a blocking FFmpeg/sidecar stub, cancels the context, and observes:
children gone and pipe closed within 2s, no partial HLS/TS/MP4 within 5s, and
the documented error class/precedence. Process and filesystem observations
must be recorded in the fixture report.

### Evidence fingerprint procedure

The fixture generator captures `ffmpeg -version`, `ffprobe -show_versions`,
`go version`, the Rust/wgpu crate lock versions, shader compiler version, and
SHA-256 of every font. Adapter evidence is captured by the sidecar hello
(`backend`, `adapter`, `features`, `limits`) and stored under
`evidence/adapter-<backend>.json`; no hardware result may be inferred.
