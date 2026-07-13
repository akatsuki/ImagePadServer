# Playlist RTSP Baseline Acceptance (#20A)

## Purpose

#20A is the protocol promotion gate that must pass before #19 hybrid playlist
work starts. It validates the existing radio copy lane; it does not implement,
enable, or approve hybrid delivery.

The acceptance is local and deterministic. It does not download or install
tools, access the internet, discover or modify a router, launch VRChat or OBS,
or infer receiver behavior from configuration.

## Required environment

Set every path explicitly to a pinned local regular file. Relative paths and
PATH-only discovery are rejected.

```powershell
$env:IMAGEPAD_RTSP_ACCEPTANCE = "1"
$env:IMAGEPAD_MEDIAMTX = "C:\absolute\pinned\mediamtx.exe"
$env:IMAGEPAD_FFMPEG = "C:\absolute\pinned\ffmpeg.exe"
$env:IMAGEPAD_FFPROBE = "C:\absolute\pinned\ffprobe.exe"
$env:IMAGEPAD_RTSP_ACCEPTANCE_OUTPUT = "C:\absolute\evidence\playlist-rtsp-baseline.json"

$producerArgs = @(
  "./internal/server",
  "-run", "^TestMusicRadioRTSPBaselineAcceptance$",
  "-count=1", "-v", "-timeout=150s"
)
go test @producerArgs
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# Required promotion decision. This command never skips.
go test -tags=rtspacceptance ./internal/server `
  -run '^TestMusicRadioRTSPPromotionGate$' `
  -count=1 -v
```

Both commands are required. The producer can write `status: blocked` and skip
when its environment is unavailable. The second command reads that exact JSON
and exits nonzero unless it is an accepted #20A record with a valid trace.
Before checking its environment, the producer atomically removes any prior
artifact and writes an invalidated record. Failure to invalidate or persist a
blocked record fails the producer command; stale accepted JSON is never reused.

For an interactive producer-only run, the equivalent command is:

```powershell
go test ./internal/server `
  -run '^TestMusicRadioRTSPBaselineAcceptance$' `
  -count=1 -v -timeout=150s
```

The test has an internal 135-second deadline, leaving teardown margin inside
the required 150-second process timeout. It uses polling, process completion,
packet arrival, and context deadlines. No fixed sleep establishes correctness.

## What the binary test does

1. Records each binary's absolute path, first version line, and SHA-256.
2. Generates two local 640x360, 30 fps, H.264/AAC MPEG-TS fixtures with the
   pinned FFmpeg.
3. Starts the real playlist radio with an empty queue and waits for the active
   fallback on a real loopback MediaMTX path.
4. Replaces UPnP with an in-memory mapping recorder. No router call occurs.
5. Captures the ready RadioManager endpoint and starts one FFprobe RTSP/TCP
   reader on its MediaMTX backend loopback URL. This is independent of the
   public `rtspUrl` / `rtspPublic` publication state and occurs only after the
   radio path is ready.
6. Keeps that same process attached
   through fallback -> track -> pause -> resume -> seek -> next -> stop.
7. Uses packet arrival and server state to bound every nonterminal transition.
8. Timestamps the stop POST at its actual dispatch, then records the reader
   exit and `RadioStatus.StoppedAt` only after that request. Intentional stop
   is terminal evidence, so it does not require a packet after the request.
9. Waits for mapping closures and verifies that FFmpeg and MediaMTX registries
   require no cleanup intervention.

The frozen stream signature is H.264 video, 640x360, yuv420p, 30/1 fps, with
AAC 48 kHz stereo audio. Evidence starts empty: FFprobe must actually report
both video and audio before any observed stream/frame signature can match.

## Acceptance rules

The persisted JSON is authoritative. #20A passes only when all of these are
true:

- `gate` is `#20A`, `status` is `accepted`, and `accepted` is `true`.
- All three binaries have unique names, absolute paths, nonempty versions, and
  valid SHA-256 values. The promotion command reopens each local regular file
  and recomputes its digest instead of trusting the JSON string. It also reruns
  each pinned binary's bounded `--version`/`-version` command and requires an
  exact match with the recorded version.
- Start/finish timestamps agree exactly with a positive recorded duration below
  135 seconds.
