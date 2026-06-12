# BrowserRelayStreamer Integration API

This document describes how an external BrowserRelayStreamer app should discover
ImagePadServer, pair with it, and request OBS RTMP relay connection details.

The integration is intentionally split into two layers:

- Discovery only tells BrowserRelayStreamer where ImagePadServer is.
- Authentication is required before ImagePadServer returns OBS stream keys.

Never treat LAN discovery as authentication.

## 1. LAN Discovery

ImagePadServer listens for UDP discovery probes on port `45849`.

BrowserRelayStreamer sends this payload to the LAN broadcast address:

```text
IMAGEPADSERVER_DISCOVER_V1
```

ImagePadServer replies with JSON:

```json
{
  "app": "ImagePadServer",
  "version": "1.2.2",
  "baseURL": "http://192.168.1.20:8080/",
  "healthPath": "/healthz",
  "obsRelayPath": "/api/obs/relay-config",
  "authRequired": true,
  "discoveryPort": 45849,
  "protocolVersion": 1
}
```

Fields:

- `baseURL`: HTTP base URL for follow-up API requests.
- `obsRelayPath`: endpoint used after authentication to get RTMP details.
- `authRequired`: always expect this to be `true`.
- `protocolVersion`: currently `1`.

The discovery response must not contain the OBS stream key, admin token, or any
long-lived credential.

## 2. Initial Pairing

Initial pairing is for first-time trust between the two apps.

The target flow is:

1. BrowserRelayStreamer discovers ImagePadServer over UDP.
2. BrowserRelayStreamer requests pairing.
3. ImagePadServer shows a large 4-digit PIN in its UI.
4. The user enters that PIN into BrowserRelayStreamer.
5. BrowserRelayStreamer proves knowledge of the PIN.
6. ImagePadServer issues a device credential scoped to OBS relay access.

### Request Pairing

```http
POST /api/pairing/request
Content-Type: application/json
```

Request:

```json
{
  "clientName": "BrowserRelayStreamer",
  "deviceName": "Bedroom PC"
}
```

Response:

```json
{
  "pairingId": "pair_...",
  "nonce": "base64url-random",
  "expiresAt": "2026-06-03T12:34:56Z",
  "pinDigits": 4
}
```

ImagePadServer displays a large 4-digit PIN on screen. The PIN should expire
quickly, preferably in 60 to 120 seconds. Failed confirmation attempts should be
rate-limited and capped.

### Confirm Pairing

BrowserRelayStreamer computes:

```text
proof = HMAC-SHA256(
  key = pin,
  message = pairingId + "\n" + nonce + "\n" + clientName + "\n" + deviceName
)
```

The proof is encoded as lowercase hex.

```http
POST /api/pairing/confirm
Content-Type: application/json
```

Request:

```json
{
  "pairingId": "pair_...",
  "clientName": "BrowserRelayStreamer",
  "deviceName": "Bedroom PC",
  "proof": "..."
}
```

Response:

```json
{
  "clientId": "brs_...",
  "clientSecret": "base64url-256bit-secret",
  "serverBaseURL": "http://192.168.1.20:8080/",
  "scope": "obs-relay",
  "createdAt": "2026-06-03T12:35:01Z"
}
```

BrowserRelayStreamer stores `clientId`, `clientSecret`, and `serverBaseURL` in
its local app data. The `clientSecret` is shown only once.

## 3. Returning Device Authentication

After the first successful pairing, BrowserRelayStreamer should authenticate
with a signed request. No PIN is needed.

For each authenticated request, send:

```text
X-ImagePad-Client-Id: brs_...
X-ImagePad-Timestamp: 2026-06-03T12:40:00Z
X-ImagePad-Nonce: base64url-random
X-ImagePad-Signature: base64url-hmac
```

Signature message:

```text
METHOD + "\n" +
PATH_WITH_QUERY + "\n" +
TIMESTAMP + "\n" +
NONCE + "\n" +
SHA256_HEX(BODY)
```

Signature:

```text
HMAC-SHA256(clientSecret, signatureMessage)
```

Server validation rules:

- Reject timestamps outside a short clock window, for example 5 minutes.
- Reject reused nonce values for the same `clientId` within the clock window.
- Reject revoked devices.
- Enforce `scope == "obs-relay"`.

## 4. Request OBS Relay Config

Once authenticated, BrowserRelayStreamer requests OBS RTMP connection details.

```http
POST /api/obs/relay-config
X-ImagePad-Client-Id: brs_...
X-ImagePad-Timestamp: ...
X-ImagePad-Nonce: ...
X-ImagePad-Signature: ...
```

Response:

```json
{
  "ok": true,
  "serverAddress": "rtmp://192.168.1.20:1935/live",
  "streamKey": "base64url-secret",
  "rtmpURL": "rtmp://192.168.1.20:1935/live/base64url-secret",
  "videoPlayerEnabled": true,
  "listening": true,
  "publishing": true,
  "latency": {
    "mode": "auto",
    "label": "auto",
    "target": "10s",
    "segmentSeconds": "2",
    "listSize": "5",
    "reencode": true
  }
}
```

Calling this endpoint prepares ImagePadServer for relay input:

- Video player mode is enabled.
- The OBS RTMP receiver is started.
- Publishing is armed so the incoming relay stream becomes the current HLS
  output.

BrowserRelayStreamer should pass `rtmpURL` to FFmpeg or its streaming backend.

## 5. CLI Helper

ImagePadServer also exposes a command-line helper for local or LAN use.

Local machine:

```powershell
imagepadserver.exe obs-relay-config --format json --output relay.json
```

LAN discovery with a current admin token:

```powershell
imagepadserver.exe obs-relay-config --discover --token <token> --output relay.json
```

RTMP URL only:

```powershell
imagepadserver.exe obs-relay-config --discover --token <token> --format rtmp-url
```

The helper currently supports:

- `--discover`
- `--server <base-url>`
- `--token <admin-token>`
- `--client-id <relay-client-id>`
- `--client-secret <relay-client-secret>`
- `--format json|env|rtmp-url`
- `--output <file>`
- `--timeout-ms <milliseconds>`

On Windows GUI builds, prefer `--output` because stdout may not be visible to a
launcher process.

## 6. Device Management Requirements

ImagePadServer should expose a UI section for paired devices.

Recommended stored fields:

- `clientId`
- `clientSecret` in the app settings file
- `deviceName`
- `scope`
- `createdAt`
- `lastSeenAt`
- `revokedAt`

Recommended user actions:

- View paired BrowserRelayStreamer devices.
- Rename a device.
- Revoke a device.
- Clear expired pairing requests.

Device credentials must not grant full admin access. They should only authorize
OBS relay operations, starting with `/api/obs/relay-config`.

## 7. Security Notes

- UDP discovery is public to the local network. It is not authentication.
- The 4-digit PIN is only a short-lived pairing confirmation.
- Do not derive the OBS stream key directly from the PIN.
- Do not return the admin token to BrowserRelayStreamer.
- Do not allow device credentials to call general admin APIs.
- Keep the existing admin-token flow for browser UI and mobile QR access.
