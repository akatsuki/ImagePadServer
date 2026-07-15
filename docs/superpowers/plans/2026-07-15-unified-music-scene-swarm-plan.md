# Unified Music Scene GPU Migration Implementation Plan

> **For agentic workers:** Use task ownership and dependency waves below; each task requires its own focused test cycle and review.

**Goal:** Make single-track and playlist music rendering use one GPU production scene with specification-level visual parity.

**Architecture:** A versioned `MusicSceneSpec` feeds one GPU frame producer; HLS and MP4 differ only at the FFmpeg mux tail. CPU rendering remains reference-only.

**Tech Stack:** Go, Rust, wgpu, WGSL, FFmpeg, JSONL sidecar protocol.

## Global Constraints

- Production music rendering must fail closed with `gpu_required` or `gpu_renderer_unavailable`.
- CPU rendering is reference/test-only.
- Canonical design size is 1280x720, scaled to 360p/720p/1080p.
- Frame rate is 30 fps with monotonic per-job PTS.
- ASS/showwaves/drawtext duplication is prohibited.

---

## Overview

**Generated**: 2026-07-15

## Overview

Unify single-track and playlist music rendering around the versioned
`MusicSceneSpec`. Preserve a CPU reference renderer for comparison only; all
production output uses the GPU frame producer.

## Dependency Graph

```text
T0 ──┬── T1 ──┐
     └── T2 ──┴── T3 ──┬── T4 ──┐
                       └── T5 ── T6
```

## Tasks

### T0: Freeze current behavior and mux contracts
- **depends_on**: []
- **location**: `internal/video/audio_visualizer.go`, `internal/video/radio_render.go`, `internal/server/music_playlist.go`, existing GPU evidence docs
- **description**: Inventory current single-track and playlist layouts, feature windows, PTS ownership, pixel formats, stride, FFmpeg mux tails, cancellation, and adapter/error behavior. Record deterministic reference fixtures before changing routing.
- **validation**: A checked-in behavior matrix identifies every intentional parity target and every tolerated rasterization difference.
- **status**: Completed
- **log**: Behavior matrix and focused contract tests freeze single HLS/playlist geometry, 30fps/PTS, RGBA stride, mux tails, cancellation, errors, restart, and parity tolerances (`a5d2fe6`).

### T1: Extract the canonical scene model
- **depends_on**: [T0]
- **location**: `internal/video/music_scene_spec.go`, `internal/video/audio_visualizer.go`
- **description**: Define versioned scene geometry, palette, feature sample window/timing, normalization, artwork, text boxes, fades, frame rate, PTS ownership, aspect-ratio rounding, and canonical scaling. Populate it from existing CPU layout constants without changing output yet.
- **validation**: Unit fixtures cover 360p/720p/1080p, quiet/loud features, artwork, metadata, fade timing, and audio-clock/frame-index conversion.
- **status**: Not Completed

### T2: Define the Go↔Rust scene protocol
- **depends_on**: [T0]
- **location**: `internal/video/gpu_contracts.go`, `gpu/playlist-compositord/src/contracts.rs`, `gpu/playlist-compositord/src/protocol.rs`
- **description**: Add a backward-compatible scene payload or feature buffer contract with schema validation, bounded sizes, artwork dimensions/alpha/color-space rules, texture upload limits, and explicit glyph-atlas metadata.
- **validation**: Go/Rust encode-decode tests, malformed/oversized payload tests, invalid-range tests, artwork limits, and protocol-version mismatch tests pass.
- **status**: Completed
- **log**: Versioned bounded `MusicScenePayload` with artwork/glyph metadata, validation, invalid-scene errors, and backward-compatible optional Render.scene (`8499a2c`). Go/Rust protocol tests pass.

