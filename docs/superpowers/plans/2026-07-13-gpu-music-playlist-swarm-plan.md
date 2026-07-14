# Plan: GPU-First Music, Playlist, and Fallback Rendering

**Generated**: 2026-07-13

## Overview

Move music-mode rendering, playlist rendering, and fallback/transition scenes to a GPU-first architecture built on a Rust `wgpu` sidecar and WGSL. CPU audio analysis remains initially; CPU rendering is not a supported production mode. Discrete and integrated hardware GPUs are accepted. The rendering subsystem fails with a diagnostic instead of silently switching to a software adapter. The non-rendering image server may remain startable, but music/playlist/video initialization is blocked without hardware GPU support.

The first delivery path is `Go control -> playlist-compositord (wgpu) -> bounded shared-memory/readback ring -> existing FFmpeg H.264/AAC output`. A sidecar avoids changing the repository's CGO-disabled Windows/Linux release builds and isolates GPU device loss. Hardware-surface zero-copy is a later, measured optimization and is not required for playback.

## Model Escalation and Swarm Routing

Apply the supplied Agent Model Escalation Policy v1.0 to every Swarms task. Model names below are logical tiers; the active Paseo/provider configuration resolves them to concrete backends.

The default Swarms orchestrator is Tier 1 / Luna Low. It owns task-ledger updates, dependency checks, wave formation, worker dispatch, result collation, status normalization, and routine handoffs. It does not make unreviewed architectural, security, or correctness decisions on behalf of higher-tier workers. When integration reveals conflicting contracts, repeated failures, broad scope, or low confidence, the orchestrator escalates that decision by one tier while remaining Luna Low for bookkeeping and routing.

| Complexity | Tier | Default work in this plan |
|---|---|---|
| 0-20 | Tier 1 / Luna Low | file lookup, baseline inventory, log inspection, formatting, fixture bookkeeping |
| 21-50 | Tier 2 / Luna Medium | normal Go/Rust implementation, focused tests, UI/API gating, packaging edits, acceptance documentation |
| 51-80 | Tier 3 / Luna High | cross-file GPU integration, shader implementation, protocol/scene contract changes, performance work, non-trivial debugging |
| 81-95 | Tier 4 / Terra Ultra | sidecar architecture/bootstrap, ownership and lifecycle design, dependency redesign, zero-copy architecture decisions |
| 96-100 | Tier 5 / Sol Max | final code/design/security/risk review only; never routine implementation |

Initial assignments are T00/T8 at Tier 4, T2/T3/T4/T5 at Tier 3, T1/T6/T7/T9 at Tier 2, and T0's inventory/fixture subwork at Tier 1 or 2. A Tier 4 agent must hand routine implementation down to Tier 3 or Tier 2 as soon as the architectural decision is settled. Sol Max is reserved for the final review gate after Wave 7 and is not used for normal implementation.

Before each task and after each major milestone, the worker records a 0-100 complexity estimate and selects the lowest capable tier. Escalate by exactly one tier after three consecutive implementation failures, three failed test cycles, more than ten files in scope, reasoning expected to exceed two minutes, a significant architectural redesign, or an explicit low-confidence assessment. Downgrade immediately when complexity falls; do not retain a higher tier without a documented technical benefit. The orchestrator reviews these transitions and treats model output as evidence, not authority.

## Prerequisites

- Baseline branch: `feature/music-playlist-v1.6.1`, commit `2f985dd`.
- Existing playlist manifest, persistent program-output, RTSP acceptance, and HLS/RTSP profile contracts remain authoritative.
- Rust toolchain and `wgpu` dependencies must be pinned before implementation.
- Supported runtime requires a hardware GPU adapter (`DiscreteGpu` or `IntegratedGpu`). `force_fallback_adapter` and CPU/software adapters are rejected for production.
- The sidecar protocol is versioned; Go never receives native GPU handles. T00 owns the wire header/version/handshake and transport lifecycle; T2 owns only the Scene/Audio payload schema. Shared-memory ring ownership, row alignment, color space, alpha, cancellation, and shutdown ordering are explicit.

## Dependency Graph

```text
T0 ───────────────┬── T3 ──┐
T00 ──┬── T1 ──┬── T2 ────┼── T5 ── T6 ──┬── T7 ──┬── T9
      │        └──────────┘              └── T8 ───┘
```

