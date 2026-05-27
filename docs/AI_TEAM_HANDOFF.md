# AI Team Handoff

This document gives the local AI agent team enough project context to continue ImagePadServer development.

## Start Here

1. Read the latest entry in **[AI_SESSION_LOG.md](AI_SESSION_LOG.md)** for recent changes, test status, and backlog.
2. Read this file for stable project context and guardrails.
3. Use **[AI_DEVELOPMENT_WORKFLOW.md](AI_DEVELOPMENT_WORKFLOW.md)** for team workflow when using Flowise or multi-agent handoff.

### Latest Session Snapshot (2026-05-24)

- **Done**: Refactored `internal/server/server.go` by extracting admin access checks to `auth.go`, remote URL/image download handling to `remote_upload.go`, media path/type helpers to `media_paths.go`, and deleted-image rendering to `deleted_image.go`.
- **Done**: Removed duplicate post-publish clipboard response handling with `withClipboardResult`; simplified reconnect channel sends in `internal/app/app.go`; removed unused `tunnel.ImageURL`; simplified `uploadMemoryLimit`.
- **Done**: Shutdown media workspace reset; `validateRemoteHTTPURL` (SSRF); settings file lock + atomic save; upload memory limits (32MB multipart spill, image read cap, 2GB video upload); yt-dlp failure no longer falls back to image download.
- **Recent changes**: Updated still-image HLS generation to use a 10-second clip in `internal/video/publisher.go`; added a UI notice in `internal/server/ui.go` recommending VRChat loop playback mode for HLS output; added remote admin page auto-close behavior on host disconnect in `internal/server/ui.go`.
- **Recent changes**: Expanded image upload support to allow `maxDimension` up to `8192` and `maxMB` up to `120`, with server-side clamping in `internal/imageproc/processor.go` and `internal/server/server.go`.
- **Recent changes**: Added Cloudflare Tunnel manual reconnect support, including tray menu `再接続`, admin UI reconnect button, `/api/tunnel/reconnect`, and reconnect channel wiring between `internal/app/app.go` and `internal/server/server.go`.
- **Spec confirmed**: Media dir wiped on every app start and shutdown.
- **Not done**: See backlog in `AI_SESSION_LOG.md` (FFmpeg race R6, token logging, doc drift, etc.).
- **Tests**: `go test ./...` passed locally after the refactor.

## Project

ImagePadServer is a local Windows-first helper app for VRChat ImagePad/video-player workflows.

Users can upload images or videos from a PC or phone, then copy a URL that VRChat can load. Image uploads are converted into ImagePad-friendly image URLs. When video-player mode is enabled, images and videos are converted and served as HLS for VRChat video players.

## Current Baseline

- Language: Go
- Module: `imagepadserver`
- Entry point: `cmd/imagepadserver/main.go`
- Current tests: `go test ./...`
- Go is installed at `C:\Program Files\Go\bin\go.exe`.
- README is valid UTF-8. If PowerShell shows mojibake, read it with `Get-Content README.md -Encoding UTF8`.
- Current branch status when this handoff was updated: preparing `v1.0.8` release on `main`.

## Main Packages

- `internal/app`: application startup, server lifecycle, browser/native window launch, tray, tunnel startup.
- `internal/server`: HTTP routes, JSON API, embedded browser UI, admin access checks.
- `internal/imageproc`: image decoding, resizing, format conversion, size tuning.
- `internal/library`: current media state and temporary storage.
- `internal/video`: FFmpeg/yt-dlp discovery/download, HLS generation, video quality presets, progress.
- `internal/tunnel`: Cloudflare Tunnel startup and public URL discovery.
- `internal/network`: LAN/Tailscale address detection and tests.
- `internal/settings`: app settings and admin token storage.
- `internal/tray`, `internal/appwindow`, `internal/clipboard`: Windows desktop integration.
- `internal/steamvr`: archived/frozen SteamVR work. Do not revive unless explicitly requested.

## Guardrails

- Preserve local-first behavior. Do not require cloud accounts for core image/video serving.
- Keep admin surfaces protected. Localhost is allowed; LAN admin access requires the token QR/cookie flow.
- Treat media public URLs as intentionally accessible from outside when using Cloudflare Tunnel.
- Avoid UPnP revival unless the user explicitly asks; current app message says UPnP auto port mapping is disabled for safety.
- Do not touch archived SteamVR launch handling unless the user explicitly asks.
- Keep Windows UX quiet: background FFmpeg/yt-dlp/cloudflared processes should not flash console windows.
- Keep UI text as valid UTF-8 Japanese.

## Release Numbering Rules

- Do not overwrite or replace assets on an already published stable release number.
- If a published stable release needs a fix, create a new stable version such as `v1.2.1`; do not mutate `v1.2.0`.
- Development/test builds may be published as prereleases using monotonically increasing dev suffixes such as `v1.2.1-dev1`, `v1.2.1-dev2`, etc.
- Do not overwrite dev release assets either; create the next dev number when another test build is needed.
- Only create or update a stable release when the user explicitly asks to release it.
- The official site/download links should point at stable releases only unless the user explicitly asks to publish a dev link.
- Moving tags, replacing checksums, or clobbering release assets requires explicit confirmation from the user.

## Current Development Themes

1. Fix or improve Japanese UI strings where mojibake appears in embedded UI code.
2. Improve video/HLS reliability for VRChat players.
3. Improve progress/status UX during video conversion.
4. Improve quality auto-selection based on upload bandwidth.
5. Keep release packaging and Windows executable behavior stable.

## Team Roles

### Supervisor

Routes work to the right specialist, keeps scope small, asks for missing constraints, and decides when the team can stop.

### Product Lead

Clarifies the intended VRChat/ImagePad workflow, writes acceptance criteria, and protects the user's local-first experience.

### Go Backend Engineer

Implements changes in Go, especially in `internal/server`, `internal/video`, `internal/imageproc`, `internal/library`, and `internal/app`.

### Windows UX Engineer

Handles tray/native window/clipboard/Windows process behavior, packaging details, and user-facing Japanese UI text.

### QA Reviewer

Reviews for regressions, security issues, race conditions, test gaps, and verifies with `go test ./...`.

## Standard Work Loop

1. Read this handoff, `README.md`, `docs/ARCHITECTURE.md`, and the files relevant to the task.
2. Identify the smallest safe change.
3. Implement with existing package boundaries.
4. Run `go test ./...`.
5. Report changed files, tests, risks, and next recommendations.

## Useful Commands

```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
go test ./...
go run .\cmd\imagepadserver
```

```powershell
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -trimpath -ldflags "-H=windowsgui" -o dist\1.2.2\dev\dev1\win\imagepadserver-v1.2.2-dev1-windows-amd64.exe .\cmd\imagepadserver
```
