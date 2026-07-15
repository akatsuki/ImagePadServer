# Unified Music Scene: Current Behavior Matrix

Status: T0 frozen reference (2026-07-15)

This matrix records the behavior that the unified `MusicSceneSpec` migration
must preserve. It is intentionally descriptive: it does not change routing or
declare the existing CPU path to be the target production architecture.

## CPU reference inventory (T3)

This is the source-of-truth inventory for the CPU reference renderer. The GPU
scene must consume the same normalized values and preserve ownership, geometry
and timing; this is a behavioral contract, not a pixel-golden test.

| Element | CPU implementation / evidence | Fixture assertion |
|---|---|---|
| Artwork tile | `SelectArtwork`, `ExtractEmbeddedArtwork`, `visualizer_color.go`, `RenderFallbackArtwork` | Front Cover wins; otherwise largest area/bytes; SoundCloud art is source-gated; cover-fit, rounded tile and shadow use `Layout.Artwork`; no artwork is a deterministic note tile |
| Background/palette | `artworkAccent` and readability helpers in `visualizer_color.go` | 32px analysis, neutral/chroma clamp and readable foreground are preserved; dim/gradient overlay owns the canvas behind artwork |
| Metadata | `ResolveAudioMetadata`, `BuildVisualizerASSWithMode`, `MeasureTextWithFFmpeg` | title/artist/album are independently optional; empty fields emit no command; title 600/48px, artist 500/28px, album 400/24px at 1280px; Unicode uses resolved fallback faces |
| Text motion | `ScrollOffset`, `buildScrollingDialogue` | 3s pause, 40 canonical px/s, right-edge clamp, cycle reset; clip padding `max(1, round(2*width/1280))` |
| Spectrum/waveform | `streamAnalyzer`, `drawSpectrumFixedFade`, waveform recipe | 24 log bands; frame-clock indexed features; fixed bottom fade; waveform uses the canonical rectangle |
| Loudness/trend/guides | `renderLoudnessLayer` and `visualizer_loudness.go` | detailed envelope, smoothed trend and guide lines occupy `Layout.Loudness`; quiet/loud fixtures differ |
| Playback UI | ASS time events and progress recipe | `FormatMediaTime` elapsed/total; rail/thumb and edge fade are frame-time driven; start/mid/end cover final state |
| Scaling | `LayoutForSize` from `baseLayout` | 360/720/1080 heights use `math.Round` x/y scaling; every element box is recorded |

### Deterministic fixture set

The fixture names are `embedded-cover-latin`, `no-artwork-fallback`,
`unicode-japanese-long-scroll`, `quiet-envelope`, `loud-envelope`, and
`fade-start-mid-end`. Each records source identity, metadata presence, artwork
ownership, feature-frame index, output size (640x360, 1280x720, 1920x1080),
element rectangles and expected content ownership. It must not contain encoded
video bytes or depend on installed fonts/FFmpeg output; those belong to the
integration gate.

## Rendering and layout

| Contract | Single-track HLS (`RunAudioVisualizerHLS`) | Playlist track (`RenderRadioTrack`) | Unified target / tolerance |
|---|---|---|---|
| Canvas | `height` from `QualityPreset` (default 720), 16:9; even width | Same `radioFallbackOutputSize` 16:9 sizing | Canonical 1280x720 scaled to 640x360, 1280x720, 1920x1080; integer rounding only |
| Frame rate | 30 fps raw-video input | 30 fps sidecar requests | Exactly 30/1; frame index is the clock |
| Waveform geometry | width `round(752*w/1280)`, height `round(168*h/720)`, x `round(432*w/1280)`, y `round(320*h/720)` | Recipe records the same geometry | Same normalized rectangles; rasterization/antialiasing may differ |
| Waveform appearance | FFmpeg `showwaves`, line mode, 0.55 opacity | GPU frame currently supplies the visual; FFmpeg must not add `showwaves`/`ASS` | Same color, opacity, line envelope, and placement; pixel differences allowed for GPU rasterization |
| Background/artwork | Prepared base PNG: blurred background, artwork/fallback tile, overlay | GPU sidecar owns the frame scene | Same artwork crop, fallback choice, overlay, and palette |
| Metadata text | ASS/libass; title 48px, artist 28px, album 24px at canonical scale; title scrolls when needed | GPU scene owns text in the migration path | Same text boxes, weights, fallback order, clipping, and scroll timing; glyph edge pixels may differ |
| Fade | HLS path has no edge fade by default | Playlist edge fade is 0.7s when duration > 1.4s | Same scene fade parameters when the caller requests a fade; audio/video tails remain format-specific |

