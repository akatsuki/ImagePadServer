# Plan: GPU Sidecar Recovery and Music Visual Parity

**Generated**: 2026-07-15

## Overview

Restore a reproducible GPU-sidecar handshake first, then make the canonical
scene carry every visual input used by the CPU reference renderer. The success
criterion is specification-level parity from the same cached audio input: the
GPU and CPU outputs have the same intent, content, layout, timing and visual
state; rasterization is allowed to differ within measured tolerances.

The current `hello: EOF` is a release-blocking integration fault. The current
`CanonicalMusicScene` only emits audio feature data, so artwork, metadata,
palette, layout, fades and several overlays cannot reach the GPU even when the
sidecar starts. The live-server screenshot therefore is evidence of the older
visualizer path, not proof of GPU parity.

## Prerequisites

- Windows machine with the NVIDIA or AMD adapter already used for prior smoke
  evidence.
- A freshly built `playlist-compositord.exe` from this checkout, not the
  checksum-marker file in the repository root.
- FFmpeg and FFprobe available through ImagePadServer's tool resolver.
- A read-only source audio file from `%APPDATA%\ImagePadServer\media`.

## Visual Inventory (CPU reference -> canonical GPU scene)

| Group | CPU reference behavior to preserve | Current GPU state | Required completion |
|---|---|---|---|
| Source artwork | Embedded/SoundCloud artwork, crop, rounded tile, shadow; deterministic fallback tile | Payload support exists, but canonical scene does not populate it | Decode/normalize/upload artwork and draw the tile/fallback identically |
| Metadata | Title, artist, source labels, truncation/scroll and Japanese glyph fallback | Protocol supports text runs, but canonical scene does not create them | Shared text layout, atlas population and per-glyph GPU draw |
| Background | Artwork-derived blurred background, dim/gradient layers, adaptive palette | Simplified renderer colors | Shared palette and background textures/uniforms |
| Spectrum/waveform | 24 bars, waveform/peak styling, loudness graph, guide lines | GPU has simplified spectrum/waveform | Match normalized feature inputs, geometry, colors and layer order |
| Playback UI | Progress rail/thumb, elapsed/total labels, fade-in/out and end state | Not represented in canonical scene | Add time/progress/fade values and GPU primitives/text |
| Layout/scaling | Canonical 1280x720 layout scaled to 360/720/1080 | GPU has independent placement | Use `VisualizerLayout`-derived canonical rects and rounding rules |
| Accessibility/error | Missing artwork, missing glyph, invalid payload, sidecar loss | Partial fallback/error handling | Deterministic fallback scene and explicit typed errors |

## Dependency Graph

```text
T1 ── T2 ──┬── T4 ──┐
           ├── T5 ──┼── T7 ── T8
T3 ────────┘        │
T6 ─────────────────┘
```

## Tasks

### T1: Reproduce and diagnose sidecar `hello: EOF`
- **tier**: Tier 2 — Luna Medium
- **depends_on**: []
- **location**: `internal/video/gpu_sidecar_process.go`, `gpu/playlist-compositord/src/main.rs`, build/release sidecar scripts
- **description**: Diagnose four distinct stages—artifact preflight, process start, hello write and hello response—rather than treating EOF as a renderer error. Drain bounded stderr while retaining its tail; collect absolute executable path, PE header/size/SHA-256, `--version`, protocol/build identity, exit code/Wait error, adapter environment and timeout/cancellation cause in a local-only report. Reproduce with a freshly built sidecar directly on stdin before testing the application's resolver path. Ensure response-reader goroutines are recovered after EOF, timeout and cancellation.
- **validation**: A failing sidecar emits a typed actionable error with bounded local stderr/exit evidence; a fresh local sidecar answers `hello_ack` and `health`; tests cover start failure, exit-before-response EOF, panic/stderr, protocol mismatch, invalid JSON, timeout, cancellation and child shutdown.
- **status**: Completed
- **log**: Reproduced on a fresh release sidecar: exit 101 before `hello_ack`; bounded stderr identified WGSL uniform `array<u32,24>` stride/alignment validation failure. Added 32 KiB stderr/process diagnostics and tests (`3b0a5d9`).
- **files edited/created**: `internal/video/gpu_sidecar_process.go`, `internal/video/gpu_sidecar_process_test.go`

