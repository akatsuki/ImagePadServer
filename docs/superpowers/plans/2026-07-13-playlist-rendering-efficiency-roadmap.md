# Playlist and Music Rendering Efficiency Improvement Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce CPU, memory, process, and transition costs in music-mode and playlist rendering while preserving the current visual output, H.264/AAC delivery contract, and RTSP/HLS compatibility.

**Architecture:** Keep the current pre-rendered `radio-track-<id>.mp4` assets as the source of truth during the first two phases. First make the existing CPU and FFmpeg path measurable, allocation-efficient, and cache-aware; then complete the persistent program-output boundary so track and fallback sources no longer replace the publisher or output clock. A later cross-platform compositor uses a platform-neutral scene contract with a `wgpu` backend and starts with CPU readback before any OS-specific zero-copy encoder bridge.

**Tech Stack:** Go, FFmpeg/ffprobe, MediaMTX, H.264/AAC, MPEG-TS, existing `internal/video` and `internal/obsrtmp`, Rust + `wgpu` only in the gated GPU phase.

## Global Constraints

- The visible visualizer, fallback scene, timing rules, and metadata behavior remain unchanged unless a separate radical-design decision is approved.
- Output remains H.264, `yuv420p`, AAC 48 kHz stereo, no B-frames, with the existing repeat-header/AUD requirements for AVPro compatibility.
- The current copy/remux lane remains available as an explicit compatibility path until the persistent program lane passes all gates.
- MediaMTX, the public RTSP URL, and the HLS path are session-owned resources; overlay or track changes must not restart them.
- A decoder or overlay failure is recoverable through black/silent or generated fallback output; persistent program encoder or publisher failure is session-fatal.
- `ProgramClock` owns output timestamps at 30 fps and 48 kHz audio. Source PTS values are translated at ingestion and never forwarded directly.
- No live waveform rendering is introduced for tracks; pre-rendered MP4 remains the track visual source during this roadmap.
- CPU rendering remains the automatic fallback on systems without a usable GPU backend.
- Every optimization is accepted only with a pixel/golden-output check, monotonic timestamp check, and measured before/after resource data.

## Current Evidence and Boundaries

- Music mode currently performs audio analysis, generates 30 fps RGBA/YUV frames in Go, and invokes FFmpeg for visualizer encoding and HLS/MP4 output (`internal/video/audio_analysis.go`, `internal/video/audio_visualizer.go`).
- `AnalyzeAudioForKind` already carries `AudioFeatures.IntegratedLUFS`, while `extractLUFS` still exists as a separate full-source scan; this is the first safe duplicate-work target.
- Playlist track generation is centered on `RenderRadioTrack` and `RadioProgramEncoderArgs` (`internal/video/radio_render.go`).
- Fallback rendering is already reusable through `RadioFallbackRenderer.RenderReusableRGBA`, but the hot path still draws at 30 fps and feeds a separate FFmpeg process (`internal/video/radio_render.go`, `internal/obsrtmp/radio_fallback.go`).
- Radio session ownership, generation guards, retry behavior, and overlay-source hooks already exist in `internal/obsrtmp/radio.go`; the persistent `ProgramClock`/compositor/program encoder boundary does not yet exist as a complete runtime path.
- Existing plan `docs/superpowers/plans/2026-07-11-playlist-hybrid-overlay.md` defines the detailed overlay rollout contract. This roadmap treats that plan's program-lane tasks as the implementation baseline and adds the missing measurement, cache, and cross-platform GPU work.

## Priority and Gates

| Priority | Phase | Outcome | Gate |
|---|---|---|---|
| P0 | Measurement and identity | Reproducible baseline, metrics, golden frames | No unexplained output drift |
| P1 | Safe CPU/FFmpeg efficiency | Lower allocations, duplicate scans, and startup cost | Same pixels/codec contract; benchmark improvement |
| P1 | Persistent program lane | Stable output clock and publisher across transitions | No reconnect, monotonic PTS, bounded recovery |
| P2 | Cross-platform compositor | Shared scene contract and `wgpu` backend | CPU adapter and GPU backend agree within tolerance |
| P3 | Zero-copy/radical redesign | Evidence-backed decision on encoder bridge or pipeline replacement | Hardware/client matrix proves benefit |

---

### Task 1: Establish a Rendering Baseline and Output-Identity Gate

