# Bundled-only Tooling + Safe Install with Grayout Progress — Design

Date: 2026-06-22

## Problem

ImagePadServer relies on three external tools — `ffmpeg`, `ffprobe`, and
`yt-dlp`. Two problems exist today:

1. **System PATH is used as a fallback.** `ffmpegPath`, `ffprobePath`,
   `usableFFprobePath`, and `ytdlpPath` in `internal/video/toolchain.go` all fall
   back to `exec.LookPath`. The product must use *only* the bundled binaries
   (or an explicit `IMAGEPAD_*` override). Whatever `ffmpeg`/`ffprobe`/`yt-dlp`
   happens to be on the machine's PATH must never be used.

2. **Installs block and freeze the UI.** Enabling video player mode
   (`handleVideoPlayer`, `POST /api/video-player`) calls `video.EnsureFFmpeg()`
   synchronously inside the HTTP handler. A first-time FFmpeg download takes
   minutes, during which the UI is frozen with no feedback. Worse, the HTTP
   server's `WriteTimeout` is 30s, so a long download is killed mid-request.

## Goals

- Never resolve a tool from system PATH. Bundled binary or explicit
  `IMAGEPAD_*` env var only.
- Make tool acquisition maximally robust ("don't fail"): mirror sources,
  bounded retries with backoff, integrity validation, atomic install.
- Replace the freeze with a **full-screen grayout overlay + progress bar** that
  shows tool name, phase, and percent while tools install, and disappears when
  done.
- Surface install progress regardless of which trigger started it
  (startup, video-player toggle, or a media operation acting as a safety net).

## Non-goals

- No change to *which* tools are used or their on-disk location
  (`settings.Dir()/bin/`).
- No new persistent settings or DB schema.
- No UPnP/tunnel/OBS behavior changes.
- Linux/other auto-install support is unchanged: with PATH removed, an explicit
  `IMAGEPAD_*` env var becomes the only way to supply a tool there. This is the
  accepted consequence of "never use PATH".

## Decisions (from brainstorming)

- **PATH removal scope:** all three tools — ffmpeg, ffprobe, yt-dlp.
- **Install timing:** both — proactive at startup *and* a safety net at the
  point of use.
- **Robustness:** mirror/fallback URLs, bounded retries with backoff, and
  startup integrity validation.
- **Failure UX is toggle-centric, not a stuck overlay:** when enabling video
  player mode and the install ultimately fails, revert the toggle back to OFF
  and surface an error. While video player mode is active, keep retrying tool
  acquisition in the background so transient failures self-heal.

---

## Architecture

### Unit A — `internal/video`: bundled-only resolution

Remove every `exec.LookPath(...)` fallback. New resolution order for each tool:

1. `IMAGEPAD_FFMPEG` / `IMAGEPAD_FFPROBE` / `IMAGEPAD_YTDLP` if set and the file
   exists (a stale env value is a diagnostic, not silently ignored).
2. The bundled binary under `settings.Dir()/bin/` (for ffprobe, also the
   directory next to a resolved ffmpeg).

`usableFFprobePath` drops its PATH candidate but keeps `-version` validation of
each candidate.

### Unit B — `internal/video`: install progress tracker

A package-level singleton (guarded by a mutex) holding a snapshot:

```
type ToolInstallStatus struct {
    Active   bool   // an install/repair is currently running
    Tool     string // "ffmpeg" | "ffprobe" | "yt-dlp"
    Phase    string // "download" | "extract" | "validate"
    Percent  int    // 0-100; download phase is byte-driven, others indeterminate
    Attempt  int    // current attempt number across sources/retries
    Failed   bool   // last run exhausted all sources and retries
    Message  string // human-readable status or error
}
```

- `func ToolInstallStatus() ToolInstallStatus` returns a copy for the server.
- Download helpers (`downloadFile`, `downloadFileAllowMissingChecksum`,
  `downloadExecutable`) gain a progress callback. `io.Copy` is wrapped by a
  counting writer that reports `written/total` into the tracker. `total` comes
  from `Content-Length` (already read for the size-limit check); when unknown,
  the bar is indeterminate.
- The existing `ffmpegBundleMu` mutex continues to serialize installs; the
  tracker is updated under it. Concurrent `Ensure*` callers observe the same
  in-flight status instead of starting a second download.

### Unit C — `internal/video`: robust acquisition

- **Source list per tool.** Each download tries an ordered list of sources.
  A source is `{url, checksumURL (optional)}`. Examples:
  - Windows ffmpeg: gyan.dev essentials (primary) → a GitHub mirror
    (e.g. BtbN/GyanD release asset) as fallback.
  - yt-dlp: GitHub latest release (primary) → mirror asset.
  After downloading from any source, the binary is **always** validated with
  `-version`; checksum is verified when a checksum source is available, and the
  download is rejected on mismatch.
- **Bounded retry with backoff.** Each source is attempted up to N times
  (small N, exponential backoff) to ride out transient network errors and
  partial transfers before advancing to the next source.