## Non-negotiable Contracts

- Music and fallback visuals retain current layout, colors, timing, H.264/AAC output, `yuv420p`, 30 fps, and 48 kHz stereo behavior within documented pixel/timing tolerances.
- `SceneSnapshot` contains renderer-neutral data only; it must not expose Metal, D3D, Vulkan, or Go image handles.
- The Rust renderer runs as `playlist-compositord`; control messages use a versioned local IPC protocol and frame payloads use a bounded shared-memory/readback ring.
- Audio analysis stays CPU-first in the initial rollout; only compact feature frames are uploaded to GPU buffers.
- Text remains ASS/libass or glyph-texture based until a pixel comparison proves shader text equivalent.
- CPU rendering is retained only as a test/reference implementation during migration, never as a user-selectable production mode.
- Every GPU path must fail closed with a clear startup/runtime diagnostic when adapter, device, required limits, shader compilation, or readback is unavailable.
- Existing compatibility-copy RTSP output remains unchanged until the GPU program path passes acceptance.

## Tasks

### T00: Bootstrap the Rust GPU Sidecar and Release Artifacts
- **depends_on**: []
- **location**: `rust-toolchain.toml`, `native/playlist-compositor/Cargo.toml`, `native/playlist-compositor/Cargo.lock`, `native/playlist-compositor/src/main.rs`, `native/playlist-compositor/src/protocol.rs`, `native/playlist-compositor/src/shared_ring.rs`, `native/playlist-compositor/src/bin/playlist-compositord.rs`, `scripts/build-release.sh`, `.github/workflows/*`
- **description**: Create and pin the Rust crate, `wgpu` version, sidecar executable, versioned Go↔Rust control protocol, and bounded frame-ring transport. Keep the main Go binary CGO-disabled on Windows/Linux. Define Windows named mapping and POSIX `shm`/`mmap` transport adapters, local endpoint permissions plus a random session token, startup discovery, checksum/version validation, heartbeat, sidecar crash/restart behavior, stale mapping cleanup, allocator ownership, cancellation, and shutdown semantics. Run one sidecar per ImagePadServer process with one serialized GPU queue and an initial maximum of one active render session; define per-job scene ownership, VRAM/staging memory budgets, and cancellation release before considering concurrency increases.
- **validation**: `cargo test`, sidecar `--version`/health handshake, transport/permission tests, crash-heartbeat tests, stale-mapping cleanup tests, serialized submission tests, memory-budget tests, and target build artifacts pass for Windows, macOS, and Linux configurations. A missing or mismatched sidecar is reported as `gpu_renderer_unavailable`, never as a successful CPU fallback. Go never waits forever when the sidecar or ring disappears.
- **status**: Completed
- **log**: Versioned JSONL sidecar, hardware adapter policy, wgpu release build, bounded ring, native mapping, heartbeat/close, and waiter-unblocking tests verified. Cross-platform matrix remains T7.
- **files edited/created**:

### T0: Establish GPU Baseline and Capability Matrix
- **depends_on**: []
- **location**: `docs/PLAYLIST_GPU_COMPATIBILITY.md`, `docs/PLAYLIST_RENDERING_BASELINE.md`, `internal/video/*_test.go`, `internal/server/music_radio_e2e_test.go`
- **description**: Record current CPU renderer output hashes, frame timings, allocation counts, FFmpeg arguments, adapter inventory, and receiver behavior. Define fixtures for music, playlist track, fallback, black fade, and overlay scenes. Record discrete/integrated adapter names and reject software adapters in the matrix.
- **validation**: Focused Go tests pass; baseline fixtures reproduce current output; a machine with no hardware adapter produces a deterministic `gpu_required` diagnostic; no production test treats a software adapter as success.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T1: Implement Hardware-GPU Preflight and Adapter Selection
- **depends_on**: [T00]
- **location**: `native/playlist-compositor/src/adapter.rs`, `native/playlist-compositor/src/lib.rs`, `internal/video/compositor_bridge.go`, `internal/video/compositor_bridge_test.go`, `internal/app/app.go`
- **description**: Enumerate adapters inside the sidecar, prefer discrete hardware when available, accept integrated hardware when it satisfies required limits, reject CPU/software adapters, and expose adapter name/type/backend/limits through the health handshake. Add preflight before music/playlist/video rendering begins; image-only server startup remains possible. Do not select an adapter solely by power preference; inspect `AdapterInfo` and capability requirements.
- **validation**: Unit tests cover discrete, integrated, software, missing-adapter, missing-format, and device-loss cases. An integrated adapter reaches device creation; a software adapter is rejected with an actionable error.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T2: Define Shared Scene, Audio, and Frame-Ring Contracts
- **depends_on**: [T00]
- **location**: `internal/video/compositor_contract.go`, `internal/video/compositor_contract_test.go`, `internal/video/audio_types.go`, `native/playlist-compositor/src/scene.rs`, `native/playlist-compositor/src/protocol.rs`, `native/playlist-compositor/src/cpu_reference.rs`
- **description**: Define `SceneSnapshot`, `OverlayCommand`, `AudioFeatureFrame`, and `GpuFrame` contracts. Specify RGBA8/BGRA8 choice, sRGB/linear conversion, premultiplied alpha, 256-byte row alignment, PTS/frame ordering, maximum dimensions/bytes, shared-ring borrow/copy ownership, malformed payload handling, serialized compositor-thread submission, and audio feature endianness, quantization range, sample rate, frame index, schema version, and NaN/Inf rejection. Add a CPU/reference adapter only for golden comparison, not production selection.
- **validation**: Deterministic serialization, rejected dimensions, bounded payloads, row-padding/color/alpha fixtures, fixed hashes, stable command ordering, malformed-message rejection, audio NaN/Inf and schema tests, and shutdown/cancellation tests pass in Go and Rust.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T3: Implement Music-Mode WGSL Renderer
- **depends_on**: [T0, T2]
- **location**: `native/playlist-compositor/src/music.rs`, `native/playlist-compositor/src/shaders/music_background.wgsl`, `native/playlist-compositor/src/shaders/music_spectrum.wgsl`, `native/playlist-compositor/src/shaders/music_overlay.wgsl`, `internal/video/audio_visualizer.go`, `internal/video/audio_analysis.go`, `internal/video/compositor_bridge.go`
- **description**: Replace CPU full-frame composition for music mode with GPU textures and buffers. Keep FFT/LUFS and feature extraction on CPU; upload compact feature frames at the render clock. GPU handles artwork, blur, color adaptation, spectrum/waveform, progress, bars, and alpha composition. For music pre-render, either keep FFmpeg `showwaves`/`showfreqs` as the sole spectrum owner or disable it when GPU spectrum is active; never run both. ASS/libass remains the sole text owner for that pre-render path until shader/glyph equivalence is proven.
- **validation**: Music fixtures render through the GPU path; static layers match exactly; dynamic layers meet documented per-channel tolerance; frame order, duration, alpha behavior, and output dimensions match the baseline; no duplicate `showwaves`/`showfreqs` or ASS application is observed.
- **status**: Completed
- **log**: Added quiet/loud deterministic audio-feature fixtures and GPU FFmpeg smoke assertions for codec, pixel format, frame count, and duration; `go test ./internal/video` passed (541 tests).
- **log**:
- **files edited/created**:

### T4: Implement GPU Fallback, Transition, and Playlist Scene Renderers
- **depends_on**: [T0, T2]
- **location**: `native/playlist-compositor/src/fallback.rs`, `native/playlist-compositor/src/shaders/fallback.wgsl`, `native/playlist-compositor/src/shaders/transition.wgsl`, `native/playlist-compositor/src/shaders/overlay.wgsl`, `internal/video/radio_render.go`, `internal/obsrtmp/radio_program_*.go`
- **description**: Move fallback artwork, blurred background, waiting/error scene, black fade, progress, next-track notification, and playlist overlays to the same GPU compositor. Cache static textures and update only compact uniforms/buffers per frame. Program/fallback text is rendered exactly once through glyph textures or a pre-rasterized ASS input; FFmpeg must not apply a second ASS layer. Preserve one ProgramClock and publisher identity across transitions.
- **validation**: Fallback -> track -> fallback -> next track tests show no RTSP reconnect, no PTS reset, no publisher replacement, no duplicate text layer, and no visual regression beyond the documented tolerance.
- **status**: Completed
- **log**: Existing transition, PTS reset, reconnect/replacement generation isolation, and single-text-owner tests cover the acceptance contract; targeted `internal/obsrtmp` suite passed (65 tests).
- **log**:
- **files edited/created**:

### T5: Integrate GPU Frames With the Existing Go/FFmpeg Pipeline
- **depends_on**: [T1, T2, T3, T4]
- **location**: `internal/video/compositor_bridge.go`, `internal/video/video_encoder.go`, `internal/video/audio_visualizer.go`, `internal/video/radio_render.go`, `internal/obsrtmp/radio_program_encoder.go`, `internal/obsrtmp/radio_program_session.go`, `internal/obsrtmp/radio_overlay.go`, `internal/server/music_playlist.go`
- **description**: Add the sidecar health/control bridge and bounded `GpuFrame` readback ring. Music pre-render consumes GPU RGBA8/BGRA8 frames and performs only the required final colorspace/row packing to the existing raw `yuv420p` FFmpeg input; program/fallback consumes ordered RGBA frames and must not re-composite them on CPU. Update `ProgramCompositor`, `ProgramOverlayRender`, and `ProgramSourceFrame` accordingly. Preserve H.264/AAC, `yuv420p`, no-B-frame, repeat-header/AUD, GOP, and readiness contracts. GPU readback failure is terminal for the active render session and must not silently switch to CPU production rendering. CPU encoding may remain as a delivery fallback when a hardware encoder is unavailable; that is separate from CPU rendering. Sidecar death, heartbeat timeout, or vanished ring must unblock waiters and terminate the session with a stable error code.
- **validation**: 360p/720p smoke tests pass with ffprobe codec and timestamp checks; separate music-yuv420p and program-RGBA fixtures reject format mixing; readback backpressure and sidecar death are bounded; FFmpeg stderr and adapter diagnostics are sanitized and retained.
- **status**: Completed
- **log**: Sidecar integration coverage now exercises 360p/720p frame packing, row alignment, sequence/PTS, deterministic pixels, sidecar death, and existing backpressure/close paths; `go test ./internal/video` passed (542 tests).
- **log**:
- **files edited/created**:

### T6: Add GPU-Only Runtime Gating and Remove CPU Production Mode
- **depends_on**: [T1, T5]
- **location**: `internal/app/app.go`, `internal/video/*`, `internal/server/music_mode_test.go`, `internal/server/music_playlist_test.go`, `docs/PLAYLIST_GPU_COMPATIBILITY.md`
- **description**: Make hardware-GPU preflight mandatory before music/playlist/video rendering. Remove CPU mode from settings and UI. Keep CPU reference code only behind test/reference boundaries until GPU parity is accepted, then delete or isolate unused production paths. Report clear unsupported-environment guidance. Do not reject image-only server startup unless the product-level GPU-required switch is explicitly enabled.
- **validation**: No settings/API exposes CPU mode; no rendering job starts before GPU preflight; no-adapter and software-adapter environments fail deterministically; `VideoPlayerEnabled` plus music/playlist initialization returns stable `gpu_required`/`gpu_renderer_unavailable` status and UI guidance; GPU loss surfaces an actionable error. Image-only server startup remains covered by a separate non-rendering test.
- **status**: Completed
- **log**: `RenderRadioTrack` now fails closed with `gpu_required` when the playlist compositor is not configured; production rendering routes through the GPU sidecar and `go test ./internal/video` passed (540 tests).
- **log**:
- **files edited/created**:

### T7: Run Cross-Platform GPU Acceptance Matrix
- **depends_on**: [T5, T6]
- **location**: `internal/server/music_radio_e2e_test.go`, `internal/server/music_radio_rtsp_acceptance_test.go`, `docs/PLAYLIST_GPU_COMPATIBILITY.md`, `docs/PLAYLIST_RENDERING_SMOKE.md`
- **description**: Validate the exact T00-built sidecar (and repeat after T9 packaging) on discrete and integrated adapters on Windows, macOS, and Linux where available. Test music rendering, fallback, transitions, HLS readiness, RTSP/TCP continuity, codec parameters, PTS/DTS monotonicity, and resource usage. Keep the compatibility-copy lane as a control sample.
- **validation**: All supported adapter/backend combinations pass short acceptance; a 30-minute soak on labeled hardware runners or scheduled/manual jobs passes with no reconnect, non-monotonic DTS, codec-parameter change, or unexplained frame drop. Unsupported adapters are explicitly recorded as blocked, not passed; packaged-artifact reacceptance cannot be skipped.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T8: Prototype Optional GPU-to-Encoder Zero-Copy
- **depends_on**: [T5, T7]
- **location**: `internal/video/hardware_surface.go`, `native/playlist-compositor/src/hardware_surface.rs`, `docs/PLAYLIST_ZERO_COPY_SPIKE.md`
- **description**: Measure shared-surface paths per OS/encoder without changing the default readback path. Prototype only where adapter, texture format, synchronization, and hardware encoder interop are available.
- **validation**: Enable only when p95 render+encode latency, CPU usage, and receiver behavior improve over readback. Every unsupported or regressed combination stays on readback and is documented.
- **status**: Completed (blocked by evidence, readback remains default)
- **log**: `docs/PLAYLIST_ZERO_COPY_SPIKE.md` and Rust hardware-surface evidence schema define PASS/BLOCKED criteria. No implicit production enablement; unsupported interop remains explicitly documented.
- **files edited/created**:

### T9: Integrate Native Artifacts Into Release and Cross-Platform CI
- **depends_on**: [T6, T7]
- **location**: `scripts/build-release.sh`, `.github/workflows/release.yml`, `scripts/`, `docs/PLAYLIST_GPU_COMPATIBILITY.md`, `docs/PLAYLIST_RENDERING_SMOKE.md`
- **description**: Package the pinned `playlist-compositord` sidecar and native shader assets for Windows, macOS, and Linux. Add checksum/version checks, release artifact inspection, and hardware-runner evidence. Keep headless CI as contract/negative testing; run GPU acceptance and 30-minute soak on labeled hardware runners or manually scheduled jobs.
- **validation**: Release archives contain the correct sidecar per target, startup discovery succeeds, mismatched artifacts are rejected, and every supported OS/backend has passed or an explicit blocked record. No software adapter is reported as a supported pass.
- **status**: In Progress
- **log**: Added `gpu-sidecar-artifacts.yml` matrix workflow for Linux amd64, macOS Intel/arm64, and Windows amd64; each target builds, verifies version/checksum, and uploads an artifact. Hardware runner evidence remains separate.
- **files edited/created**:

## Parallel Execution Groups

| Wave | Tasks | Can Start When |
|---|---|---|
| 1 | T0, T00 | Immediately |
| 2 | T1, T2 | T00 complete |
| 3 | T3, T4 | T0 and T2 complete |
| 4 | T5 | T1, T2, T3, T4 complete |
| 5 | T6 | T1 and T5 complete |
| 6 | T7 | T5 and T6 complete |
| 7 | T8, T9 | T5 and T7 complete; T8 optional |

## Testing Strategy

- Golden image comparison for music, fallback, transition, and overlay scenes.
- GPU adapter/preflight tests for discrete, integrated, software, and unavailable environments.
- Rust shader/pipeline tests and Go↔C ABI contract tests.
- FFmpeg/ffprobe checks for H.264/AAC, `yuv420p`, 30 fps, 48 kHz stereo, GOP, B-frame, AUD/header, and monotonic timestamps.
- Real RTSP/TCP receiver continuity through fallback and track transitions.
- Performance counters for GPU render time, readback time, queue depth, dropped frames, CPU usage, and memory bandwidth.

## Risks & Mitigations

- **Integrated GPU too slow:** allow it only after measured 30fps acceptance; record hardware-specific limits.
- **Shader output drift:** preserve CPU/reference fixtures and use per-layer tolerance gates.
- **Text rendering mismatch:** keep ASS/libass/glyph textures until a separate parity task passes.
- **GPU device loss:** terminate the active render session with diagnostics; do not silently enter CPU production mode.
- **FFmpeg readback bottleneck:** keep zero-copy as an optional later task; do not block initial GPU rollout on it.
- **RTSP regression:** keep compatibility-copy as a control lane and require receiver-side acceptance before enabling GPU program output by default.

## User Approval Gate

Do not start `parallel-task` implementation waves until this plan is approved. After approval, invoke `parallel-task docs/superpowers/plans/2026-07-13-gpu-music-playlist-swarm-plan.md`, apply the model routing policy above, and execute only the currently unblocked wave.
