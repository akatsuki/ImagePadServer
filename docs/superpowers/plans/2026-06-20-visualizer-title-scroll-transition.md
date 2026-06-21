# Visualizer Title Scroll Transition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep long metadata on one line, extend its scroll by two seconds at the existing speed, and reset through a 300 ms fade-out, 500 ms blank interval, and 300 ms fade-in.

**Architecture:** Keep timing and event generation in `visualizer_ass.go`. `scrollCycle` owns the 2.0-second movement extension and 0.5-second blank interval; `buildScrollingDialogue` owns extra travel, ASS fade tags, and the event gap.

**Tech Stack:** Go, ASS/libass events, FFmpeg runtime rendering, Go tests.

---

### Task 1: Lock extended cycle timing

**Files:**
- Modify: `internal/video/visualizer_ass_test.go`
- Modify: `internal/video/visualizer_ass.go`

- [ ] Change `TestASSScrollOverflowCycle` expectations so 248 px overflow at 40 px/s produces movement `248/40 + 2 = 8.2` seconds and total cycle `3 + 8.2 + 0.5 = 11.7` seconds. Apply equivalent expectations at 360p and 1080p.
- [ ] Run `rtk go test ./internal/video -run '^TestASSScrollOverflowCycle$' -count=1 -v` and confirm RED on old movement values.
- [ ] Add named constants for 2.0-second extra movement, 0.5-second blank time, and 300 ms fades. Update `scrollCycle` to include movement extension and blank interval.
- [ ] Rerun the focused test and confirm GREEN.

### Task 2: Lock movement, fade, and gap events

**Files:**
- Modify: `internal/video/visualizer_ass_test.go`
- Modify: `internal/video/visualizer_ass.go`

- [ ] Add a failing test for a 1000 px title in a 752 px viewport at 1280 width. Require initial hold `0.00..3.00` with `\fad(300,0)`, movement `3.00..11.20` from X=432 to X=104 with `\fad(0,300)`, no event for `11.20..11.70`, and next hold beginning at `11.70`.
- [ ] Run `rtk go test ./internal/video -run '^TestScrollingDialogueExtendedFadeCycle$' -count=1 -v` and confirm RED.
- [ ] Update `buildScrollingDialogue` to end movement at `viewportX - overflow - scaledSpeed*2`, end the scroll event before the 500 ms blank interval, and attach fade tags to outgoing and incoming events.
- [ ] Rerun the focused test and confirm GREEN.

### Task 3: Runtime and repository verification

**Files:**
- Modify only if a failing test exposes a defect covered by a new regression assertion.

- [ ] Render the reported China Advice title through FFmpeg/libass at hold, movement, fade-out, blank, and fade-in timestamps.
- [ ] Run `rtk go test ./... -skip '^TestRenderFallbackArtworkGolden$' -count=1`.
- [ ] Run `rtk go build ./...`.
- [ ] Run `rtk git diff --check` and confirm no temporary diagnostic source remains.