**Files:**
- Create: `internal/video/render_metrics.go`
- Create: `internal/video/render_metrics_test.go`
- Modify: `internal/video/audio_visualizer_test.go`
- Modify: `internal/video/radio_render_test.go`
- Modify: `internal/server/music_radio_e2e_test.go`
- Create: `docs/PLAYLIST_RENDERING_BASELINE.md`

**Interfaces:**
- `RenderMetrics` records `analysisDuration`, `renderDuration`, `encodeDuration`, `frames`, `bytesWritten`, `allocsPerFrame`, `queueDepth`, `droppedFrames`, and end-to-end PTS delta.
- `RenderMetricsCollector.BeginJob(jobID string)`, `ObserveStage(name string, elapsed time.Duration)`, `ObserveFrame(bytes int, elapsed time.Duration)`, `ObserveQueue(depth int)`, and `Snapshot() RenderMetrics` are process-local and testable without Prometheus.
- `WriteVisualizerRGBAFrames` and `RadioFallbackRenderer.RenderReusableRGBA` accept an optional collector without changing their existing output when the collector is nil.

- [ ] Write a failing test that renders a fixed 2-second fixture at 540p and requires non-zero analysis, render, encode, frame, byte, and allocation measurements.
- [ ] Write a failing golden test that hashes fixed-time PNG frames and verifies `RadioFallbackRenderer` output is identical before and after metrics instrumentation.
- [ ] Run `go test ./internal/video ./internal/server -run 'Test(RenderMetrics|Visualizer.*Golden|Radio.*Golden)' -count=1` and confirm failure because the collector and golden harness do not exist.
- [ ] Implement the collector with monotonic `time.Since` measurements and no per-frame map allocation; use fixed stage counters and a bounded histogram array.
- [ ] Add a 30-second radio E2E assertion that records public RTSP path, reconnect count, non-monotonic DTS warnings, and audio/video PTS drift.
- [ ] Run the focused tests plus `go test ./internal/server -run TestMusicRadio -count=1` and record the baseline hardware, encoder, preset, resolution, and fixture in `docs/PLAYLIST_RENDERING_BASELINE.md`.

**Acceptance:** The repository has a repeatable baseline for CPU time, allocations, bytes, PTS drift, and visual hashes before any optimization is enabled.

### Task 2: Remove Safe Duplicate Work and Per-Frame Allocations

**Files:**
- Modify: `internal/video/audio_analysis.go`
- Modify: `internal/video/audio_visualizer.go`
- Modify: `internal/video/audio_types.go`
- Modify: `internal/video/radio_render.go`
- Modify: `internal/video/audio_analysis_test.go`
- Modify: `internal/video/audio_visualizer_test.go`
- Modify: `internal/video/radio_render_test.go`

**Interfaces:**
- `AudioAnalysis.Features.IntegratedLUFS` is the single LUFS result used by both visualizer and playlist rendering.
- `writeVisualizerFrames` reuses a bounded in-flight buffer pool sized to the configured worker count; its output bytes and frame order remain unchanged.
- `RadioFallbackRenderer` reuses its existing frame buffer and level-map buffers for `RenderReusableRGB`/`RenderReusableRGBA`.

- [ ] Add a fake-FFmpeg test that counts source reads and fails when one music job performs a second full-source LUFS scan after `AnalyzeAudioForKind` already produced `IntegratedLUFS`.
- [ ] Add allocation tests around `writeVisualizerFrames` and `RenderReusableRGBA` that require the post-change allocation count to be lower than the baseline captured in Task 1.
- [ ] Run `go test ./internal/video -run 'Test(AnalyzeAudio|Visualizer|RadioFallback)' -count=1` and confirm the new tests fail.
- [ ] Refactor `AnalyzeAudioForKind` to retain the measured LUFS in `AudioAnalysis.Features.IntegratedLUFS`; remove the unconditional `extractLUFS` second pass and keep a guarded fallback only for analyses that lack a valid LUFS value.
- [ ] Allocate the frame pool once per render job, return buffers after FFmpeg writes complete, and preserve cancellation behavior when a worker exits early.
- [ ] Move fallback wave-level maps and premultiplied static-layer buffers into renderer-owned reusable storage; do not regenerate them for every frame.
- [ ] Re-run the focused tests and compare `go test -bench 'Benchmark(WriteVisualizer|RadioFallbackRenderer)' -benchmem ./internal/video` with the Task 1 baseline.

**Acceptance:** Duplicate LUFS reads are absent, per-frame allocations decrease, and fixed-time PNG hashes remain identical.

### Task 3: Add Analysis and Render-Recipe Caches

