# Music Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a video-player-only Music Mode that downloads yt-dlp page URLs as audio plus artwork and publishes them through the existing audio visualizer.

**Architecture:** Persist `MusicModeEnabled` beside `VideoPlayerEnabled`, expose it inside the existing video-player state plus a dedicated admin endpoint, and render its toggle only while video-player support is active. In Music Mode, URL publish and queue handlers route non-SoundCloud page URLs through a generic yt-dlp audio acquisition function; direct media and normal video mode retain their current paths.

**Tech Stack:** Go, net/http, embedded HTML/JavaScript, yt-dlp, ffprobe/FFmpeg, Go tests.

---

### Task 1: Persist and expose Music Mode

**Files:**
- Modify: `internal/settings/settings.go`
- Modify: `internal/server/server.go`
- Test: `internal/server/server_test.go`

- [ ] Add failing handler tests proving Music Mode can only be enabled while video-player support is enabled and is cleared when video-player support is disabled.
- [ ] Run `go test ./internal/server -run 'MusicMode|VideoPlayer'` and verify the new tests fail because the endpoint/state do not exist.
- [ ] Add `MusicModeEnabled`, register `/api/music-mode`, return `musicModeEnabled` in video-player state, and clear Music Mode in the video-player disable transaction.
- [ ] Re-run the focused tests and verify they pass.

### Task 2: Acquire generic page audio through yt-dlp

**Files:**
- Create: `internal/video/music_download.go`
- Create: `internal/video/music_download_test.go`
- Modify: `internal/video/audio_types.go`

- [ ] Add a failing unit test that captures yt-dlp arguments and requires `--no-playlist`, `-f bestaudio/best`, thumbnail/info sidecars, a bounded maximum file size, and manifest-selected output.
- [ ] Run `go test ./internal/video -run Music` and verify it fails because `DownloadMusic` and `SourceMusic` do not exist.
- [ ] Implement `DownloadMusic` using a unique prefix and `ReadSinglePathManifest`, parse generic yt-dlp metadata, and return `AcquiredAudio` with `SourceMusic`.
- [ ] Re-run the focused tests and verify they pass.

### Task 3: Route Music Mode URL requests into the visualizer

**Files:**
- Modify: `internal/server/audio_upload.go`
- Modify: `internal/server/server.go`
- Test: `internal/server/upload_url_test.go`

- [ ] Add failing publish and queue routing tests that inject the music downloader and assert the secure direct downloader is bypassed only when Music Mode is active.
- [ ] Run the focused server tests and verify the expected routing failures.
- [ ] Add a server acquisition helper that probes downloaded music audio, extracts embedded artwork, preserves yt-dlp artwork/metadata, and calls the existing publish/queue visualizer functions.
- [ ] Re-run the focused tests and verify normal-mode direct-media tests still pass.

### Task 4: Add the conditional UI toggle

**Files:**
- Modify: `internal/server/ui.go`
- Test: `internal/server/ui_media_test.go`

- [ ] Add failing static UI tests requiring the Music Mode row, endpoint call, and conditional visibility tied to video-player state.
- [ ] Run `go test ./internal/server -run 'MusicModeUI'` and verify failure.
- [ ] Add the toggle immediately below video-player support, hide it while video-player support is off, and synchronize it through `/api/music-mode`.
- [ ] Run server and video package tests, then `go test ./...` and `go build ./...`.

### Task 5: Keep yt-dlp cookie integration frozen

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/ui.go`
- Modify: `internal/video/music_download.go`
- Modify: `internal/video/soundcloud_download.go`
- Modify: `internal/video/publisher.go`
- Test: `internal/server/music_mode_test.go`
- Test: `internal/video/music_download_test.go`

- [ ] Add failing tests proving browser-cookie detection and cookie arguments are absent.
- [ ] Remove the browser-reporting endpoint and client-side browser detection.
- [ ] Ensure Music, SoundCloud, and standard video yt-dlp calls never receive `--cookies` or `--cookies-from-browser`.
- [ ] Re-run focused tests, full tests excluding the known visualizer golden mismatch, and the Windows build.

### Self-review

- Spec coverage: subordinate visibility, automatic disable, yt-dlp audio-only acquisition, publish and queue routing, unchanged normal video mode, and globally frozen cookie integration are all assigned above.
- Placeholder scan: no deferred implementation items remain.
- Type consistency: persisted/API name is `MusicModeEnabled`; runtime JSON name is `musicModeEnabled`; acquired source kind is `SourceMusic`.