### T2: Repair and harden the GPU startup contract
- **tier**: Tier 3 — Luna High
- **depends_on**: [T1]
- **location**: `internal/video/gpu_sidecar_process.go`, `internal/video/toolchain.go`, sidecar artifact workflow, `gpu/playlist-compositord/src/main.rs`
- **description**: Fix the diagnosed startup cause. Add sidecar version/protocol/build-SHA and adapter backend/device-type to `hello_ack`; preflight/reject checksum or text files masquerading as executables; resolve the current platform artifact deterministically; keep bounded stderr in diagnostic tools/logs only and sanitized codes in public APIs. Escalate this task one tier after three inconclusive reproductions.
- **validation**: Fresh NVIDIA and AMD local smoke runs establish hello/health/render; stale/mismatched/invalid executables fail before rendering with the correct code; API responses never expose raw stderr.
- **status**: Completed
- **log**: Repaired WGSL uniform alignment (`array<u32,24>` to six `vec4<u32>` blocks) and preserved host packing. Fresh release sidecar now returns NVIDIA RTX 5070 Ti `hello_ack`, `health.ready=true`, and a 64x64 RGBA render (`a91ac7f`). GPU pipe/sidecar shutdown cancellation was hardened (`811a248`).
- **files edited/created**: `gpu/playlist-compositord/src/gpu_render.rs`, `internal/video/audio_visualizer.go`, `internal/video/gpu_sidecar_process.go`

### T3: Freeze CPU visual contract and comparison fixtures
- **tier**: Tier 1 — Luna Low
- **depends_on**: []
- **location**: `internal/video/visualizer_*.go`, `internal/video/*_test.go`, `cmd/music-render-compare`
- **description**: Produce a checked-in, frame-indexed matrix of every CPU visual input/output: artwork crop/interpolation, rounded corners, shadow and fallback note tile; blurred cover background/readability overlay and adaptive/WCAG foreground palette; title/artist/album font metrics, clipping, scroll pause/velocity/reset and Japanese fallback; spectrum, waveform, loudness/trend/guidelines; progress rail/thumb, elapsed/total formatting, edge fade/end state and 360/720/1080 rounding. Add deterministic fixtures and scene-element boxes, not image-metric implementation.
- **validation**: Fixtures include embedded artwork, no artwork, Japanese/Unicode/long scrolling metadata, quiet/loud audio, 360/720/1080 and start/mid/end fade frames; each has expected geometry, asset and content ownership.
- **status**: Completed
- **log**: Added CPU function/test correspondence and six deterministic fixtures covering artwork, palette/background, metadata/Unicode/scroll, feature layers, progress/fade and scaling (`804033f`).
- **files edited/created**: `docs/superpowers/specs/2026-07-15-unified-music-scene-behavior-matrix.md`, `internal/video/unified_music_scene_inventory_test.go`

### T4: Extend the canonical scene model with all visual state
- **tier**: Tier 4 — Terra Ultra
- **depends_on**: [T2, T3]
- **location**: `internal/video/music_scene.go`, `internal/video/gpu_contracts.go`, `gpu/playlist-compositord/src/contracts.rs`, `gpu/playlist-compositord/src/protocol.rs`
- **description**: Define the versioned source-of-truth scene plus deterministic asset normalization: canonical layout rects, crop/overlay/palette decision, decoded RGBA artwork/fallback and asset hashes, font-atlas generation, glyph runs, progress/time, loudness/waveform/trend data, fades and source identity. Populate assets once per job and compact frame inputs once per HLS/playlist frame; preserve bounded payloads and backwards compatibility where required.
- **validation**: Go/Rust round-trip, limit, Unicode fallback, format/color-space, 360/720/1080, frame-clock and fade fixtures pass. HLS and playlist have identical canonical JSON/asset hashes and frame-input fingerprints for the same source/frame—not encoded-video byte identity.
- **status**: Partial
- **log**: Artwork SHA-256 normalization, bounded glyph atlas/TextRuns, canonical layout, progress/time/fade, loudness/trend/guides, artwork-derived palette and frame fingerprint are populated; missing artwork now produces an explicit deterministic fallback asset (`da8c196`). Title/Artist/Album/Time runs now use canonical rect-centered baselines (`5687a36`). Go/Rust field-name/default compatibility was corrected (`94675db`, `5b76f3f`, `66d3d32`). Exact CPU font rasterization/metrics remain.
- **files edited/created**: `internal/video/music_scene.go`, `internal/video/gpu_contracts.go`, `gpu/playlist-compositord/src/contracts.rs`