### T3: Implement the shared GPU scene renderer
- **depends_on**: [T1, T2]
- **location**: `internal/video/gpu_music_renderer.go`, `internal/video/shaders/music_visualizer.wgsl`, `gpu/playlist-compositord/src/gpu_render.rs`
- **description**: Render background, artwork, waveform, spectrum, glow, text ownership, and fades according to `MusicSceneSpec`. Use a pre-rasterized glyph atlas with font identity, fallback order, missing-glyph behavior, Unicode coverage, and atlas bounds defined by T2.
- **validation**: GPU integration fixtures match CPU reference structure/colors within documented tolerances; glyph fallback and Unicode fixtures pass; ASS/showwaves duplication is rejected.
- **status**: Partial
- **log**: `MusicScenePayload` is consumed by the Rust renderer; WGSL renders deterministic background glow, waveform, and 24-band spectrum while preserving scene-absent compatibility (`c8a6a10`). Artwork and bounded glyph atlas RGBA/rect/layout data now validate and upload (`d39cf06`, `38e098a`). Actual text sampling in WGSL remains the final renderer gap.

### T4: Route single-track HLS through the GPU producer
- **depends_on**: [T1, T2, T3]
- **location**: `internal/video/audio_visualizer.go`, `internal/video/gpu_music_renderer.go`
- **description**: Replace production `RunAudioVisualizerHLS` CPU composition with GPU RGBA frames piped to the existing HLS muxer. Preserve explicit RGBA pixel format, row stride, bounded backpressure, process lifetime/cancellation, sidecar restart handling, and GPU-required errors. Keep CPU renderer reference-only.
- **validation**: Single-track HLS fixture verifies codec, pixel format, frame count, duration, PTS monotonicity/discontinuity handling, backpressure, cancellation, sidecar death, and GPU-required errors.
- **status**: Partial
- **log**: GPU-only single-track routing is in place; T5 is now integrating the same canonical scene payload and frame producer with playlist output.

### T5: Converge playlist and single-track integration
- **depends_on**: [T3, T4]
- **location**: `internal/server/music_playlist.go`, `internal/video/radio_render.go`, `internal/video/*_integration_test.go`
- **description**: Ensure both output types consume the same scene/frame producer and differ only in FFmpeg mux tail. Use one deterministic feature/artwork fixture for both.
- **validation**: Same input fixture produces matching scene geometry, colors, text ownership, fades, and normalized feature response within explicit structural/color tolerances; frame drops and PTS discontinuities are rejected.
- **status**: Completed
- **log**: Added `CanonicalMusicScene`; single-track HLS and playlist now send identical frame index, PTS, quantized spectrum, and scene payload through `RenderScene`. Shared-feature parity tests pass (`4f6de7e`).

### T6: End-to-end acceptance and cleanup
- **depends_on**: [T5]
- **location**: `internal/server`, `internal/video`, `docs/PLAYLIST_GPU_COMPATIBILITY.md`
- **description**: Run focused Go/Rust tests, Windows NVIDIA/AMD and macOS smoke/soak, update evidence, and remove obsolete production CPU routing while retaining reference tests. Linux remains contract-only unless a Vulkan runner is provided.
- **validation**: All focused tests pass; CI artifact checks pass; a static architecture/grep test proves no production music path invokes CPU composition; adapter selection, restart, Unicode, and negative cases are covered.
- **status**: In Progress
- **log**: Focused Go tests (44) and Rust tests (20) pass. Full Go suite has one unrelated Windows TempDir cleanup failure. Actual glyph sampling plus final static/runtime audit remain.

## Parallel Execution Groups

| Wave | Tasks | Can Start When |
|---|---|---|
| 1 | T0 | Immediately |
| 2 | T1, T2 | T0 complete |
| 3 | T3 | T1 and T2 complete |
| 4 | T4 | T3 complete |
| 5 | T5 | T3 and T4 complete |
| 6 | T6 | T5 complete |

## Testing Strategy

- Deterministic CPU reference fixtures for quiet/loud audio and artwork.
- GPU frame comparison with structural and color tolerances.
- HLS and MP4 integration checks for timestamps and codec parameters.
- Sidecar death, invalid scene, and missing-adapter negative tests.

## Risks & Mitigations

- Font rasterization differs across CPU/GPU: compare layout and glyph ownership,
  not exact pixels.
- Scene payload grows too large: use bounded normalized feature arrays and
  explicit schema limits.
- Single-track HLS regressions: keep the CPU reference path test-only until
  GPU fixture and runtime acceptance pass.
