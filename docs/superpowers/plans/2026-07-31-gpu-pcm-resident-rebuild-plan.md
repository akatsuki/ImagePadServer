# Plan: PCM-resident GPU music renderer rebuild

**Generated**: 2026-07-31

## Overview

Rebuild the music/playlist GPU hot path around a persistent wgpu compositor and
a VRAM-resident PCM ring. CPU remains responsible for compressed-audio decode,
metadata, orchestration, and mux control; GPU owns PCM feature extraction,
visual composition, YUV conversion, and the encoder-facing frame resource.
The existing CPU renderer remains the golden reference and is never removed
until parity is re-established.

The current path is not a suitable optimization base: Go sends one request per
frame, the sidecar allocates frame-sized YUV/readback buffers, and FFmpeg
consumes packed CPU YUV through stdin. The new path must make those boundaries
explicit and measurable instead of treating a passing image gate as evidence
of zero-copy execution.

## Prerequisites

- Existing wgpu 0.20 sidecar and Go scene contracts.
- NVIDIA/AMD/Apple hardware validation where available.
- CPU reference render and strict 150-frame comparison fixtures.
- No new codec dependency: compressed audio decode stays on the existing CPU
  path; only PCM transport and feature extraction move to GPU.

## Dependency Graph

```text
T1 contract/profiling ──┬── T2 persistent sidecar ──┬── T4 Go production route
                       │                           │
                       └── T3 PCM VRAM + GPU FFT ───┘
                                                   │
                                                   └── T5 encoder transport
                                                           │
                                                           └── T6 parity/benchmark
                                                                   │
                                                                   └── T7 final review
```

## Tasks

### T1: Freeze the execution contract and stage timings
- **depends_on**: []
- **tier**: 4 (architect)
- **location**: `internal/video/gpu_contracts.go`, `internal/video/gpu_sidecar_process.go`, `gpu/playlist-compositord/src/protocol.rs`, `docs/MUSIC_RENDER_V2_PROTOCOL.md`
- **description**: Add explicit job/session/PCM-ring/frame-resource contracts and a stage-timing record. Define that a job uploads PCM once, accepts ordered frame descriptors, and returns a GPU-owned frame handle; packed CPU YUV is diagnostic-only.
- **validation**: Contract round-trip tests reject per-frame PCM uploads and CPU-plane payloads; timing records include upload, feature, composite, YUV, transport, and encode stages.
- **status**: In Progress
- **log**:
- **files edited/created**:

### T2: Make the sidecar persistent and batch-oriented
- **depends_on**: [T1]
- **tier**: 3 (senior engineer)
- **location**: `gpu/playlist-compositord/src/main.rs`, `gpu/playlist-compositord/src/protocol.rs`, `gpu/playlist-compositord/src/gpu_render.rs`
- **description**: Add `BeginJob`, `UploadPcm`, `RenderBatch`, and `EndJob` protocol messages. Retain the adapter/device/pipelines and reusable frame resources for the whole job. Eliminate per-frame buffer creation and per-frame renderer setup.
- **validation**: Rust tests prove one renderer/session handles 150 ordered frames, no frame-sized `MAP_READ` buffers are created in the production path, and cancellation closes the job deterministically.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T3: Upload PCM once and compute features in VRAM
- **depends_on**: [T1]
- **tier**: 3 (senior engineer)
- **location**: `internal/video/audio_analysis.go`, `internal/video/audio_visualizer.go`, `gpu/playlist-compositord/src/audio_gpu.rs`, `gpu/playlist-compositord/src/shaders`
- **description**: Keep CPU compressed-audio decode, pack bounded float/Q15 PCM chunks into a persistent storage buffer, upload through a bounded staging ring, and compute waveform/RMS/loudness/spectrum in WGSL. Remove the per-frame `StreamWaveformPCMHistoryFrames` dependency from the production GPU route.
- **validation**: CPU and GPU feature arrays match within defined tolerances for deterministic fixtures; one PCM upload is recorded per job; no per-frame PCM transfer occurs.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T4: Replace the Go per-frame route with the persistent job route
- **depends_on**: [T2, T3]
- **tier**: 3 (senior engineer)
- **location**: `internal/video/audio_visualizer.go`, `internal/video/gpu_sidecar_process.go`, `internal/video/music_scene.go`
- **description**: Send one job contract and an ordered frame timeline to the sidecar. Remove the per-frame `RenderSceneYUV` + `PackedBytes` loop from production. Preserve CPU reference and explicit GPU-required failure behavior.
- **validation**: 150-frame render uses one sidecar process and one PCM upload; stage timings are emitted; single and playlist paths share `CanonicalMusicScene`.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T5: Connect GPU-owned output to the encoder without CPU YUV readback
- **depends_on**: [T2, T4]
- **tier**: 4 (architect)
- **location**: `gpu/playlist-compositord/src/gpu_yuv_transport.rs`, `gpu/playlist-compositord/src/hardware_surface.rs`, `internal/video/audio_visualizer.go`, `internal/video/video_encoder.go`
- **description**: Implement a platform backend for an imported GPU surface or a persistent GPU staging path. Keep CPU packed YUV only as an explicitly named diagnostic fallback; production must fail closed when the encoder bridge cannot accept GPU resources.
- **validation**: Runtime evidence shows zero `MAP_READ`/`read(&ys/us/vs)` in the production encoder path and identifies the actual NVIDIA, AMD, and Apple backend used.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T6: Re-run parity, performance, and failure gates
- **depends_on**: [T3, T4, T5]
- **tier**: 2 (standard engineer)
- **location**: `cmd/music-render-compare`, `internal/video/*_test.go`, `artifacts/`
- **description**: Add end-to-end 150-frame tests, CPU/GPU feature parity, stage timing reports, cancellation, no-GPU fail-closed, and playlist/single-route equivalence. Use release builds for performance measurement.
- **validation**: GPU wall time beats the CPU reference on the target machine; frame count/PTS/duration match; MAE/RMSE remain within the agreed parity threshold; all production static guards pass.
- **status**: Not Completed
- **log**:
- **files edited/created**:

### T7: Independent architecture and release review
- **depends_on**: [T6]
- **tier**: 5 (final reviewer)
- **location**: all changed files and acceptance artifacts
- **description**: Review the implementation against GPU-only rendering, VRAM PCM residency, encoder ownership, cross-platform behavior, and the original CPU parity requirement. Do not approve based on contract tests alone.
- **validation**: Reviewer signs off only with runtime evidence and no unresolved readback, CPU-overlay, or per-frame IPC findings.
- **status**: Not Completed
- **log**:
- **files edited/created**:

## Parallel Execution Groups

| Wave | Tasks | Can Start When |
|------|-------|----------------|
| 1 | T1 | Immediately |
| 2 | T2, T3 | T1 complete |
| 3 | T4 | T2 and T3 complete |
| 4 | T5 | T2 and T4 complete |
| 5 | T6 | T3, T4, and T5 complete |
| 6 | T7 | T6 complete |

## Testing Strategy

- Rust unit tests for protocol, persistent buffers, PCM feature kernels, and
  cancellation.
- Go contract tests for one-upload-per-job and CPU reference preservation.
- 150-frame NVIDIA strict comparison, followed by AMD/iGPU and macOS smoke
  where hardware is available.
- Stage timing report separating CPU decode, upload, GPU feature work,
  composition, YUV/encoder transport, and mux.
- Static audit forbidding production `MAP_READ`, packed YUV, ASS/showwaves,
  and per-frame sidecar start/stop.

## Risks & Mitigations

- **wgpu cannot import a native encoder surface uniformly**: keep the backend
  trait explicit and fail closed; implement D3D12/Vulkan/Metal adapters behind
  it rather than silently reading back.
- **GPU FFT parity drift**: keep a deterministic CPU feature fixture and compare
  bounded arrays before enabling dynamic layers in production.
- **Audio upload stalls**: use a persistent staging ring and job-level upload,
  not `queue.write_buffer` from the frame loop.
- **Debug-build distortion**: performance claims require release sidecar and Go
  binaries with recorded fingerprints.
- **Parity regressions**: retain CPU golden output and do not remove the
  reference path until all layer and whole-frame gates pass.
