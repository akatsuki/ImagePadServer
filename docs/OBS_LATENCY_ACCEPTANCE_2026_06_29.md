# OBS Latency Acceptance — 2026-06-29

Acceptance record for the four OBS latency transports (plan
`docs/superpowers/plans/2026-06-29-obs-latency-presets-llhls.md`, Task 7).
Design record: `docs/OBS_LATENCY_PROTOCOL_DECISION.md`.

## Environment

| Item | Value |
|---|---|
| OS | Windows 11 Pro (10.0.26200) |
| FFmpeg | 8.1.1 (bundled, pinned) |
| MediaMTX | v1.19.2 (pinned, SHA-256 verified) |
| Date | 2026-06-29 |

## Scope and honesty note

This record covers the **automated, protocol-level acceptance** that can be run
without a VRChat client or VR headset. The **VRChat PC playback**, **in-world
end-to-end latency measurement**, and **Quest** items genuinely require the
operator's running VRChat/VR environment and human observation, so they are
recorded here as **PENDING** with a concrete procedure rather than claimed.

Per the plan, no latency figure is inferred from configured segment/part
durations, and **LHLS and LL-HLS remain experimental** because VRChat
*consumption* of prefetch/parts is not yet evidenced (only protocol emission is).

## 1. Automated protocol-level acceptance — PASS

Run with real binaries (opt-in, gated):

```powershell
$env:IMAGEPAD_MEDIAMTX_TEST=1; $env:IMAGEPAD_LHLS_FFMPEG_TEST=1
$env:IMAGEPAD_FFMPEG="<pinned ffmpeg>"
go test ./internal/obsrtmp -run 'TestLHLSProducerArtifacts|TestMediaMTXRuntimeBootsRealBinary|TestMediaMTXPublishAndLLHLSReady' -v
```

Result (2026-06-29): all PASS.

| Transport | Evidence (executed) | Result |
|---|---|---|
| HLS | Standard MPEG-TS HLS path (unchanged, covered by existing `internal/video` + server HLS tests). | PASS |
| LHLS | `TestLHLSProducerArtifacts`: real FFmpeg DASH/LHLS streamed to the loopback sink emits a media playlist advertising `#EXT-X-PREFETCH`, a non-empty `#EXT-X-MAP` init segment, and FFprobe-readable fMP4 segments; readiness gate fires only on live prefetch state; the DASH `.mpd` and temp files are never publicly readable. | PASS |
| LL-HLS | `TestMediaMTXRuntimeBootsRealBinary`: pinned MediaMTX boots healthy with the rendered per-session config. `TestMediaMTXPublishAndLLHLSReady`: an H.264/AAC RTSP/TCP publish using the per-session credential reaches an LL-HLS media playlist carrying all required tags (`#EXT-X-SERVER-CONTROL`, `#EXT-X-PART-INF`, `#EXT-X-MAP`, `#EXT-X-PART`, `#EXT-X-PRELOAD-HINT`). | PASS |
| RTSPT | Same test confirms the MediaMTX path becomes ready via the API for the RTSP/TCP read path; `rtspt://<advertised-host>:<port>/<session-path>` is generated only after readiness. | PASS |

Proxy semantics (`TestMediaMTXProxyPreservesSemantics`): method, session/
blocking-reload query (`_HLS_msn`/`_HLS_part`), `Range`, status code, and
`Content-Type` are preserved through the public proxy; cancellation returns 502
without hanging.

## 2. Failure-behavior acceptance — PASS (automated)

Plan Step 4 behaviors, verified by unit tests in `internal/obsrtmp`:

| Failure | Behavior | Test |
|---|---|---|
| MediaMTX port conflict / early exit | `start` returns an error ("exited before becoming healthy"); no false-ready. | `TestMediaMTXStartFailsOnEarlyExit` |
| MediaMTX never healthy (e.g. missing/invalid) | `start` times out and tears down the owned process. | `TestMediaMTXStartTimesOutAndTearsDown` |
| Sidecar crash mid-session | Surfaced via the owned-process exit channel. | `TestMediaMTXWaitObservesCrash` |
| Graceful vs forced stop | Graceful first, escalates to kill after grace; never touches an unrelated process. | `TestMediaMTXGracefulStopAvoidsKill`, `...ForcedStopEscalatesToKill`, `...StopOnlyTouchesOwnedProcess` |
| LHLS sink hostile input | Non-loopback 403, wrong token 403, bad method 405, disallowed/traversal name 400, oversize 413. | `TestLHLSSink*` |
| No silent transport change | Each capability advertises only its own transport; readiness gates hide the public URL until the *selected* transport is actually ready. | `TestOBSLatencyAliasesAndCapabilitySurface`, readiness gates |

FFmpeg crash and network disconnect manifest as the publisher process exiting,
which ends the session through the existing `errCh` path (shared with the HLS
path); ordered shutdown then stops the owned MediaMTX after the publisher is gone.

## 3. PENDING — operator/VRChat-environment items

These require a running OBS encoder, the VRChat client, and (for Quest) a
headset. They could not be executed from the automation environment.

### 3a. VRChat PC playback of all four ImagePadServer URLs (plan Step 2)

Procedure:
1. Enable OBS mode; start the receiver. In OBS, stream to the shown RTMP URL/key.
2. For each mode (HLS, LHLS, LL-HLS, RTSPT) selected in the UI, copy the public
   URL (RTSPT: the `rtspt://` URL).
3. In VRChat PC (AVPro), load each URL. Record: first frame appears, audio/video
   sync, 5-minute stability, reconnect after a deliberate OBS stop/start.
4. Preserve AVPro API / playback-path logs and note whether the low-latency
   feature is *consumed* (LHLS prefetch / LL-HLS parts) or AVPro falls back to
   full segments. Use ImagePadServer's own output, not third-party servers.

### 3b. End-to-end latency measurement (plan Step 3)

Use an OBS millisecond-clock source compared against the VRChat-rendered frame.
Record median, worst observed, startup time, and reconnect time per mode. Do not
infer latency from configured durations.

### 3c. Quest acceptance

Separate pass on a standalone headset (HLS-family only; RTSPT is PC-only).

## 4. Decision (plan Step 5)

- HLS: accepted at the protocol level; unchanged from prior releases.
- **LHLS, LL-HLS: remain `Experimental = true`** until 3a/3b provide VRChat
  consumption evidence. No experimental flag is flipped on protocol evidence
  alone.
- RTSPT: PC-only; protocol/read-path verified; in-VRChat acceptance pending 3a.
- Quest: pending (3c), tracked separately.
