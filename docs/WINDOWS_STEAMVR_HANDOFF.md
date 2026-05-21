# Windows / SteamVR Handoff

This file is for a Codex session or worker AI running on a Windows machine with SteamVR installed.

## Goal

Add a Windows-only SteamVR integration for ImagePadServer.

The core app is already a cross-platform Go web server. Do not move image processing, upload handling, or UPnP logic into the SteamVR code. The SteamVR feature should call the existing local HTTP API and remain optional.

## Current Project State

ImagePadServer currently provides:

- Browser UI
- Smartphone QR on desktop
- Smartphone-friendly upload UI
- Image upload
- Resize/convert for VRChat ImagePad usage
- `/image/current` image serving
- UPnP port mapping
- Public ImagePad URL when UPnP succeeds
- Local ImagePad URL behind a reveal button
- `internal/steamvr` optional startup hook with Windows/non-Windows build tags

Important files:

- `cmd/imagepadserver/main.go`: process entry point
- `internal/app/app.go`: app boot, HTTP server lifecycle, browser launch, UPnP background task
- `internal/server/server.go`: HTTP routes and JSON state/upload API
- `internal/server/ui.go`: embedded browser UI
- `internal/imageproc/processor.go`: image resize/convert
- `internal/library/store.go`: current image storage
- `internal/upnp/upnp.go`: UPnP discovery and port mapping
- `internal/steamvr`: optional SteamVR integration hook; currently no-op

Run locally:

```powershell
go version
go run ./cmd/imagepadserver
```

Optional fixed port:

```powershell
$env:IMAGEPAD_PORT="8095"
go run ./cmd/imagepadserver
```

Test:

```powershell
go test ./...
```

Build:

```powershell
go build -o imagepadserver.exe ./cmd/imagepadserver
```

## Existing HTTP API

Use these endpoints from SteamVR code rather than adding duplicate state.

### `GET /api/state`

Returns app state.

Relevant fields:

```json
{
  "phoneURL": "http://192.168.x.x:8080/",
  "imageURL": "http://global-ip:8080/image/current?v=...",
  "publicImageURL": "http://global-ip:8080/image/current?v=...",
  "localImageURL": "http://192.168.x.x:8080/image/current?v=...",
  "current": {
    "id": "...",
    "contentType": "image/jpeg",
    "width": 1024,
    "height": 768,
    "sizeBytes": 123456,
    "originalName": "example.png"
  },
  "upnp": {
    "ok": true,
    "externalIP": "...",
    "gateway": "...",
    "service": "..."
  }
}
```

### `POST /api/upload`

Multipart form:

- `image`: file
- `format`: `jpeg` or `png`
- `quality`: JPEG quality, default `88`
- `maxDimension`: default/max `2048`; larger values are clamped by the server
- `maxMB`: default/max `30`; larger values are clamped by the server

The size limit applies to the encoded output. JPEG tries lower quality settings down to 50 before failing. PNG cannot reduce quality, so it fails if the encoded PNG is still larger than the limit.

### `GET /image/current`

Returns the currently selected image.

## Desired SteamVR UX

Windows-only optional feature:

- Show a SteamVR dashboard/overlay entry for ImagePadServer.
- Let the user open/select an image from VR.
- Upload the selected image to the running local server, or pass it to the same image processing path.
- Show the currently published image preview if feasible.
- Show/copy the ImagePad URL.
- Prefer `publicImageURL` when it is non-empty.
- Offer `localImageURL` only as a secondary/fallback option.
- Provide a button to open the browser UI on desktop.

Keep the browser UI as the source of truth. SteamVR should be a convenience surface, not a second application model.

## Recommended Implementation Shape

Prefer a Windows-only package behind build tags.

Suggested layout:

```text
internal/steamvr/
  steamvr_windows.go
  steamvr_unsupported.go
```

Use build tags:

```go
//go:build windows
// +build windows
```

and for unsupported platforms:

```go
//go:build !windows
// +build !windows
```

Expose a tiny interface:

```go
package steamvr

type Config struct {
    ServerURL string
}

func Start(cfg Config) error
```

Then call it from `internal/app/app.go` after the HTTP server starts. On non-Windows, `Start` should be a no-op.

Do not let this return value stop the core web server. SteamVR missing, SteamVR not running, OpenVR initialization failure, or helper process failure should be logged and treated as an optional integration failure.

