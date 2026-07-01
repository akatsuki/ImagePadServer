# OBS HLS, LHLS, LL-HLS, and RTSPT Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement four truthful OBS latency modes: standard HLS, community LHLS, Apple LL-HLS, and PC-only RTSPT.

**Architecture:** Keep FFmpeg as the OBS RTMP receiver and encoder. Standard HLS uses the current MPEG-TS HLS muxer. LHLS uses FFmpeg's experimental DASH muxer through a private loopback HTTP PUT sink so `#EXT-X-PREFETCH` is actually emitted. LL-HLS and RTSPT share an owned MediaMTX sidecar fed by FFmpeg over RTSP/TCP.

**Tech Stack:** Go 1.25, FFmpeg 8.x, MediaMTX, MPEG-TS HLS, community LHLS/fMP4, Apple LL-HLS/CMAF, RTSP over TCP, embedded HTTP handlers and UI.

**Decision record:** `docs/OBS_LATENCY_PROTOCOL_DECISION.md`

---

## Model Assignment

| Task | Assignment | Reason |
|---|---|---|
| Task 0 protocol/runtime gate | **[GPT-5.5, complete]** | Producer selection and runtime proof |
| Task 1 canonical values and aliases | **[GPT-5.4-mini]** | Bounded mechanical change |
| Task 2 MediaMTX tool management | **[GPT-5.5]** | Supply chain, install concurrency, release behavior |
| Task 3 LHLS PUT sink and publisher | **[GPT-5.5]** | Streaming HTTP, security, partial artifact lifecycle |
| Task 4 LL-HLS and RTSPT sidecar | **[GPT-5.5]** | Process ownership, proxy semantics, readiness |
| Task 5 API/UI capability surface | **[GPT-5.4-mini after 5.5 contract]** | Safe after status shapes are fixed |
| Task 6 docs and unit verification | **[GPT-5.4-mini]** | Bounded documentation and test execution |
| Task 7 browser/VRChat acceptance | **[GPT-5.5]** | Environment-dependent user-visible correctness |

---

### Task 0: Prove producers and freeze contracts **[GPT-5.5, complete]**

**Files:**
- Created: `docs/OBS_LATENCY_PROTOCOL_DECISION.md`
- Modified: this plan

- [x] **Step 1: Record the receiver matrix**

Recorded all four modes against VRChat PC AVPro, browser preview, and Quest constraints. LHLS and LL-HLS remain required and experimental until actual VRChat acceptance.

- [x] **Step 2: Probe FFmpeg and select concrete producers**

Verified FFmpeg 8.1.1. HLS uses the HLS muxer. LHLS uses the DASH muxer with `-strict experimental -streaming 1 -lhls 1 -hls_playlist 1` and requires non-file output.

- [x] **Step 3: Prove MediaMTX LL-HLS and RTSP/TCP paths**

Verified MediaMTX v1.19.2 asset SHA-256. Its LL-HLS output produced 200 ms CMAF parts, init map, server control, and preload hint; FFprobe read H.264/AAC through the master playlist. A separate RTSP/TCP publish/read probe also passed.

- [x] **Step 4: Reconcile actual VRChat evidence**

Current logs prove ImagePadServer HLS playback and third-party `rtsp://`/`rtspt://` playback through AVPro MediaFoundation. They do not yet prove that VRChat consumes LHLS prefetches or LL-HLS parts, so those claims are deferred to Task 7 without removing the required modes.

- [x] **Step 5: Freeze capability and failure semantics**

The decision record defines producer ownership, readiness, MIME/cache rules, proxy behavior, legacy aliases, and the prohibition on transport-label fallback.

---

## Canonical Modes

```go
const (
	LatencyModeHLS   = "hls"
	LatencyModeLHLS  = "lhls"
	LatencyModeLLHLS = "llhls"
	LatencyModeRTSPT = "rtspt"
)
```

```go
var legacyLatencyModeAliases = map[string]string{
	"auto":   LatencyModeHLS,
	"normal": LatencyModeHLS,
	"low":    LatencyModeLHLS,
	"ultra":  LatencyModeLLHLS,
}
```

---

### Task 1: Normalize mode storage **[GPT-5.4-mini]**

