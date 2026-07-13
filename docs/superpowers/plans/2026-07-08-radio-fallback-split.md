# Radio Fallback Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Separate playlist-radio fallback streaming from `RadioManager` so CPU and future DirectX fallback renderers can be swapped without changing radio session control.

**Architecture:** `RadioManager` decides when fallback runs. A new `RadioFallbackFeeder` owns fallback renderer creation, FFmpeg process setup, stdin frame feeding, and stdout copying into the persistent publisher. `internal/video` remains responsible for frame rendering and FFmpeg argument construction.

**Tech Stack:** Go, FFmpeg, existing `internal/video` radio fallback renderer, existing `internal/obsrtmp` radio publisher pipeline.

## Global Constraints

- Keep this refactor behavior-preserving for playlist radio fallback streaming.
- Do not introduce DirectX in this task; create the boundary that will allow it next.
- Keep test seams in `RadioManager` so existing tests can replace fallback execution.
- Do not touch release/version files in this task.

---

### Task 1: Extract Radio Fallback Feeder

**Files:**
- Create: `internal/obsrtmp/radio_fallback.go`
- Modify: `internal/obsrtmp/radio.go`
- Test: `internal/obsrtmp/radio_fallback_test.go`

**Interfaces:**
- Consumes: `video.EnsureFFmpeg()`, `video.VisualizerFonts()`, `video.WriteRadioFallbackLogo(outDir)`, `video.RadioFallbackRenderSize(preset)`, `video.NewRadioFallbackRenderer(...)`, `video.RadioFallbackFeederArgs(...)`, `video.WriteRadioFallbackFrames(...)`.
- Produces: `type RadioFallbackFeeder struct`, `func NewRadioFallbackFeeder(outDir string, preset func() video.QualityPreset) *RadioFallbackFeeder`, `func (f *RadioFallbackFeeder) Run(ctx context.Context, timestampOffset float64, sink io.Writer) error`.

- [ ] **Step 1: Add failing feeder tests**

Add tests that construct a feeder with injected dependencies and verify it passes the selected preset, timestamp offset, and sink through the pipeline.

- [ ] **Step 2: Create feeder implementation**

Move the current `runFFmpegFallbackFeeder` logic out of `RadioManager` into `RadioFallbackFeeder.Run`, preserving error messages and context cancellation behavior.

- [ ] **Step 3: Delegate from RadioManager**

Replace `RadioManager.runFFmpegFallbackFeeder` internals with `NewRadioFallbackFeeder(m.outDir, presetFn).Run(ctx, timestampOffset, sink)`.

- [ ] **Step 4: Run targeted tests**

Run: `go test ./internal/obsrtmp ./internal/video -count=1`
Expected: PASS.

- [ ] **Step 5: Run broader impacted tests**

Run: `go test ./internal/server ./internal/video ./internal/obsrtmp ./internal/playlist -count=1`
Expected: PASS or report exact timeout/failure.