## Integration Options To Investigate

SteamVR overlays are normally implemented through OpenVR.

Possible approaches:

1. Use an existing Go OpenVR binding if it is maintained enough.
2. Use a small Windows helper executable written in C++/C# and launched by the Go app.
3. Defer native overlay and first ship a Windows tray/menu integration that opens the browser UI and copies URLs.

Do not commit to a heavy dependency before verifying it can:

- Build on Windows
- Run with current SteamVR
- Create a dashboard overlay or persistent overlay
- Receive click events
- Display a simple UI or texture

If native overlay becomes too costly, implement a tray first and document SteamVR overlay as future work.

Current investigation notes:

- Valve's OpenVR `IVROverlay` API supports dashboard overlays, overlay events, mouse-style controller input, and setting textures or files as overlay content. That matches the desired dashboard-tab proof of concept.
- OpenVR applications should initialize as an overlay application (`VRApplication_Overlay`) rather than a scene/game application.
- The visible Go binding candidate found so far is `github.com/tbogdala/openvr-go`. It exposes OpenVR concepts and events, but appears old and should not be adopted until it is proven to build and initialize against the current SteamVR runtime on the target Windows machine.
- A small C++ or C# helper remains the lower-risk native overlay path if the Go binding is stale. Keep it as a separate executable launched by the Go app so the core server remains portable and SteamVR failures stay optional.
- For a quick user-facing fallback, a Windows tray/menu integration can provide "Open browser UI" and "copy public/local URL" without OpenVR.

Reference links:

- Valve OpenVR repository: https://github.com/ValveSoftware/openvr
- Valve IVROverlay overview: https://github.com/ValveSoftware/openvr/wiki/IVROverlay_Overview
- Valve OpenVR API documentation: https://github.com/ValveSoftware/openvr/wiki/API-Documentation
- Go binding candidate: https://pkg.go.dev/github.com/tbogdala/openvr-go

## Suggested Milestones

1. Confirm the app builds and runs on Windows.
2. Confirm upload and URL copy work from Edge/Chrome.
3. Add `internal/steamvr` no-op package and app hook. Done.
4. Investigate OpenVR overlay feasibility. In progress.
5. Implement the smallest Windows-only proof of concept:
   - SteamVR dashboard icon appears
   - Clicking it opens the ImagePadServer browser UI
6. Add image selection:
   - Native file picker or overlay-triggered browser upload
7. Add URL copy:
   - Copy `publicImageURL` from `/api/state` when non-empty
   - Otherwise show `localImageURL` as the fallback option, clearly labeled as local/LAN only
8. Add tests/build checks that non-Windows builds remain unaffected.

## Manual Test Checklist

On Windows:

```powershell
go test ./...
go build -o imagepadserver.exe ./cmd/imagepadserver
.\imagepadserver.exe
```

Then verify:

- `go version` works and the expected Go toolchain is on `PATH`.
- Browser UI opens.
- Uploading PNG/JPEG works.
- `/api/state` returns `publicImageURL`, `localImageURL`, and `imageURL`.
- If UPnP fails, SteamVR code does not copy the non-URL `imageURL` fallback text.
- UPnP failure does not crash the app.
- SteamVR not installed does not crash the app.
- SteamVR installed but not running does not crash the app.
- SteamVR running shows the expected overlay/dashboard behavior.
- Non-Windows build remains valid in CI.

## Constraints

- Keep cross-platform behavior intact.
- Keep SteamVR code Windows-only.
- Do not require SteamVR for normal server usage.
- Do not block app startup if SteamVR integration fails.
- Do not duplicate image processing logic.
- Do not put native/experimental code into `internal/server`.
- Avoid forcing admin privileges for the core app.

## Notes About ImagePad / VRChat

The URL shown to ImagePad may still require VRChat's `Allow Untrusted URLs`, because locally hosted URLs are usually not on VRChat's trusted image host list.

The app should not promise that other users can always see the image. That depends on:

- UPnP success
- Router/firewall behavior
- ISP NAT/CGNAT
- VRChat URL trust settings
- ImagePad/world implementation

## If You Need To Change The HTTP API

Keep compatibility with the browser UI. If adding endpoints for SteamVR, prefer additive APIs such as:

- `POST /api/copy-image-url`
- `POST /api/open-browser`
- `GET /api/current-image-metadata`

Document any new endpoint in this file and `README.md`.