- **Startup integrity validation.** `func ValidateInstalledTools()` validates
  the bundled ffmpeg/ffprobe/yt-dlp with `-version` (and a basic non-empty/size
  sanity check). A binary that fails validation is treated as missing and
  re-acquired through the normal path.
- Atomic publish (`.tmp` → rename) and the install mutex are retained so a
  partial/interrupted install is never observed as complete. When both ffmpeg
  and ffprobe must be installed, ffprobe is published before ffmpeg so the
  presence of ffmpeg implies a complete pair (existing self-healing invariant).

### Unit D — `internal/video`: async orchestration for video tools

- `func EnsureVideoToolsAsync()` starts (if not already running) a background
  goroutine that ensures ffmpeg + ffprobe, reporting through the tracker. It is
  idempotent and safe to call repeatedly.
- A background **retry loop tied to "video player active"**: while video player
  mode is enabled but the tools are missing/broken, the loop re-runs acquisition
  with backoff so the mode self-heals without user action. The loop exits when
  the tools validate successfully or video player mode is turned off.

### Unit E — `internal/server`: state + async toggle

- `state()` / `stateWithMedia()` add a `"toolInstall"` object built from
  `video.ToolInstallStatus()`. The frontend already polls `/api/state`, so no
  new transport is needed.
- `handleVideoPlayer` (enable=true):
  - If tools already validate → enable immediately (current fast path).
  - Else → call `video.EnsureVideoToolsAsync()` and return immediately with the
    current state (no synchronous download in the handler; avoids the 30s
    `WriteTimeout`). The persisted `VideoPlayerEnabled` is **not** set to true
    yet; an "intent to enable" is recorded.
  - On async success → set `VideoPlayerEnabled=true`, enqueue still conversion,
    `SyncOBSReceiver`.
  - On async failure (all sources + retries exhausted) → leave/return
    `VideoPlayerEnabled=false` (toggle reverts to OFF) and expose the error via
    `toolInstall.Failed` + message.
- The existing media-operation call sites keep calling `Ensure*`; because those
  now report through the tracker, an install triggered deep in a worker still
  drives the same overlay (the "safety net" path).

### Unit F — `internal/server/ui.go`: grayout overlay + progress bar

- A full-screen grayout overlay element with a progress bar, reusing the
  existing `.progress-track` / `.progress-fill` styles (and the
  `.progress-fill.indeterminate` variant for non-download phases).
- The existing `/api/state` poll loop shows the overlay while
  `state.toolInstall.active`, rendering tool name + phase + percent, and hides
  it when inactive. On `toolInstall.failed`, show the error text; the video
  player toggle reflects the reverted OFF state from the same state payload.

### Unit G — `internal/app/app.go`: startup wiring

- At startup, call `video.ValidateInstalledTools()` (integrity check / repair
  of any broken bundled binary).
- If `VideoPlayerEnabled` is persisted true, call
  `video.EnsureVideoToolsAsync()` so tools are warm by the time the UI loads;
  the overlay appears if they are not yet ready.

---

## Data flow

1. Trigger (startup / toggle / media op) → `Ensure*` or
   `EnsureVideoToolsAsync` → tracker `Active=true`.
2. Acquisition iterates sources × retries, updating tracker phase/percent;
   download phase is byte-driven via the counting writer.
3. Frontend `/api/state` poll renders the grayout overlay from `toolInstall`.
4. Success → tracker `Active=false`; video-player intent (if any) is committed.
   Failure → tracker `Failed=true` with message; video-player toggle reverts.

## Error handling

- Stale/invalid `IMAGEPAD_*` value: diagnostic context included in the final
  error only if bundled acquisition also fails; recovery continues.
- All sources + retries exhausted: `toolInstall.Failed=true`, message surfaced;
  video-player toggle reverts to OFF; while video player mode is active the
  background retry loop keeps trying.
- Non-auto-install platforms with no env var: clear hint naming `IMAGEPAD_*`.

## Testing

- `toolchain_test.go`: PATH-present environment must NOT resolve a PATH binary
  (new assertion); bundled/env resolution still works; `usableFFprobePath`
  no longer returns a PATH candidate.
- Progress tracker: counting writer reports monotonic percent to 100; snapshot
  is race-free under concurrent `Ensure*`.
- Source fallback: first source failing advances to the mirror; `-version`
  validation rejects a corrupt download; checksum mismatch rejected.
- `ValidateInstalledTools`: a corrupt bundled binary is detected and re-acquired
  (injected installer seam, as in the existing self-healing tests).
- `handleVideoPlayer`: enabling with missing tools returns immediately with
  `toolInstall.active` and does not persist `VideoPlayerEnabled=true`; on
  injected install failure the toggle stays/returns OFF; on success it is
  enabled.
- UI: `ui_media_test.go`-style assertion that the overlay element and the
  `toolInstall` polling branch are present in the served HTML.

## Open invariants preserved

- Atomic `.tmp` → rename publish; ffprobe-before-ffmpeg ordering; single
  process-wide install mutex; `-version` as the usability gate.
