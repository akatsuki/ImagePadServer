# OBS Latency Protocol Decision

Date: 2026-06-29
Status: Accepted
Owner: GPT-5.5 / Task 0

## Decision

All four requested modes are required:

1. `hls`: standard MPEG-TS HLS.
2. `lhls`: community LHLS with `#EXT-X-PREFETCH`.
3. `llhls`: Apple LL-HLS with CMAF partial segments.
4. `rtspt`: PC-only RTSP interleaved over TCP.

Each label must correspond to the emitted protocol. A shorter ordinary HLS segment is not LHLS, and an RTSPT selection must never fall back to HLS while retaining the RTSPT label.

The producer strategy is fixed as follows:

| Mode | Producer |
|---|---|
| `hls` | Existing FFmpeg HLS muxer, MPEG-TS segments |
| `lhls` | FFmpeg DASH muxer with `-strict experimental -streaming 1 -lhls 1 -hls_playlist 1`, writing to an app-owned HTTP PUT sink |
| `llhls` | MediaMTX low-latency HLS remuxer fed by FFmpeg over RTSP/TCP |
| `rtspt` | The same MediaMTX path exposed to the client as `rtspt://` |

The custom Go CMAF/LL-HLS packager proposed previously remains rejected. MediaMTX owns CMAF part creation, timestamps, init segments, blocking reload, and playlist sessions.

## Evidence

### Installed FFmpeg

Tested executable:

```text
C:\Users\masah\AppData\Local\Microsoft\WinGet\Packages\Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe\ffmpeg-8.1.1-full_build\bin\ffmpeg.exe
```

Observed version: `8.1.1-full_build-www.gyan.dev`.

Disposable outputs were generated under:

```text
C:\Users\masah\AppData\Local\Temp\imagepad-task0-20260629
```

Standard MPEG-TS HLS and fMP4 HLS were readable by FFprobe as HLS with H.264 video and AAC audio.

### Community LHLS producer boundary

FFmpeg exposes `-lhls` on the DASH muxer, not on the HLS muxer. It requires:

```text
-strict experimental
-f dash
-streaming 1
-lhls 1
-hls_playlist 1
```

The file-output probe produced valid fMP4 HLS playlists but no `#EXT-X-PREFETCH`. This is expected from FFmpeg's `libavformat/dashenc.c`: the current code sets `prefetch_url` to `NULL` when the output protocol is `file`, because file output uses atomic rename. Non-file output passes the current segment URL to `write_hls_media_playlist`, which emits `#EXT-X-PREFETCH`.

Therefore the concrete ImagePadServer producer is:

```text
OBS RTMP input
  -> FFmpeg DASH muxer with LHLS enabled
  -> private loopback HTTP PUT endpoint owned by ImagePadServer
  -> validated init segment, current fMP4 segment, master.m3u8, media playlists
  -> public read-only /stream route
```

The PUT endpoint is not public. It accepts only a random per-session token on loopback, a fixed allowlist of generated filenames, bounded request sizes, and the methods required by FFmpeg. The public route never exposes `.tmp` files.

### Apple LL-HLS producer proof

MediaMTX v1.19.2 Windows amd64 was downloaded from the official GitHub release and verified:

```text
asset: mediamtx_v1.19.2_windows_amd64.zip
sha256: 53028b551afcc8d9ddbd56eb8406d5b31e395e5505d52e28347f211696be9345
```

The disposable flow was:

```text
FFmpeg H.264/AAC test source
  -> RTSP/TCP publish to MediaMTX
  -> MediaMTX LL-HLS endpoint
  -> HTTP playlist inspection and FFprobe read
```

The generated media playlist contained:

```text
#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,PART-HOLD-BACK=0.50000,CAN-SKIP-UNTIL=6.00000
#EXT-X-PART-INF:PART-TARGET=0.20000
#EXT-X-MAP:URI="..._init.mp4?session=..."
#EXT-X-PART:DURATION=0.20000,URI="..._part0.mp4?session=...",INDEPENDENT=YES
#EXT-X-PRELOAD-HINT:TYPE=PART,URI="..._part5.mp4?session=..."
```

FFprobe followed the MediaMTX master playlist and read H.264 video plus AAC audio as HLS. MediaMTX session query parameters must be preserved exactly; constructing a fixed `stream.m3u8` URL produced an authentication error.

### RTSPT producer and reader proof

The disposable RTSP test performed:

```text
FFmpeg test source
  -> RTSP/TCP publish to rtsp://127.0.0.1:8554/imagepad-task0
  -> MediaMTX v1.19.2
  -> RTSP/TCP read by a separate FFprobe process
```

FFprobe reported `format_name=rtsp`, H.264 video at 640x360, and AAC audio. MediaMTX logged one publishing session and one TCP reading session.

### VRChat PC evidence

Current local client logs:

```text
C:\Users\masah\AppData\LocalLow\VRChat\VRChat\output_log_2026-06-28_21-50-58.txt
C:\Users\masah\AppData\LocalLow\VRChat\VRChat\output_log_2026-06-29_09-02-47.txt
```

Observed with AVPro Video 3.3.6 and `MF-MediaEngine-Hardware`:

- The ImagePadServer HTTPS HLS URL opened at 22:34:43 and reported the hardware playback path at 22:34:45. VRChat yt-dlp resolution took 2468 ms before the open call.
- `rtsp://topaz.chat/live/algostream` opened and started in the same logged second at 09:27:20, then continued until an explicit stop at 09:33:18.
- `rtspt://topaz.chat/live/algostream` opened and started in the same logged second at 09:33:31.
- Other RTSP hosts failed, so protocol support does not guarantee arbitrary server/codec compatibility.

No current local VRChat log proves that MediaFoundation consumes community LHLS prefetches or Apple LL-HLS parts. These modes remain required, but their UI copy and status must remain experimental until Task 7 validates ImagePadServer-produced URLs in the actual client.

## Receiver Matrix

| Mode | Transport/container | VRChat PC AVPro | Browser preview | Quest | Decision |
|---|---|---|---|---|---|
| `hls` | HTTPS MPEG-TS HLS | Proven | Existing path | Compatibility baseline | Required |
| `lhls` | HTTPS fMP4 community LHLS | Base HLS fallback plausible; prefetch consumption unproven | Requires a compatible player | Unproven | Required, experimental |
| `llhls` | HTTPS CMAF Apple LL-HLS | Base HLS playback plausible; part consumption unproven | FFprobe producer path proven | Plausible ExoPlayer path, unproven in VRChat | Required, experimental |
| `rtspt` | RTSP/TCP H.264/AAC | Proven with compatible server | No browser preview | Not a Quest product path | Required, PC-only |

## Frozen Capability Contract

```go
type LatencyCapability struct {
	Mode         string `json:"mode"`
	Label        string `json:"label"`
	Transport    string `json:"transport"`
	Experimental bool   `json:"experimental"`
	Available    bool   `json:"available"`
	Selectable   bool   `json:"selectable"`
	PreviewURL   string `json:"previewURL,omitempty"`
	Message      string `json:"message,omitempty"`
}
```

### `hls`

- `transport`: `hls`.
- `experimental`: false.
- Ready only when the playlist references a complete, non-empty MPEG-TS segment.
- `previewURL`: existing public HTTPS `.m3u8` URL.

### `lhls`

- `transport`: `lhls`.
- `experimental`: true until VRChat acceptance passes.
- Ready only when the public media playlist contains `#EXT-X-PREFETCH`, its init segment exists, and at least one complete fMP4 segment is readable.
- `previewURL`: public master `.m3u8`; generated relative URIs must remain valid through the public route.
- Failure: no fallback to ordinary HLS under the LHLS label.

### `llhls`

- `transport`: `llhls`.
- `experimental`: true until VRChat acceptance passes.
- Ready only when the MediaMTX path is ready and a media playlist contains `#EXT-X-PART`, `#EXT-X-SERVER-CONTROL`, `#EXT-X-MAP`, and `#EXT-X-PRELOAD-HINT`.
- `previewURL`: MediaMTX public master playlist exposed through the app/tunnel. Session query strings and blocking reload query parameters must be forwarded unchanged.
- Failure: no fallback to short HLS under the LL-HLS label.

### `rtspt`

- `transport`: `rtspt`.
- `experimental`: false for PC after ImagePadServer-produced URL acceptance passes.
- Ready only when MediaMTX reports the path and an independent RTSP probe reads H.264 and AAC.
- `previewURL`: `rtspt://<advertised-host>:<rtsp-port>/<session-path>`.
- Browser preview is empty.
- Failure: no HLS fallback.

## HTTP and Process Rules

- Public playlists: `application/vnd.apple.mpegurl`.
- MPEG-TS segments: `video/mp2t`.
- fMP4 init/segment/part files: `video/mp4`.
- Live responses: `Cache-Control: no-store, max-age=0`.
- The LHLS PUT sink listens on loopback only and requires a random per-session token.
- Public reads cannot access upload endpoints or temporary files.
- LL-HLS proxying preserves query strings, range requests, blocking reload, and cancellation.
- Shutdown order: stop FFmpeg publisher, wait for path removal, then stop only the owned MediaMTX process.

## Legacy Mapping

```text
auto   -> hls
normal -> hls
low    -> lhls
ultra  -> llhls
```

## Rejected Approaches

### Custom Go LL-HLS packager

Rejected because playlist tags alone do not create valid CMAF parts. MediaMTX already produced valid parts, init segments, sessionized playlists, and blocking reload metadata in the Task 0 probe.

### Calling short HLS `LHLS`

Rejected because `-hls_time` does not implement `#EXT-X-PREFETCH`.

### Direct file output for FFmpeg LHLS

Rejected because FFmpeg intentionally suppresses the prefetch URL for file protocol output. The app-owned HTTP PUT sink is required.

### Protocol-label fallback

Rejected. A failed LHLS, LL-HLS, or RTSPT start returns unavailable/error for that mode instead of publishing a different transport under the selected label.

## Follow-up Acceptance Gates

- `hls`: artifact readiness, server MIME/cache tests, browser smoke, VRChat playback.
- `lhls`: loopback PUT integration, prefetch tag/artifact checks, compatible browser check, VRChat playback, reconnect, and latency measurement.
- `llhls`: MediaMTX lifecycle/proxy tests, part-tag/artifact checks, FFprobe/browser check, VRChat playback, reconnect, and latency measurement.
- `rtspt`: process ownership, port conflict, publish/read integration, and VRChat playback against the ImagePadServer-produced URL.