## Feature timing and PTS

| Contract | Frozen behavior |
|---|---|
| Feature sample window | `AudioAnalysis.Frames` is consumed in frame order by the CPU reference writer; the GPU contract carries quantized spectrum/RMS/peak plus sample rate. |
| Frame clock | `frameIndex / 30` seconds; playlist sidecar sends `pts_ns = frameIndex * 1s / 30`. |
| Ownership | The producer owns sequence and PTS; FFmpeg consumes raw frames and must not synthesize a second video clock. |
| Monotonicity | PTS must be non-decreasing within a job; restart/new track starts a new sequence at zero. Discontinuities are an error for a single job, not silently repaired. |
| Duration | Playlist emits `ceil(duration*30)` frames (minimum one); HLS reference uses the analysis frame fixture and FFmpeg input duration. The unified producer must use one explicit frame-count policy. |

## Pixel format and transport

| Contract | Frozen behavior |
|---|---|
| Sidecar output | `GpuFrame` schema 1, RGBA8, sRGB, owned by transport, row stride >= `width*4` and aligned to 256 bytes. |
| FFmpeg playlist tail | Current GPU playlist path receives packed RGBA (`-f rawvideo -pix_fmt rgba`) and lets FFmpeg convert to the encoder format. |
| FFmpeg HLS tail | Current CPU reference path receives planar `yuv420p` (`-pix_fmt yuv420p`); its CPU conversion is reference-only during migration. |
| Target handoff | Both paths consume the same GPU RGBA frame producer. Conversion to encoder format belongs at the shared mux boundary; no duplicate showwaves/ASS filter graph is permitted. |
| Backpressure | Pipe writes are blocking and context-cancellable; a failed FFmpeg write or sidecar response fails the job and cleans partial output. |

## Mux tails

| Output | Tail-specific responsibilities |
|---|---|
| Single-track HLS | HLS event playlist, 4s segments, independent segments, AAC 48kHz stereo; no playlist edge fade unless explicitly requested. |
| Playlist MP4 | H.264/AAC, `+faststart`, track duration limit, optional 0.7s audio/video edge fades, radio GOP/latency profile. |
| Shared | 30 fps, 16:9 dimensions, encoder selection, GPU-required errors, cancellation, and frame/PTS contract. |

## Cancellation and error behavior

- Missing `IMAGEPAD_PLAYLIST_COMPOSITORD` fails closed with `gpu_required` for
  production playlist rendering.
- Sidecar start, hello, render, malformed frame, or adapter failure maps to
  `gpu_renderer_unavailable`; partial FFmpeg output is killed/removed.
- Context cancellation must terminate both FFmpeg and sidecar and return the
  context error (or a wrapped transport error), without leaving a running
  child process.
- A sidecar restart is a new session and resets sequence/PTS; it must not
  append frames to the old job clock.

## Intentional rasterization tolerance

The migration target is specification parity, not byte-identical pixels.
Allowed differences are GPU/CPU glyph antialiasing, line coverage at fractional
coordinates, and color conversion rounding (one code value per channel). Not
allowed are changes to geometry, text ownership/content, feature normalization,
fade windows, frame count, PTS order, alpha/color-space contract, or mux stream
parameters.
