# Tier4 GPU parity architecture (zero-based rework)

Status: architect decision record; preparation only.  The CPU visualizer remains
the reference route and no production switch is authorized by this document.

## Decision

The current wgpu sidecar is a transport and shader smoke implementation, not a
CPU-equivalent renderer.  It currently approximates a palette background,
artwork sample, spectrum glow, and glyph instances.  It does not yet implement
the CPU scene graph.  Adding more constants to the existing WGSL is therefore
rejected.  The GPU route must be rebuilt around a versioned `MusicScenePayload`
whose values are prepared once by Go and consumed by both the GPU packer and
the parity comparator.

The CPU route is not modified.  Until every wave below passes, the GPU route is
opt-in and a failed GPU render is fail-closed (`gpu_renderer_unavailable`).

## Observable CPU contract to reproduce

The following are separate parity domains; a single whole-frame RMSE cannot
substitute for any domain:

1. **Audio/clock**: 48 kHz stereo AAC input, the `loudnorm=I=-14.0:TP=-1.0:LRA=11.0`
   filtered branch, mono analysis, 8,192 Hann FFT, 24 logarithmic bands,
   1,000-point loudness envelope/trend, `ceil(duration*30)` frames, and PTS
   exactly `i/30` from zero.
2. **Background/artwork**: cover selection and SoundCloud gating, RGBA
   normalization, 32-pixel palette analysis, 3x3 analysis blur, full-canvas
   `gblur=sigma=64`, adaptive WCAG foreground/overlay, centered cover-fit,
   rounded tile, CPU shadow, and deterministic no-artwork palette/fingerprint
   tile.
3. **Layout/typography**: canonical 1280x720 rectangles with independently
   rounded scale, optional title/artist/album, measured glyph advances, pinned
   font/fallback identity, UTF-8/Japanese/RTL shaping, and independent
   three-second/40 px/s scroll cycles.
4. **Dynamic layers**: frame-indexed spectrum bars; filtered-audio
   `showwaves=mode=line:rate=30` samples (never synthesized from spectrum);
   loudness guide/detail/trend curves; progress rail/thumb; elapsed/total time;
   edge fades and track reset semantics.
5. **Encoding/lifecycle**: HLS event playlist, independent four-second
   segments, H.264/AAC 48 kHz stereo, BT.709 limited-range yuv420p, exact
   duration/no presentation padding, cancellation precedence, child
   termination, and partial-output cleanup.

## Gap matrix (current sidecar versus required)

| Domain | Current implementation | Required replacement | Priority |
|---|---|---|---|
| Background | palette approximation and local glow | CPU palette/blur/overlay graph with serialized RGBA constants | P0 |
| Artwork | one texture sample in tile | cover-fit crop, alpha, rounded clip, shadow, fallback tile | P0 |
| Typography | guessed atlas glyphs and character iteration | HarfBuzz/FreeType shaped runs, atlas bounds and scroll phase | P0 |
| Spectrum | simple bars/glow | finalized 24-band values, gap/fade/AA constants | P1 |
| Waveform | no filtered `showwaves` image | upload exact filtered showwaves RGBA layer | P0 |
| Loudness | no CPU curve rasterizer | upload 1,000-point envelope/trend and rasterize at 4x | P1 |
| Progress/time | absent/approximate | exact frame clock, clamp, formatting and fade | P1 |
| Color | sRGB RGBA readback | explicit straight-alpha composite and BT.709/tv encode metadata | P0 |
| Frames/PTS | sidecar accepts caller values | shared frame manifest; zero PTS; no tail padding | P0 |
| Mux tail | CPU ffmpeg probe may add 0.1 s encoder tail | compare raw frame domain separately from mux domain; reject unexplained tail | P0 |
| Cleanup | basic child lifecycle | bounded cancel/cleanup/error-precedence evidence | P1 |

## Minimal implementation waves

### Wave A — canonical scene and raw-frame contract (Tier3)