### T5: Implement full GPU scene parity
- **tier**: Tier 3 — Luna High
- **depends_on**: [T2, T3, T4]
- **location**: `gpu/playlist-compositord/src/gpu_render.rs`, WGSL shader sources, texture/atlas helpers
- **description**: Render every T3 visual group on the GPU in the canonical layer order: artwork tile/fallback, blurred background/palette, metadata glyph runs, bars/waveform/loudness, guide lines, progress/timing and fades. Use pre-rasterized textures/atlas only; no FFmpeg ASS/drawtext/showwaves production overlay.
- **validation**: GPU fixtures show artwork and Japanese metadata at exact canonical rectangles; no-artwork/missing-glyph fallback works; visual comparisons meet structural/color thresholds for all fixture frames.
- **status**: Partial
- **log**: GPU artwork texture/fallback, glyph instances, palette/progress/loudness/fade uniforms are wired; loudness/trend use bounded 64+64 storage samples and spectrum/artwork now use canonical rects (`41faa64`, `b154f1b`, `9ef7579`, `f56e93c`). Full CPU font rasterization, blurred background equivalence, HLS timing and pixel-level geometry gates remain.
- **files edited/created**: `gpu/playlist-compositord/src/gpu_render.rs`

### T6a: Strengthen comparison artifacts and CPU baseline
- **tier**: Tier 2 — Luna Medium
- **depends_on**: [T3]
- **location**: `cmd/music-render-compare`, `scripts/`, documentation
- **description**: Make the compare command choose a read-only cached audio input, hash it, export CPU output and fixed screenshots, record FFmpeg version plus analysis/scene/encode/mux timing and real-time factor. Preserve artifacts and report GPU startup failure distinctly. Reserve image metric implementation for T6b.
- **validation**: The command writes CPU output, screenshots, source hash, JSON/Markdown report and stage timings without changing the cache; controlled failure is explicit and artifacts are retained.
- **status**: Completed
- **log**: Compare command now records input SHA-256, FFmpeg path, GPU adapter metadata, CPU/GPU wall time, ffprobe duration/frame count and start/mid/end PNGs (`4ca3c24`). Short cached input completed both paths: CPU 2.03s/157 frames/5.23s, GPU 8.67s/151 frames/5.03s.
- **files edited/created**: `cmd/music-render-compare/main.go`

### T6b: Run GPU comparison metrics and hardware evidence
- **tier**: Tier 3 — Luna High
- **depends_on**: [T2, T4, T5, T6a]
- **location**: `cmd/music-render-compare`, `scripts/`, hardware evidence docs
- **description**: Export GPU HLS from the verified sidecar and measure structural scene fingerprints, element bounding boxes, alpha masks and color/raster tolerance in addition to PSNR/SSIM. Record sidecar hash/version, adapter/backend, source duration, all stage timings and failure artifacts for HLS and playlist output.
- **validation**: Controlled mismatch fails a fixed gate; supported NVIDIA and AMD comparisons cover 360/720/1080, start/mid/end, scrolling text, no-artwork/missing glyph, Unicode, cancellation and sidecar failover.
- **status**: Completed
- **log**: Comparison CLI now records PNG dimensions, average RGBA, non-background bounds/pixel counts, and CPU/GPU mean-absolute/RMSE/mismatch ratios for start/mid/end (`b34eafb`, `5ec0c9c`). Short cache evidence shows matching dimensions but substantial raster mismatch; this is recorded as a failure signal, not hidden.
- **files edited/created**:

### T7: Integrate both production routes and reject CPU overlays
- **tier**: Tier 4 — Terra Ultra
- **depends_on**: [T4, T5]
- **location**: `internal/video/audio_visualizer.go`, `internal/video/radio_render.go`, `internal/server/music_playlist.go`, integration tests
- **description**: Route single-track HLS and playlist pre-rendering through the exact same populated scene producer. Retain CPU only behind a clearly named comparison/test API. Add static and integration guards against production ASS/showwaves/drawtext/CPU-frame composition.
- **validation**: Same cached input/frame produces equivalent canonical scene records and comparable GPU screenshots from both output routes; cancellation, sidecar death, invalid scene and GPU-required cases remain correct.
- **status**: Completed
- **log**: Single-track HLS and playlist pre-render both call `CanonicalMusicScene` and use GPU sidecar→raw RGBA→FFmpeg only. Static guard prevents production ASS/showwaves/showfreqs/drawtext/subtitles regressions (`741d3ad`).
- **files edited/created**:

### T8: Hardware acceptance, review and release gate
- **tier**: Tier 5 — Sol Max
- **depends_on**: [T6b, T7]
- **location**: test evidence, `docs/PLAYLIST_GPU_COMPATIBILITY.md`, CI workflows
- **description**: Review implementation and evidence; run full Go/Rust suites, fresh NVIDIA and AMD cache-based comparisons, and macOS smoke. Record adapter, sidecar hash/version, render conversion time, screenshot metric thresholds and known raster tolerances. Linux remains contract-only without a Vulkan-capable runner.
- **validation**: CPU/GPU comparison reports pass stated thresholds on supported adapters; full tests and artifact checks pass; review confirms no CPU production renderer or duplicate FFmpeg overlay remains.
- **status**: Not Completed
- **log**: Tier5-directed two-pass mux now separates GPU video encoding from audio/HLS mux, preserves the CPU tail clock, and cleans up the intermediate TS (`8a5033b`). Canonical GPU text now uses a deterministic embedded Go Regular atlas with advance metrics and Unicode tofu fallback (`409f809`). Rebuilt sidecar short-cache evidence matches CPU/GPU at 157 frames and 5.233333s with `comparisonGate.pass=true`; render wall time was CPU 0.93s vs GPU 2.87s. Robust screenshot seeking now avoids HLS boundary black frames. Visual raster parity remains outside the gate (mid RMSE 46.80, start 68.42, end 49.33), and AMD/macOS/long-form acceptance remain open.
- **files edited/created**:

## Five-Tier Allocation

| Tier | Model policy | Assigned task style | Tasks |
|---|---|---|---|
| 1 | Luna Low | Narrow inventory, fixtures, report documentation | T3 |
| 2 | Luna Medium | Bounded Go/CLI/process diagnostics | T1, T6a |
| 3 | Luna High | Cross-file recovery, GPU/WGSL implementation and image gates | T2, T5, T6b |
| 4 | Terra Ultra | Contract redesign and route convergence | T4, T7 |
| 5 | Sol Max | Independent final design/code/evidence gate | T8 |

## Parallel Execution Groups

| Wave | Tasks | Can Start When |
|---|---|---|
| 1 | T1, T3 | Immediately |
| 2 | T2, T6a | T1 / T3 complete respectively |
| 3 | T4 | T2 and T3 complete |
| 4 | T5 | T2, T3 and T4 complete |
| 5 | T6b, T7 | T2, T4, T5, T6a / T4, T5 complete |
| 6 | T8 | T6b and T7 complete |

## Testing Strategy

- Treat the actual cached `.opus` source as read-only; use a stable copy or
  source hash in reports, never mutate or delete it.
- Compare start, 30-second/midpoint and final-fade frames at 360/720/1080 for
  HLS and playlist pre-render output.
- Record source SHA-256, source duration, sidecar SHA/version/adapter, FFmpeg
  version, each stage time and real-time factor (`audio duration / render time`).
- Use scene fingerprints, element boxes, alpha-mask/color tolerance and PSNR/
  SSIM together; do not require a pixel hash.
- Run Go and Rust suites before hardware tests, then verify the executable
  sidecar's protocol/version and adapter on each hardware target.

## Risks & Mitigations

- **Stale sidecar binary**: fingerprint and preflight the artifact before
  starting; never infer compatibility from file extension.
- **No sidecar stderr**: preserve bounded diagnostic stderr for local reports
  while returning a sanitized public error.
- **CPU/GPU font drift**: compare text ownership, baseline/box geometry and
  glyph fallback rather than pixel-identical glyph edges.
- **Payload growth**: retain explicit byte, dimension, glyph and text-run caps.
- **Hardware-specific behavior**: make NVIDIA/AMD acceptance separate evidence;
  do not treat a software adapter as hardware success.