**Files:**
- Create: `internal/video/render_cache.go`
- Create: `internal/video/render_cache_test.go`
- Modify: `internal/video/audio_analysis.go`
- Modify: `internal/video/radio_render.go`
- Modify: `internal/server/music_playlist.go`
- Modify: `internal/server/music_playlist_test.go`
- Create: `docs/PLAYLIST_RENDER_CACHE.md`

**Interfaces:**
- `AnalysisCacheKey{SourceSHA256, AnalyzerVersion, LoudnormFilter, FPS}` identifies deterministic analysis results.
- `RenderRecipeKey{SourceSHA256, ArtworkSHA256, MetadataDigest, QualityPreset, EncoderName, RendererVersion}` identifies deterministic track output.
- `AnalysisCache.Load(key) (AudioAnalysis, bool)`, `AnalysisCache.Store(key, AudioAnalysis) error`, `RenderCache.Load(key) (path string, ok bool)`, and `RenderCache.Store(key, path string) error` use atomic writes and bounded size/age eviction.

- [ ] Write failing tests for cache hits, analyzer-version misses, artwork/metadata invalidation, corrupt-entry deletion, and concurrent readers.
- [ ] Run `go test ./internal/video -run Test(Render|Analysis)Cache -count=1` and confirm failure.
- [ ] Implement SHA-256 keys from the source/artwork bytes plus explicit version strings; never key by path or mtime alone.
- [ ] Write cache entries to a temporary sibling file, `fsync` the file, atomically rename it, and validate the stored digest before returning a hit.
- [ ] Place analysis cache lookup before FFT/LUFS work and render cache lookup before `RenderRadioTrack`; preserve progress callbacks by reporting a completed cached job.
- [ ] Add playlist tests proving a repeated publish reuses the track asset while a changed encoder, artwork, metadata, or renderer version forces regeneration.
- [ ] Document cache location, eviction policy, invalidation fields, and recovery behavior in `docs/PLAYLIST_RENDER_CACHE.md`.

**Acceptance:** Re-publishing the same recipe avoids analysis and render work without serving stale or partially written media.

### Task 4: Complete the Persistent Program Clock, Compositor, and Encoder Boundary

**Files:**
- Create: `internal/obsrtmp/radio_program.go`
- Create: `internal/obsrtmp/radio_program_test.go`
- Create: `internal/obsrtmp/radio_program_feeder.go`
- Create: `internal/obsrtmp/radio_program_feeder_test.go`
- Modify: `internal/obsrtmp/radio.go`
- Modify: `internal/video/radio_render.go`
- Modify: `internal/video/video_encoder.go`
- Modify: `internal/video/radio_render_test.go`

**Interfaces:**
- `ProgramClock` emits 30 fps video PTS, 48 kHz audio PTS, and black/silent fallback during source gaps.
- `ProgramEncoder` exposes `Start(ctx, preset)`, `WriteVideoRGBA(frame []byte, pts time.Duration)`, `WriteAudioPCM(samples []byte, pts time.Duration)`, `Output() io.Reader`, `Healthy() error`, and `Close()`.
- `ProgramTrackFeeder.Run(ctx, mediaPath string, startSeconds int, encoder ProgramEncoder, overlaySource func() OverlaySnapshot) error` decodes existing pre-rendered MP4 and translates source frames onto the program clock.
- `RadioManager` owns either `compatibility-copy` or `program` for a session and never swaps packet producers on the same publisher sink.

- [ ] Add failing encoder-argument tests requiring `raw RGBA` input, `yuv420p`, AAC 48 kHz stereo, `-bf 0`, repeat headers/AUD, and the selected latency GOP policy.
- [ ] Add failing clock tests for strictly monotonic video/audio PTS, 48 kHz sample rollover, and black/silent output during a 500 ms decoder gap.
- [ ] Add failing recovery tests proving decoder/overlay failure preserves publisher identity, RTSP URL, and MediaMTX path, while encoder death becomes a terminal session error.
- [ ] Run `go test ./internal/obsrtmp ./internal/video -run 'Test(RadioProgram|ProgramTrackFeeder|RadioOverlay|RadioRTSP)' -count=1` and confirm failure.
- [ ] Implement the bounded program channels: obsolete video frames may be dropped, audio is never dropped, and a full audio queue is a health failure.
- [ ] Decode track MP4 into canonical `RGBA`, 30 fps, fixed resolution, and 48 kHz stereo PCM; never forward decoder PTS directly.
- [ ] Keep `ProgramClock` running while a child decoder is replaced, emitting black/silence until the next aligned frame/audio pair arrives.
- [ ] Add FFmpeg smoke coverage that runs 360p for five seconds, then uses ffprobe to require H.264/AAC and monotonic packet timestamps.
- [ ] Re-run focused tests and the existing copy-lane compatibility test without changing its expected `-c copy` arguments.

