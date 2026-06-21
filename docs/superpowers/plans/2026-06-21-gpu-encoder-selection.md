# GPU Encoder Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prefer validated hardware H.264 encoders across every video-producing FFmpeg path and retry once with libx264 when hardware encoding fails.

**Architecture:** Add a focused `internal/video/video_encoder.go` component for platform priority, capability probing, cached selection, profile-specific FFmpeg arguments, and generic fallback execution. Inject the selected profile into pure argument builders in video and OBS paths; keep non-video FFmpeg calls unchanged.

**Tech Stack:** Go, FFmpeg CLI, H.264 NVENC/QSV/AMF/VideoToolbox/libx264, Go tests.

---

### Task 1: Encoder profiles and platform priority

**Files:**
- Create: `internal/video/video_encoder.go`
- Create: `internal/video/video_encoder_test.go`

- [ ] Write failing table tests requiring Windows priority `h264_nvenc`, `h264_qsv`, `h264_amf`, `libx264`; macOS priority `h264_videotoolbox`, `libx264`; and other platforms `libx264`.
- [ ] Write failing tests for standard and low-latency profile arguments, including bitrate caps for hardware profiles and existing CRF behavior for libx264.
- [ ] Run `go test ./internal/video -run 'VideoEncoder|EncoderPriority'` and confirm undefined profile APIs fail compilation.
- [ ] Implement `VideoEncoderProfile`, `EncoderPurpose`, `CPUVideoEncoder`, platform priority, and deterministic argument construction.
- [ ] Re-run the focused tests and confirm they pass.

### Task 2: Capability probe and process cache

**Files:**
- Modify: `internal/video/video_encoder.go`
- Modify: `internal/video/video_encoder_test.go`

- [ ] Write failing tests with an injected command runner for advertised-encoder parsing, real-probe fallback order, timeout/failure handling, and one-result-per-FFmpeg-path caching.
- [ ] Run the focused tests and verify the probe tests fail for missing selection behavior.
- [ ] Implement `SelectVideoEncoder(ctx, ffmpeg)` using `ffmpeg -encoders`, a bounded synthetic encode, hidden windows, temporary output validation, and a synchronized per-path cache.
- [ ] Re-run focused tests and verify the selection behavior passes.

### Task 3: Non-OBS H.264 call-path integration and fallback

**Files:**
- Modify: `internal/video/audio_visualizer.go`
- Modify: `internal/video/soundcloud.go`
- Modify: `internal/video/publisher.go`
- Modify: associated tests under `internal/video/*_test.go`

- [ ] Add failing argument tests proving visualizer, SoundCloud, still MP4/HLS, and uploaded-video HLS accept an injected hardware profile instead of hard-coded `libx264`.
- [ ] Add failing fallback tests requiring one hardware attempt, job-scoped cleanup, one libx264 attempt, combined errors, and no fallback on cancellation.
- [ ] Run focused tests and confirm failures reference hard-coded libx264 or missing fallback APIs.
- [ ] Inject profiles into each pure argument builder and route execution through the shared hardware-to-CPU fallback helper.
- [ ] Re-run all `internal/video` tests except the known fallback-artwork golden mismatch.

### Task 4: OBS low-latency integration

**Files:**
- Modify: `internal/obsrtmp/manager.go`
- Modify: `internal/obsrtmp/manager_test.go`

- [ ] Add failing tests proving re-encode mode uses injected hardware low-latency arguments while copy mode remains untouched.
- [ ] Add a failing process-attempt test requiring hardware startup failure to retry once with libx264 and never loop.
- [ ] Run `go test ./internal/obsrtmp` and confirm the new tests fail.
- [ ] Select the encoder before building OBS arguments, expose a profile-aware pure builder, and retry the receiver once with CPU when hardware startup fails.
- [ ] Re-run OBS tests and confirm they pass.

### Task 5: State exposure, regression verification, and dev build

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/obsrtmp/manager.go`
- Modify: state tests under `internal/server` and `internal/obsrtmp`
- Modify: `internal/about/about.go`
- Modify: `winres/winres.json`
- Regenerate: `cmd/imagepadserver/rsrc_windows_amd64.syso`

- [ ] Add failing state tests for encoder name and hardware boolean.
- [ ] Expose the cached selected encoder in video-player and OBS status without persisting device details.
- [ ] Run `go test ./... -skip RenderFallbackArtworkGolden` and `go build ./...`.
- [ ] Increment the monotonic `v1.3.0-devN` version, regenerate Windows resources, and build the versioned Windows artifact.
- [ ] Verify embedded file/product versions, SHA-256, process startup, and localhost HTTP 200.

### Self-review

- Spec coverage: all H.264-producing paths, Windows/macOS priority, real probing, cache lifetime, encoder profiles, CPU fallback, OBS handling, state exposure, and non-goals are represented.
- Placeholder scan: no deferred behavior or unspecified implementation steps remain.
- Type consistency: all paths use `VideoEncoderProfile`; selection uses `SelectVideoEncoder`; CPU fallback uses `CPUVideoEncoder`; purposes are standard and low latency.
- Worktree safety: implementation commits are intentionally omitted because the shared worktree already contains overlapping uncommitted user changes; only explicit files are edited and verified.