**Files:**
- Modify: `internal/obsrtmp/manager.go`
- Modify: `internal/obsrtmp/manager_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Write failing normalization tests**

Cover all canonical and legacy values, whitespace, case, and unknown values. Unknown values fall back to `hls`.

- [ ] **Step 2: Verify failure**

```powershell
rtk go test ./internal/obsrtmp -run TestNormalizeLatencyProfile -count=1 -v
```

- [ ] **Step 3: Add constants, aliases, labels, and capability fields**

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

Labels:

- `通常遅延（HLS）`
- `低遅延（LHLS, 実験）`
- `超低遅延（LL-HLS, 実験）`
- `リアルタイム（RTSPT, PC専用）`

- [ ] **Step 4: Run focused tests and commit**

```powershell
rtk go test ./internal/obsrtmp ./internal/server -run 'Latency|OBS' -count=1
git add internal/obsrtmp/manager.go internal/obsrtmp/manager_test.go internal/server/server.go internal/server/server_test.go
git commit -m "feat: normalize OBS latency modes"
```

---

### Task 2: Manage MediaMTX safely **[GPT-5.5]**

**Files:**
- Create: `internal/obsrtmp/mediamtx_tool.go`
- Create: `internal/obsrtmp/mediamtx_tool_test.go`

- [ ] **Step 1: Write tool resolution and checksum tests**

Resolution order: `IMAGEPAD_MEDIAMTX`, bundled versioned path, managed install. Reject missing executables and checksum mismatches.

- [ ] **Step 2: Pin and document a release asset**

Do not call GitHub `latest` at runtime. Task 0 tested v1.19.2 Windows amd64 with SHA-256 `53028b551afcc8d9ddbd56eb8406d5b31e395e5505d52e28347f211696be9345`; re-verify before release pinning.

- [ ] **Step 3: Implement atomic installation**

Download to a temporary path, enforce size limits, verify SHA-256, extract only `mediamtx.exe`, `mediamtx.yml`, and `LICENSE`, then atomically rename into a versioned app-data directory.

- [ ] **Step 4: Test concurrency and failures**

Cover interrupted download, invalid zip, checksum mismatch, concurrent callers, valid existing install, and read-only destination.

- [ ] **Step 5: Run and commit**

```powershell
rtk go test ./internal/obsrtmp -run MediaMTX -count=1
git add internal/obsrtmp/mediamtx_tool.go internal/obsrtmp/mediamtx_tool_test.go
git commit -m "feat: manage MediaMTX runtime"
```

---

### Task 3: Implement community LHLS output **[GPT-5.5]**

**Files:**
- Create: `internal/obsrtmp/lhls_sink.go`
- Create: `internal/obsrtmp/lhls_sink_test.go`
- Modify: `internal/obsrtmp/manager.go`
- Modify: `internal/obsrtmp/manager_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/media_paths.go`

- [x] **Step 1: Write failing sink security tests**

Verify loopback-only binding, random per-session token, method allowlist, generated filename allowlist, path traversal rejection, body limits, cancellation, and cleanup. The sink must never share the public route or admin authentication model.

- [x] **Step 2: Write failing producer artifact tests**

Run FFmpeg against the sink and require:

- `master.m3u8` references the media playlist;
- media playlist includes `#EXT-X-PREFETCH`;
- `#EXT-X-MAP` init file exists and is non-empty;
- at least one completed fMP4 segment is FFprobe-readable;
- current prefetch target is accessible while FFmpeg writes it;
- public reads cannot access temporary or unknown files.

- [x] **Step 3: Implement the private HTTP PUT sink**

Accept only loopback requests containing the current random token. Map generated names into a per-session directory. Stream current segment bytes without buffering the entire request in memory. Publish manifests atomically and preserve cancellation.

- [x] **Step 4: Add the exact FFmpeg DASH/LHLS output**

Required options:

```text
-strict experimental
-f dash
-streaming 1
-lhls 1
-hls_playlist 1
-seg_duration 1
```

Set explicit video/audio bitrates so the master playlist contains usable stream metadata. Keep recording as a separate FFmpeg output.

- [x] **Step 5: Gate readiness**

Do not expose the public URL until `#EXT-X-PREFETCH`, init file, and one completed readable segment exist. Failure must remain an LHLS error; do not relabel ordinary HLS as LHLS.

- [x] **Step 6: Run and commit**

```powershell
rtk go test ./internal/obsrtmp ./internal/server -run LHLS -count=1
git add internal/obsrtmp/lhls_sink.go internal/obsrtmp/lhls_sink_test.go internal/obsrtmp/manager.go internal/obsrtmp/manager_test.go internal/server/server.go internal/server/media_paths.go
git commit -m "feat: add community LHLS output"
```

---

### Task 4: Implement MediaMTX LL-HLS and RTSPT **[GPT-5.5]**

**Files:**
- Create: `internal/obsrtmp/mediamtx.go`
- Create: `internal/obsrtmp/mediamtx_test.go`
- Modify: `internal/obsrtmp/manager.go`
- Modify: `internal/obsrtmp/manager_test.go`
- Modify: `internal/server/server.go`

- [x] **Step 1: Write lifecycle and process-ownership tests**

Cover startup, health, port conflict, publish failure, sidecar crash, graceful stop, forced timeout, and protection against terminating unrelated processes.

- [x] **Step 2: Start an app-owned minimal MediaMTX configuration**

Use configurable loopback management/HLS ports and an advertised RTSP port. Disable unused RTMP, WebRTC, SRT, and MoQ listeners. Restrict publishing to the app-owned path and credentials.

- [x] **Step 3: Publish H.264/AAC over RTSP/TCP**

Use one MediaMTX path per OBS session. Keep the recording output separate. Store owned process handles and session path explicitly.

- [x] **Step 4: Implement LL-HLS proxying without rewriting protocol semantics**

Proxy the MediaMTX master/media playlists, init segments, full segments, and parts through the public `/stream` surface. Preserve session query strings, range requests, blocking-reload query parameters, request cancellation, status codes, and content types.