**Acceptance:** Track and fallback transitions do not reconnect receivers, reset output timestamps, or replace the public publisher path.

### Task 5: Add Cross-Platform Scene and Compositor Contracts

**Files:**
- Create: `internal/video/compositor_contract.go`
- Create: `internal/video/compositor_contract_test.go`
- Create: `native/playlist-compositor/Cargo.toml`
- Create: `native/playlist-compositor/src/lib.rs`
- Create: `native/playlist-compositor/src/scene.rs`
- Create: `native/playlist-compositor/src/cpu.rs`
- Create: `native/playlist-compositor/src/wgpu_backend.rs`
- Create: `native/playlist-compositor/include/playlist_compositor.h`
- Create: `internal/video/compositor_bridge.go`
- Create: `internal/video/compositor_bridge_test.go`
- Create: `docs/PLAYLIST_GPU_COMPATIBILITY.md`

**Interfaces:**
- Go defines `SceneSnapshot{ClockPTS, Width, Height, Background, TrackTexture, OverlayCommands, AccentColor, Progress}` and `OverlayCommand{Kind, Rect, TextureID, Color, Text}`; it contains no Metal, D3D, Vulkan, or `image.RGBA` handles.
- The C ABI exports `pc_create`, `pc_resize`, `pc_render_rgba`, `pc_capabilities`, and `pc_destroy`; all pointers have explicit ownership and length rules.
- Rust `wgpu` maps the same scene to Metal on macOS, D3D12 on Windows, and Vulkan on Linux; the first production milestone returns CPU-readable RGBA for the existing FFmpeg pipe.

- [ ] Add failing Go contract tests for deterministic scene serialization, rejected dimensions, bounded text/artwork payloads, and unchanged CPU-adapter hashes.
- [ ] Add failing Rust tests for the same scene snapshot producing the same command order on all backends.
- [ ] Run `go test ./internal/video -run 'Test(Compositor|Scene)' -count=1` and `cargo test --manifest-path native/playlist-compositor/Cargo.toml`; confirm failure because the contract and crate are absent.
- [ ] Implement the Go CPU adapter first and route the existing renderer through the contract without enabling `wgpu`; this proves the boundary before GPU debugging begins.
- [ ] Implement the Rust scene model and a `wgpu` render pass for background, artwork, spectrum/wave overlays, progress, and solid text primitives; keep ASS/libass text in the existing path until pixel comparison passes.
- [ ] Build platform capability reporting and select `wgpu` only when adapter creation, required texture formats, and readback succeed; otherwise return to the CPU adapter.
- [ ] Add a fixed-fixture comparison that requires exact CPU output for static layers and a documented per-channel tolerance for GPU output caused by shader/format rounding.
- [ ] Record supported OS, backend, adapter, texture format, readback latency, and failure reason in `docs/PLAYLIST_GPU_COMPATIBILITY.md`.

**Acceptance:** One scene contract drives CPU and `wgpu` implementations, and unsupported adapters fail closed to the known CPU path.

### Task 6: Measure and Gate GPU-to-Encoder Handoffs

**Files:**
- Create: `internal/video/hardware_surface.go`
- Create: `internal/video/hardware_surface_test.go`
- Modify: `internal/video/video_encoder.go`
- Modify: `native/playlist-compositor/src/wgpu_backend.rs`
- Create: `docs/PLAYLIST_ZERO_COPY_SPIKE.md`

**Interfaces:**
- `HardwareSurfaceBridge` reports `CanImport(backend, encoder string) bool`, `Import(surface SurfaceHandle) error`, `Encode(surface SurfaceHandle, pts time.Duration) error`, and `Close() error`.
- A bridge is opt-in per machine and encoder; the default remains `wgpu readback -> existing FFmpeg input`.