Extend the existing scene payload with a schema-versioned static layer and
dynamic layer: background RGBA/palette mode, artwork metadata, shaped glyph
runs, waveform RGBA, loudness samples, progress/time state, and frame manifest.
All arrays carry lengths, hashes, and source/toolchain fingerprints.  GPU
rendering consumes these values; it must not reimplement audio analysis,
font selection, cover selection, or waveform synthesis in WGSL.

Acceptance: fixture JSON can reconstruct every CPU layer and validates dimensions,
stride, alpha mode, frame index, and PTS before the sidecar is invoked.

### Wave B — static compositor (Tier3)

Implement separate WGSL passes (or deterministic compute stages) for background,
blur/overlay, artwork tile/fallback, and alpha-composited base.  Use CPU fixture
constants for crop origin, radius, shadow kernel, and palette.  Keep all output
in linear-light intermediate space; convert only at the encode boundary.

Acceptance: static-layer region gates pass for opaque, transparent, odd-aspect,
and no-artwork fixtures at start/mid/end.

### Wave C — shaped text and UI (Tier3)

Pack glyph atlas alpha plus shaped glyph instances (`glyph id`, atlas rect,
advance, baseline, color, opacity, clip).  Implement independent scroll state,
fade, time/progress, and edge-fade sampling from the shared frame manifest.
No character-count width or missing-glyph substitution is permitted in a
declared fixture.

Acceptance: boundary samples at 0, 2.999, 3.000, cycle end, and second cycle
match CPU text/UI region thresholds; Latin, Japanese, combining, RTL, and emoji
fixtures are required.

### Wave D — dynamic layers (Tier3)

Upload exact `showwaves` RGBA generated from the filtered audio branch.  Add
24-band spectrum, 4x loudness curve rasterization/downsample, and progress
state.  Waveform is an image/sample layer, never a spectrum-derived shader.

Acceptance: per-region gates pass at seven canonical frame samples plus all
scroll/fade boundaries; feature index and source hash match the CPU report.

### Wave E — mux/lifecycle evidence (Tier2, reviewed by Tier5)

Encode GPU raw frames with the same ffmpeg argument vector and color metadata as
the CPU reference.  Compare raw frame count/PTS before probing HLS.  Report both
domains explicitly: `rawFrames`, `rawPts`, `muxFrames`, `muxDuration`, and
`muxTail = muxDuration - rawDuration`.  A mux tail is diagnostic only; it must
never be hidden by `len+6` padding or by shifting PTS.  Cancellation must close
the pipe, terminate children within 2 seconds, and leave no partial artifacts
within 5 seconds.

Acceptance: all fixture reports include ffprobe output, actual sidecar
HelloAck adapter/backend/toolchain fingerprint, hashes, and cleanup evidence.

## Tier5 GO gate

Tier5 may return **GO** only when all are true:

- CPU production route is unchanged and GPU remains opt-in.
- All eight frozen fixtures pass every region threshold in the CPU inventory;
  no aggregate-only score is accepted.
- Raw GPU frame count is exactly `ceil(duration*30)` and PTS is zero-based,
  monotonic, and equal to `i/30`.
- GPU waveform source hash equals the filtered CPU `showwaves` source hash.
- ffprobe confirms BT.709, limited range, yuv420p, 30 fps, H.264/AAC and no
  unexplained presentation padding.  Any mux tail is reported, bounded, and
  reproduced identically by the reference command.
- HelloAck fingerprint is captured from the running sidecar, not supplied only
  by an environment variable; NVIDIA, AMD iGPU, and macOS matrices have either
  passing evidence or an explicit unsupported result.
- Cancellation, malformed-frame rejection, child termination, and cleanup
  tests pass with the stated time bounds.
- Tier5 independently re-runs the evidence commands from a clean checkout and
  signs the gate.  Only the owner can then authorize a production switch.

## Ownership and non-goals

Tier4 owns this contract and resolves parity ambiguities.  Tier3 owns scene
packing, WGSL passes, and integration tests.  Tier2 owns fixtures/reporting and
bounded lifecycle tests.  Tier5 is read-only final reviewer.  This work does
not include deleting the CPU renderer, silently enabling a fallback, changing
the release route, or accepting approximate visuals as parity.
