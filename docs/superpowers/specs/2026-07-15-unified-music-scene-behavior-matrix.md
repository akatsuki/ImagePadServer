# Unified Music Scene: Current Behavior Matrix

Status: T0 frozen reference (2026-07-15)

This matrix records the behavior that the unified `MusicSceneSpec` migration
must preserve. It is intentionally descriptive: it does not change routing or
declare the existing CPU path to be the target production architecture.

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