- [ ] Add failing capability tests that reject a bridge when adapter, texture format, encoder, or synchronization primitive is unavailable.
- [ ] Run `go test ./internal/video -run TestHardwareSurfaceBridge -count=1` and confirm failure.
- [ ] Prototype Windows D3D11 shared texture import for NVENC/QSV/AMF, macOS Metal texture import for VideoToolbox, and Linux Vulkan export/import for VAAPI/NVENC behind separate build tags; do not change the default path.
- [ ] Measure GPU render time, readback time, encoder queue depth, PCIe transfer bytes, CPU utilization, and end-to-end PTS drift for 540p, 720p, and 1080p.
- [ ] Enable a bridge only when it beats the readback path at p95 render-plus-encode time and passes the same visual/codec/receiver gates as Task 4.
- [ ] Record unsupported combinations and measured regressions in `docs/PLAYLIST_ZERO_COPY_SPIKE.md`; leave unsupported OS/driver combinations on readback.

**Acceptance:** Zero-copy is an evidence-backed opt-in optimization, never a prerequisite for playlist playback.

### Task 7: Run Compatibility, Performance, and Rollout Verification

**Files:**
- Modify: `internal/server/music_radio_e2e_test.go`
- Modify: `internal/server/music_mode_test.go`
- Modify: `internal/obsrtmp/radio_test.go`
- Modify: `internal/video/radio_render_test.go`
- Create: `docs/PLAYLIST_RENDERING_SMOKE.md`

- [ ] Add E2E coverage for empty overlay, visible notification overlay, track transition, decoder failure, encoder failure, HLS readiness, and stable public RTSP identity.
- [ ] Run `go test ./internal/video ./internal/obsrtmp ./internal/server -count=1` and require all packages to pass.
- [ ] Run a 30-minute 720p/30fps soak with at least three track transitions, capturing FFmpeg stderr, MediaMTX logs, CPU/GPU utilization, queue depth, drops, and receiver-side observations.
- [ ] Require no RTSP reconnect, no non-monotonic DTS warning, no audio gap over 100 ms, no stale cache hit, and p95 render time under the 33.3 ms frame budget.
- [ ] Run the same smoke matrix with GPU disabled, hardware encoder forced, and CPU encoder forced; record all results in `docs/PLAYLIST_RENDERING_SMOKE.md`.
- [ ] Keep overlay default `off` until Tasks 1-6 pass on the supported OS/encoder matrix; enable notifications only through an explicit persisted setting.

## Rollout Order

1. Land Task 1 and publish the baseline before changing the hot path.
2. Land Tasks 2-3 with the current CPU renderer and copy/remux delivery unchanged.
3. Land Task 4 behind an encoder preflight and explicit session-start mode selection.
4. Run Task 7 with program mode disabled by default; enable it only on machines that pass the smoke matrix.
5. Land Task 5 as a CPU-adapter-backed `wgpu` prototype; compare output and resource data before changing the default renderer.
6. Run Task 6 as a hardware-specific spike; promote only measured wins.

## Radical Options Kept as Separate Decisions

These are not mixed into the safe rollout because each changes product or compatibility boundaries:

- **Single long-lived FFmpeg filtergraph:** move decode, overlay, audio mix, and encode into one process; choose only if Go compositor maintenance or process boundaries dominate the measured cost.
- **Direct libav integration:** remove rawvideo/MPEG-TS pipes and call libavcodec/libavfilter in one native service; choose only with a dedicated packaging and crash-isolation plan.
- **On-demand live rendering:** remove full-track pre-rendering and keep only analysis plus a few seconds of lookahead; choose only after cache hit rate and startup latency data show pre-rendering is the dominant cost.
- **Static background plus dynamic overlay streams:** change the delivery protocol and receiver assumptions; choose only if the supported AVPro/RTSP/HLS matrix accepts a layered representation.
- **WebRTC or CMAF as the primary delivery surface:** treat RTSP/HLS as compatibility outputs; choose only as a product-level latency and compatibility decision, not as a renderer micro-optimization.

## Plan Review Checklist

- [ ] Every phase has a named file boundary, test command, and acceptance gate.
- [ ] No task requires a future unnamed interface; new interfaces are listed in the task that produces them.
- [ ] The copy lane remains testable while the program lane is introduced.
- [ ] CPU fallback remains available on every supported OS.
- [ ] Pixel identity, codec contract, timestamp monotonicity, receiver compatibility, and resource metrics are all explicit release gates.

## Source Threads

- `codex://threads/019f4fb8-929e-7ad2-8f46-933a07a2187d` — music-mode generation pipeline, 12 safe optimizations, 6 radical redesigns, and `wgpu` direction.
- `codex://threads/019f4fb2-54ab-7442-b5c3-bc2e4fcfb2ce` — playlist fallback renderer, hybrid implementation status, persistent program boundary, and cross-platform compositor strategy.
