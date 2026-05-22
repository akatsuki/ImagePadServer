# Architecture

ImagePadServer is split into small packages so platform-specific integrations can be added without turning the web server into a single large module.

## Packages

- `cmd/imagepadserver`: process entry point
- `internal/app`: application boot, HTTP server lifecycle, browser launch, UPnP startup
- `internal/server`: HTTP routes, JSON API, embedded browser UI
- `internal/imageproc`: image decoding, resizing, format conversion, size tuning
- `internal/library`: current image state and temporary file storage
- `internal/network`: LAN and Tailscale address detection
- `internal/upnp`: SSDP discovery, WAN service selection, SOAP port mapping
- `internal/browser`: OS-specific browser opening

## Runtime Flow

1. Start the local HTTP server.
2. Detect the advertised LAN or Tailscale address.
3. Open the browser UI.
4. Start UPnP discovery and port mapping in the background.
5. Accept image uploads from the browser UI.
6. Convert the image for VRChat/ImagePad usage.
7. Serve the selected image at `/image/current`.
8. Prefer the public ImagePad URL when UPnP provides an external IP.

## Future Windows/SteamVR Integration

SteamVR support should be added as a Windows-only package that talks to the existing HTTP API instead of directly touching image processing or storage. That keeps the core server portable across Windows, macOS, and Linux.
