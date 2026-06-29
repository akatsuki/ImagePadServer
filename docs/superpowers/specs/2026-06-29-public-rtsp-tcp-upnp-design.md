# Public RTSP/TCP over UPnP Design

## Goal

Make the OBS real-time mode playable by both PC and Android VRChat with a
standard `rtsp://` URL while retaining RTSP-over-TCP transport.

When the real-time mode is actively published, ImagePadServer will temporarily
map the MediaMTX RTSP TCP port through UPnP and advertise the router's external
IPv4 address. The mapping must not exist outside that session.

## Scope

- Keep the persisted latency mode ID `rtspt` for settings compatibility.
- Rename its user-facing label to `リアルタイム（RTSP TCP）`.
- Continue configuring MediaMTX with `rtspTransports: [tcp]`.
- Change the advertised player URL scheme from `rtspt://` to `rtsp://`.
- Open the active MediaMTX RTSP port through UPnP only while an RTSP session is
  published.
- Remove the mapping when the stream ends, the receiver stops or restarts, the
  latency mode changes, or the application exits.
- Put the RTSP URL in the normal public/share URL field instead of a dedicated
  RTSP layout block.

Cloudflare Tunnel remains the public transport for HTTP and HLS. It is not used
as an RTSP transport.

## Architecture

### UPnP Mapping Handle

Extend `internal/upnp` with an owned TCP mapping handle. A successful mapping
records the gateway service, internal port, external port, and external IPv4
address. Its `Close` operation deletes exactly that mapping and is idempotent.

The initial implementation maps the same dynamically allocated port externally
and internally. MediaMTX already allocates an available RTSP port for each
session, avoiding a fixed `8554` dependency.

### OBS and MediaMTX Lifecycle

The OBS manager owns the MediaMTX runtime and therefore knows the active RTSP
port and randomized path. It will expose an RTSP-ready callback containing:

- session ID
- RTSP port
- randomized MediaMTX path

The server handles that callback only when publishing is armed. It attempts the
UPnP mapping, builds `rtsp://<external-ip>:<port>/<path>`, and supplies the
result back to OBS status. A failed mapping leaves the local RTSP URL available
and reports the failure without stopping OBS recording or HLS output.

The server owns at most one public RTSP mapping. Installing a new mapping first
closes the previous owned mapping. All OBS shutdown and completion paths close
the current mapping.

### MediaMTX Read Access

The published path is randomized per session and remains RTSP/TCP-only.
MediaMTX read permission must allow an external client to reach that path.
Publishing remains restricted to loopback with per-session credentials.

The public path includes the existing cryptographically generated session ID.
No stable public RTSP path is introduced. The path disappears when the
session's MediaMTX process exits.

### URL Selection

For real-time mode:

1. Prefer the UPnP external IPv4 URL after mapping succeeds.
2. Fall back to the configured advertised LAN or Tailscale host when UPnP is
   unavailable.
3. Always use the `rtsp://` scheme.

For HLS-family modes, existing HTTP and Cloudflare URL selection is unchanged.

## UI

Remove the dedicated `obsRtspt` block and its special copy handler.

When real-time mode is active and ready:

- `shareURL` contains the RTSP URL.
- `shareURLLabel` is `RTSP TCP URL`.
- The existing shared URL box and existing copy behavior are used.
- The OBS latency selector remains in its current location.

This avoids a mode-specific layout and prevents long RTSP URLs from expanding
the latency-control row.

The external-publication status reports one of:

- RTSP TCP published through UPnP with the external IPv4 address.
- RTSP TCP available only on LAN/Tailscale.
- UPnP mapping failed with the router error.

## CGNAT Handling

An address returned by the router is rejected as globally public when it is
private, loopback, link-local, unspecified, multicast, or within
`100.64.0.0/10`.

If the router reports such an address, ImagePadServer treats the mapping as
non-public, closes it, keeps the LAN URL, and reports that CGNAT or upstream NAT
prevents direct RTSP publication.

## Error Handling

- UPnP discovery or mapping failure does not terminate the OBS session.
- Mapping deletion is best-effort during shutdown, but its result is recorded
  for tests and logs.
- Repeated stop calls do not delete unrelated router mappings.
- An old session callback cannot replace or clear a newer session's mapping.
- MediaMTX startup failure cannot leave a UPnP mapping because mapping starts
  only after MediaMTX reports ready.

## Testing

### UPnP Unit Tests

- Successful mapping returns an owned handle with external IP and port.
- `Close` sends `DeletePortMapping` once.
- Repeated `Close` calls are safe.
- Private and CGNAT external addresses are rejected for public advertisement.

### OBS and Server Tests

- MediaMTX remains TCP-only.
- Player URL uses `rtsp://`, not `rtspt://`.
- RTSP capability keeps the persisted `rtspt` mode ID.
- A successful mapping changes OBS/share status to the external RTSP URL.
- A failed mapping preserves a LAN RTSP URL.
- Stream completion, stop, restart, and mode change close the owned mapping.
- A stale callback cannot replace a newer session mapping.

### UI Tests

- No dedicated RTSP URL block remains.
- Real-time mode uses the normal share URL and label.
- Existing HLS share URL behavior is unchanged.

## Acceptance

- PC and Android VRChat receive a standard `rtsp://` URL.
- Media payload transport is TCP-only.
- A router mapping exists only during an actively published RTSP session.
- The displayed URL uses a globally routable external IPv4 address when UPnP
  succeeds.
- The URL appears in the normal share URL position without layout breakage.
- HLS, LHLS, and LL-HLS behavior does not regress.
