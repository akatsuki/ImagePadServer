# Playlist Streaming Final Acceptance (#20B)

## Scope

#20B is a test-only promotion gate for the program pipeline. It does not alter
playlist, copy-lane, MediaMTX, or FFmpeg production behavior. It runs the five
delivery profiles against pinned local binaries and writes one atomic JSON
record. The build-tagged gate rejects a missing, blocked, partial, malformed,
or failed record.

| Profile | GOP | Required protocol evidence |
| --- | ---: | --- |
| `hls-high` | 120 | HLS ready with `fmp4` artifacts |
| `hls` | 30 | HLS ready with `fmp4` artifacts |
| `rtsp-low` | 60 | continuous RTSP/TCP receiver |
| `rtsp-ultra` | 30 | continuous RTSP/TCP receiver |
| `rtsp-realtime` | 15 | continuous RTSP/TCP receiver |

Every run captures an actual receiver copy and probes it with the supplied
FFprobe. It persists the continuous receiver packet timeline, capture path,
file size, and SHA-256 for each profile. Promotion reopens every capture,
rechecks its digest and size, and reprobes its codec and GOP. It requires H.264
video, AAC audio, and the profile GOP. A packet count without the corresponding
continuous timestamp evidence and durable capture cannot promote. It also
rejects non-monotonic per-stream PTS/DTS, a reconnect, reader stderr, RTSP path
loss, HLS readiness loss, unexpected child-process cleanup, or incomplete
UPnP-mapping cleanup. HLS readiness is MediaMTX's artifact-level check, not a
configuration inference: it verifies the master/media playlists, init segment,
and a media segment for the active profile.

## Required pins

Use absolute local regular-file paths. The test records each path, first
version line, and SHA-256 using the same inspection as #20A.

```powershell
$env:IMAGEPAD_FINAL_ACCEPTANCE = "1"
$env:IMAGEPAD_MEDIAMTX = "C:\absolute\pinned\mediamtx.exe"
$env:IMAGEPAD_FFMPEG = "C:\absolute\pinned\ffmpeg.exe"
$env:IMAGEPAD_FFPROBE = "C:\absolute\pinned\ffprobe.exe"
$env:IMAGEPAD_FINAL_ACCEPTANCE_OUTPUT = "C:\absolute\evidence\playlist-final-acceptance.json"

go test ./internal/server `
  -run '^TestMusicRadioFinalAcceptance$' `
  -count=1 -v -timeout=12m

go test -tags=finalacceptance ./internal/server `
  -run '^TestMusicRadioFinalAcceptancePromotionGate$' `
  -count=1 -v
```

The ordinary matrix run uses 12 seconds of continuous receiver evidence per
profile, plus readiness and teardown time. It is not promotable and cannot be
used by the final gate.

## Five-profile soak

The actual soak is explicit and is never silently converted into a short run.
With `IMAGEPAD_FINAL_ACCEPTANCE_SOAK=1`, `IMAGEPAD_FINAL_ACCEPTANCE_SOAK_SECONDS`
is the required duration of every profile, not a total divided across profiles.
Its default is exactly 1800 seconds for each of the five profiles, so the matrix
requires at least 9000 seconds of receiver continuity before readiness and
teardown. Values below 1800 fail the run.

```powershell
$env:IMAGEPAD_FINAL_ACCEPTANCE = "1"
$env:IMAGEPAD_FINAL_ACCEPTANCE_SOAK = "1"
$env:IMAGEPAD_FINAL_ACCEPTANCE_SOAK_SECONDS = "1800"
$env:IMAGEPAD_MEDIAMTX = "C:\absolute\pinned\mediamtx.exe"
$env:IMAGEPAD_FFMPEG = "C:\absolute\pinned\ffmpeg.exe"
$env:IMAGEPAD_FFPROBE = "C:\absolute\pinned\ffprobe.exe"
$env:IMAGEPAD_FINAL_ACCEPTANCE_OUTPUT = "C:\absolute\evidence\playlist-final-soak.json"

go test ./internal/server `
  -run '^TestMusicRadioFinalAcceptance$' `
  -count=1 -v -timeout=3h

go test -tags=finalacceptance ./internal/server `
  -run '^TestMusicRadioFinalAcceptancePromotionGate$' `
  -count=1 -v
```

While every profile's FFmpeg receiver capture and MediaMTX path are alive, the
runner records repeated PowerShell process CPU and working-set samples for the
owned capture FFmpeg child, FFprobe reader, and MediaMTX child, plus available
Windows GPU performance-counter data. It captures those PID lists directly from
the owned process handles and queries PowerShell only by those IDs; it never
uses a global process-name scan. Promotion requires at least three samples per
profile, each within that profile's receiver capture timestamps. Each sample
must contain the mandatory raw JSON fields and process records whose names and
IDs exactly match the recorded FFmpeg, reader, and MediaMTX PID lists; the
combined CPU record cannot contain an extra process. Empty, placeholder,
mismatched, missing, pre-start, post-stop, and probe-error records fail the
gate. The JSON also contains binary identities, per-profile receiver
packet/codec/GOP evidence, HLS and RTSP readiness, receiver stderr,
reconnect/path-loss counts, and timestamps.

Promotion requires `requested: true`, at least 1800 seconds for every profile,
and at least 9000 seconds across the matrix. Every profile and receiver capture
must meet the 1800-second floor without a tolerance reduction, and capture
start/end timestamps must cover its declared profile soak. Its ffprobe duration
must also cover that soak; the gate tolerance is used only when comparing the
recorded and independently probed capture durations.
A successful short matrix is evidence only for the ordinary matrix; it cannot
pass final promotion.

If the producer lacks an opt-in flag, an output path, or valid pins, it writes
`status: "blocked"` when the output path is valid and logs the exact blocker as
`BLOCKED #20B FINAL ACCEPTANCE`. The subsequent promotion command fails; a
blocked or skipped producer cannot promote the playlist pipeline.
