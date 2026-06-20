# ffprobe Self-Healing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every server media path recover a missing or stale ffprobe into the application-local bin directory.

**Architecture:** Add one exported resolver in `internal/video`, keep the existing FFmpeg bundle downloader, and make the server delegate to it. A small installer seam permits network-free RED/GREEN tests.

**Tech Stack:** Go, os/exec, existing FFmpeg downloader, Go tests.

---

### Task 1: Self-healing ffprobe resolver

**Files:**
- Modify: `internal/video/toolchain.go`
- Modify: `internal/video/toolchain_test.go`

- [ ] Add `TestEnsureFFprobeRepairsStaleConfiguredPath` and `TestEnsureFFprobeConcurrentRepairRunsInstallerOnce`. Use a temporary `IMAGEPAD_DATA_DIR`, empty PATH, stale `IMAGEPAD_FFPROBE`, and an injected installer that writes executable ffmpeg/ffprobe fixtures into the local bin directory.
- [ ] Run `rtk go test ./internal/video -run '^TestEnsureFFprobe' -count=1 -v`; expected RED because `EnsureFFprobe` and the installer seam do not exist.
- [ ] Add one mutex, an installer seam defaulting to `downloadFFmpeg`, and `EnsureFFprobe`. Re-check candidates after locking; if none is usable, run the installer, validate local ffprobe with `-version`, and return it. A stale explicit path must not stop recovery.
- [ ] Run focused tests and `rtk go test ./internal/video -count=1`.

### Task 2: Remove the server resolver divergence

**Files:**
- Modify: `internal/server/audio_upload.go`
- Modify: `internal/server/audio_upload_test.go`

- [ ] Add `TestFindFFprobeDelegatesToVideoResolver` through a package-level resolver seam and verify server acquisition uses its returned path.
- [ ] Run `rtk go test ./internal/server -run '^TestFindFFprobeDelegates' -count=1 -v`; expected RED because server still owns environment/sibling-only resolution.
- [ ] Replace the server implementation with `video.EnsureFFprobe`; remove its obsolete executable-name and filesystem lookup code.
- [ ] Run focused tests, `rtk go test ./internal/server ./internal/video -count=1`, `rtk go build ./...`, and `rtk git diff --check`.

### Task 3: Commit

- [ ] Stage only the plan and ffprobe implementation/test files. Preserve `internal/about/about.go` and unrelated visualizer planning changes.
- [ ] Commit `fix: self-heal missing ffprobe`.