- [x] **Step 5: Gate LL-HLS readiness**

Require `#EXT-X-SERVER-CONTROL`, `#EXT-X-PART-INF`, `#EXT-X-MAP`, `#EXT-X-PART`, and `#EXT-X-PRELOAD-HINT`, plus an FFprobe-readable H.264/AAC master playlist.

- [x] **Step 6: Gate RTSPT readiness**

Require MediaMTX path readiness and an independent RTSP/TCP probe that sees H.264 and AAC. Generate `rtspt://<advertised-host>:<port>/<session-path>` only after success.

- [x] **Step 7: Implement ordered shutdown**

Stop FFmpeg, wait for path removal, then stop only the owned MediaMTX process. Remove per-session credentials and status URLs.

- [x] **Step 8: Run and commit**

```powershell
rtk go test ./internal/obsrtmp ./internal/server -run 'LLHLS|RTSP|MediaMTX' -count=1
git add internal/obsrtmp/mediamtx.go internal/obsrtmp/mediamtx_test.go internal/obsrtmp/manager.go internal/obsrtmp/manager_test.go internal/server/server.go
git commit -m "feat: add LL-HLS and RTSPT outputs"
```

---

### Task 5: Update API and UI **[GPT-5.4-mini after 5.5 contract]**

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Modify: `internal/server/ui.go`

- [x] **Step 1: Write canonical round-trip and capability tests**

Verify all four canonical values, legacy aliases, `experimental`, `available`, `selectable`, transport-specific URL shape, and no cross-transport fallback.

- [x] **Step 2: Render all four options**

```html
<select id="obsLatencyMode" aria-label="OBS latency mode">
  <option value="hls">通常遅延（HLS）</option>
  <option value="lhls">低遅延（LHLS, 実験）</option>
  <option value="llhls">超低遅延（LL-HLS, 実験）</option>
  <option value="rtspt">リアルタイム（RTSPT, PC専用）</option>
</select>
```

- [x] **Step 3: Render status honestly**

Disable share actions until readiness succeeds. Show HLS-family browser preview only when its public master is ready. Show RTSPT as copyable text with no browser preview. Preserve detailed startup errors.

- [x] **Step 4: Run and commit**

```powershell
rtk go test ./internal/server -run 'OBS|Latency|LHLS|RTSP' -count=1
git add internal/server/server.go internal/server/server_test.go internal/server/ui.go
git commit -m "feat: expose OBS latency capabilities"
```

---

### Task 6: Document and run automated verification **[GPT-5.4-mini]**

**Files:**
- Modify: `README.md`
- Modify: `docs/OBS_AVPRO_FEASIBILITY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/ROADMAP_INDEX.md` if necessary

- [x] **Step 1: Document protocol and platform trade-offs**

Do not claim measured latency from segment/part duration. Mark LHLS and LL-HLS experimental until Task 7 passes.

- [x] **Step 2: Run all tests**

```powershell
rtk go test ./... -count=1
```

- [x] **Step 3: Commit**

```powershell
git add README.md docs/OBS_AVPRO_FEASIBILITY.md docs/ROADMAP.md docs/ROADMAP_INDEX.md
git commit -m "docs: describe OBS latency transports"
```

---

### Task 7: Browser and VRChat acceptance **[GPT-5.5]**

**Files:**
- Create: `docs/OBS_LATENCY_ACCEPTANCE_YYYY_MM_DD.md`

- [ ] **Step 1: Verify HLS, LHLS, and LL-HLS public URLs**

Record playlist tags, MIME/cache headers, first frame, reconnect, five-minute stability, and audio/video sync. Confirm LHLS serves the active prefetched segment and LL-HLS preserves MediaMTX session/query behavior.

- [ ] **Step 2: Verify all four ImagePadServer-produced URLs in VRChat PC**

Use the actual ImagePadServer output, not third-party servers. Preserve AVPro API/playback-path logs and whether each low-latency feature is consumed or merely falls back to full segments.

- [ ] **Step 3: Measure end-to-end latency**

Use an OBS millisecond clock or equivalent source/receiver comparison. Record median, worst observed latency, startup time, and reconnect time. Do not infer latency from configured durations.

- [x] **Step 4: Verify failure behavior** (automated; see `docs/OBS_LATENCY_ACCEPTANCE_2026_06_29.md`)

Cover occupied ports, missing/invalid MediaMTX, LHLS sink rejection, sidecar crash, FFmpeg crash, and network disconnect. Confirm the selected transport never silently changes.

- [x] **Step 5: Record acceptance and update experimental flags only from evidence** (recorded; LHLS/LL-HLS kept experimental pending VRChat evidence)

Quest acceptance remains separate where a Quest device is required.

---

## Completion Checklist

- [x] Concrete producers are selected for all four required modes.
- [x] FFmpeg file-output suppression of LHLS prefetch is accounted for.
- [x] MediaMTX LL-HLS parts and RTSP/TCP read path are proven locally.
- [x] No custom LL-HLS packager remains.
- [x] No protocol-label fallback is allowed.
- [x] Implementation tests pass.
- [ ] ImagePadServer-produced URLs pass browser and VRChat acceptance.