- One FFprobe reader remains alive until the intentional stop.
- Per-stream PTS and DTS never decrease.
- Recorded packet and stream counters equal the recomputed evidence counts, and
  at least two stream indexes are observed.
- Packet gaps, request-to-first-packet gaps, and recorded transition continuity
  gaps are each at most the fixed code limit of 5000 ms.
- JSON `maxPacketGapMillis` and `maxTransitionGapMillis` must both equal 5000.
  They are evidence assertions, never configurable limits or validator input.
- All six transitions occur once, in order, with monotonic timestamps and one
  invariant nonempty RTSP path. For every nonterminal transition, the gate finds
  the first packet at or after the request and recomputes both its arrival
  timestamp and continuity gap; claimed values must match exactly. Intentional
  stop instead records zero packet/gap values and requires both
  `stopRequestedNanos`, `readerExitedNanos`, and `radioStoppedNanos`. The
  terminal observations must be strictly after the stop request and within the
  run duration. Nanosecond offsets retain causal ordering when dispatch and
  shutdown occur in the same millisecond. Pre-stop or missing terminal evidence
  is rejected.
- The RTSP path remains ready and unchanged before stop.
- No reconnect, codec-signature change, stderr error, or server error occurs.
- Mapping event counts match cleanup counters, and every synthetic create has
  exactly one later close for the same protocol and ports.
- The radio and reader exit, and registry cleanup kills zero processes.
- Total duration is below 135 seconds.

A successful producer `go test` process exit is not enough: Go reports an
all-skipped package as PASS. The required build-tagged promotion command fails
for `SKIP`, `status: blocked`, a missing metrics file, malformed trace evidence,
or `accepted: false`; none can promote #19.

## Deterministic parser fixtures

The default test suite does not need binaries. It validates a known-good trace
and rejects one fixture for each required fault:

```powershell
go test ./internal/server `
  -run '^TestMusicRadioRTSPAcceptance(FaultFixturesAreRejected|ValidFixtureIsAccepted)$' `
  -count=1
```

Fault fixtures cover nonmonotonic timestamps, signature change, missing audio,
missing video, path disappearance, reconnect, packet timeout, and a separate
transition-only timeout. They are stored under `internal/server/testdata/` and
do not invoke a process or network socket. Tool-version tests also reject empty
or whitespace-only version output.

## Metrics record

The JSON record contains:

- binary path, version, and SHA-256 evidence;
- the frozen stream contract and every observed signature;
- packet and stream counts plus packet arrival, stream index, PTS, and DTS;
- request time, first-packet time, gap, and path for every transition; the stop
  transition additionally records post-request reader-exit and radio-stopped
  timestamps instead of a post-stop packet;
- reader stderr and server errors;
- reconnect and path-disappearance counts;
- synthetic mapping create/close lifecycle;
- reader, radio, FFmpeg, and MediaMTX cleanup results.

The promotion validator requires every field above, cross-checks counters
against their arrays, pairs mapping lifecycle events, and rejects an
`accepted: true` record when any authoritative evidence is absent or
inconsistent. Required JSON field presence is checked before typed values, so
omitting an authoritative zero or empty array is distinct from explicitly
recording `0`, `""`, or `[]`. This includes `readerStderr`, `serverErrors`,
`trace.errors`, `reconnects`, and `pathDisappearances`.

Writes are atomic in the selected evidence directory. When the output path is
valid but another prerequisite is unavailable, the test writes
`status: blocked` and `accepted: false` before skipping. Failure to persist that
blocked evidence is a test failure, not a skip.

## Current workstation limitation (2026-07-12)

The binary acceptance was not run during #20A implementation:

- `IMAGEPAD_MEDIAMTX`, `IMAGEPAD_FFMPEG`, and `IMAGEPAD_FFPROBE` are unset.
- FFmpeg 8.1.1 and FFprobe are present on PATH, but PATH discovery is not an
  accepted pin and was not used by the binary test.
- MediaMTX is not present on PATH.
- No downloader, installer, internet access, router, VRChat, or OBS fallback
  was used to turn this blocked environment into a pass.

The observed test result is therefore `SKIP` with
`BLOCKED #19 PROMOTION GATE`; it is not #20A acceptance.
