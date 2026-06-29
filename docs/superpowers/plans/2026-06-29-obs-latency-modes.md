# OBS Latency Modes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the ineffective LHLS/LL-HLS choices with five explicit OBS delivery modes: highest-quality HLS, normal HLS, low-latency RTSP x1, ultra-low-latency RTSP x2, and realtime RTSP x3.

**Architecture:** Keep the existing HLS and MediaMTX RTSP pipelines. Remove LHLS/LL-HLS from selectable capabilities and route legacy `lhls`, `llhls`, `low`, and `ultra` values to new RTSP profiles. Add bitrate multiplication and encoder-purpose selection to `LatencyProfile` so FFmpeg argument generation is mode-specific without duplicating the whole RTSP pipeline.

**Tech Stack:** Go, FFmpeg, MediaMTX RTSP, existing `internal/obsrtmp` manager tests, existing `internal/server` OBS latency API.

---

### Task 1: Replace Latency Profile Definitions

**Files:**
- Modify: `internal/obsrtmp/manager.go`
- Test: `internal/obsrtmp/manager_test.go`

- [ ] **Step 1: Write the failing normalization/capability test**

Add or update `TestNormalizeLatencyModeAndProfile` so it asserts these canonical modes and labels:

```go
cases := []struct {
	name      string
	input     string
	wantMode  string
	wantLabel string
	wantX     int
}{
	{name: "highest hls", input: LatencyModeHLSHigh, wantMode: LatencyModeHLSHigh, wantLabel: "最高画質HLS（遅延増）", wantX: 1},
	{name: "normal hls", input: LatencyModeHLS, wantMode: LatencyModeHLS, wantLabel: "高画質HLS（通常遅延）", wantX: 1},
	{name: "low rtsp", input: LatencyModeRTSPLow, wantMode: LatencyModeRTSPLow, wantLabel: "低遅延RTSP", wantX: 1},
	{name: "ultra rtsp", input: LatencyModeRTSPUltra, wantMode: LatencyModeRTSPUltra, wantLabel: "超低遅延RTSP", wantX: 2},
	{name: "realtime rtsp", input: LatencyModeRTSPRealtime, wantMode: LatencyModeRTSPRealtime, wantLabel: "リアルタイムRTSP", wantX: 3},
	{name: "legacy lhls", input: "lhls", wantMode: LatencyModeRTSPLow, wantLabel: "低遅延RTSP", wantX: 1},
	{name: "legacy llhls", input: "llhls", wantMode: LatencyModeRTSPUltra, wantLabel: "超低遅延RTSP", wantX: 2},
	{name: "legacy rtspt", input: "rtspt", wantMode: LatencyModeRTSPRealtime, wantLabel: "リアルタイムRTSP", wantX: 3},
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `rtk go test ./internal/obsrtmp -run TestNormalizeLatencyModeAndProfile -count=1`

Expected: FAIL because the new constants and `BitrateMultiplier` field do not exist.

- [ ] **Step 3: Implement profile constants and compatibility aliases**

In `internal/obsrtmp/manager.go`, replace selectable profile constants with:

```go
const (
	LatencyModeHLSHigh      = "hls-high"
	LatencyModeHLS          = "hls"
	LatencyModeRTSPLow      = "rtsp-low"
	LatencyModeRTSPUltra    = "rtsp-ultra"
	LatencyModeRTSPRealtime = "rtsp-realtime"

	LatencyModeLHLS  = "lhls"
	LatencyModeLLHLS = "llhls"
	LatencyModeRTSPT = "rtspt"
)
```

Update `legacyLatencyModeAliases`:

```go
var legacyLatencyModeAliases = map[string]string{
	"auto":   LatencyModeHLS,
	"normal": LatencyModeHLS,
	"low":    LatencyModeRTSPLow,
	"ultra":  LatencyModeRTSPUltra,
	LatencyModeLHLS:  LatencyModeRTSPLow,
	LatencyModeLLHLS: LatencyModeRTSPUltra,
	LatencyModeRTSPT: LatencyModeRTSPRealtime,
}
```

Add `BitrateMultiplier int` and `EncoderPurpose video.EncoderPurpose` to `LatencyProfile`.

- [ ] **Step 4: Replace `latencyProfiles` with five selectable profiles**

Use these profile values:

```go
LatencyModeHLSHigh: {
	Mode: LatencyModeHLSHigh, Label: "最高画質HLS（遅延増）", Transport: LatencyModeHLS,
	Available: true, Selectable: true, Target: "10s+", SegmentSeconds: "4", ListSize: "6",
	DVRListSize: "1800", FrameRate: "30", GOPFrames: "120", Reencode: true,
	BitrateMultiplier: 1, EncoderPurpose: video.EncoderStandard,
	Message: "画質優先のHLS出力です。遅延は増えます。",
}
LatencyModeHLS: {
	Mode: LatencyModeHLS, Label: "高画質HLS（通常遅延）", Transport: LatencyModeHLS,
	Available: true, Selectable: true, Target: "5s", SegmentSeconds: "1", ListSize: "8",
	DVRListSize: "1800", FrameRate: "30", GOPFrames: "30", Reencode: true,
	BitrateMultiplier: 1, EncoderPurpose: video.EncoderLowLatency,
	Message: "通常遅延のHLS出力です。",
}
LatencyModeRTSPLow: {
	Mode: LatencyModeRTSPLow, Label: "低遅延RTSP", Transport: LatencyModeRTSPT,
	Available: true, Selectable: true, Target: "3-4s", SegmentSeconds: "2", ListSize: "4",
	DVRListSize: "3600", FrameRate: "30", GOPFrames: "60", Reencode: true,
	BitrateMultiplier: 1, EncoderPurpose: video.EncoderStandard,
	Message: "画質寄りのRTSP出力です。",
}
LatencyModeRTSPUltra: {
	Mode: LatencyModeRTSPUltra, Label: "超低遅延RTSP", Transport: LatencyModeRTSPT,
	Available: true, Selectable: true, Target: "1-2s", SegmentSeconds: "1", ListSize: "4",
	DVRListSize: "3600", FrameRate: "30", GOPFrames: "30", Reencode: true,
	BitrateMultiplier: 2, EncoderPurpose: video.EncoderLowLatency,
	Message: "低遅延と画質のバランスを取ったRTSP出力です。",
}
LatencyModeRTSPRealtime: {
	Mode: LatencyModeRTSPRealtime, Label: "リアルタイムRTSP", Transport: LatencyModeRTSPT,
	Available: true, Selectable: true, Target: "0.5s+", SegmentSeconds: "0.5", ListSize: "16",
	DVRListSize: "3600", FrameRate: "30", GOPFrames: "15", Reencode: true,
	BitrateMultiplier: 3, EncoderPurpose: video.EncoderLowLatency,
	Message: "最小遅延のRTSP出力です。",
}
```

- [ ] **Step 5: Update `NormalizeLatencyMode` and `LatencyCapabilities`**

Accept only the five new canonical modes after aliasing:

```go
switch mode {
case LatencyModeHLSHigh, LatencyModeHLS, LatencyModeRTSPLow, LatencyModeRTSPUltra, LatencyModeRTSPRealtime:
	return mode
default:
	return LatencyModeHLS
}
```

Expose capabilities in this order:

```go
for _, mode := range []string{LatencyModeHLSHigh, LatencyModeHLS, LatencyModeRTSPLow, LatencyModeRTSPUltra, LatencyModeRTSPRealtime} {
```

- [ ] **Step 6: Run tests and commit**

Run: `rtk go test ./internal/obsrtmp -run 'NormalizeLatency|Capabilities' -count=1`

Commit:

```powershell
rtk git add internal/obsrtmp/manager.go internal/obsrtmp/manager_test.go
rtk git commit -m "feat: replace OBS latency mode catalog"
```

### Task 2: Make FFmpeg Encoding Mode-Specific

**Files:**
- Modify: `internal/obsrtmp/manager.go`
- Modify: `internal/video/video_encoder.go`
- Test: `internal/obsrtmp/manager_test.go`
- Test: `internal/video/video_encoder_test.go`

- [ ] **Step 1: Add bitrate scaling helper tests**

Add tests that assert 1080p preset `4500k/5200k/9000k` becomes:

```go
// x1: 4500k / 5200k / 9000k
// x2: 9000k / 10400k / 18000k
// x3: 13500k / 15600k / 27000k
```

- [ ] **Step 2: Implement `scaledLatencyPreset`**

Add a helper in `manager.go`:

```go
func scaledLatencyPreset(preset video.QualityPreset, multiplier int) video.QualityPreset {
	if multiplier <= 1 {
		return preset
	}
	preset.VideoBitrate = video.ScaleBitrateForStreaming(preset.VideoBitrate, multiplier)
	preset.MaxRate = video.ScaleBitrateForStreaming(preset.MaxRate, multiplier)
	preset.BufferSize = video.ScaleBitrateForStreaming(preset.BufferSize, multiplier)
	return preset
}
```

Add exported helper in `video_encoder.go` using the existing bitrate parser style:

```go
func ScaleBitrateForStreaming(value string, multiplier int) string {
	if value == "" || multiplier <= 1 {
		return value
	}
	bps := parseBitrateToBps(value)
	if bps <= 0 {
		return value
	}
	return strconv.Itoa((bps/1000)*multiplier) + "k"
}
```

- [ ] **Step 3: Select encoder purpose from latency profile**

Change encoder selection in `runOne` from hard-coded `video.EncoderLowLatency` to:

```go
purpose := m.currentLatency().EncoderPurpose
if purpose == "" {
	purpose = video.EncoderLowLatency
}
selected := video.VideoEncoderProfile{Name: "copy", Purpose: purpose}
if m.currentLatency().Reencode {
	selected = video.SelectVideoEncoder(parent, ffmpeg, purpose)
}
```

- [ ] **Step 4: Use scaled preset in RTSP and HLS args**

At the start of `ffmpegRTSPArgs`, `ffmpegArgsWithEncoder`, and `ffmpegLHLSArgs`, apply:

```go
latency := m.currentLatency()
preset = scaledLatencyPreset(preset, latency.BitrateMultiplier)
```

Keep LHLS code buildable even if no longer selectable.

- [ ] **Step 5: Make RTSP standard mode use quality-oriented encoder flags**

For `LatencyModeRTSPLow`, `EncoderPurpose` is `video.EncoderStandard`, so NVENC uses `p6`, AQ, lookahead, and capped constant quality. Keep explicit `-maxrate`/`-bufsize` from the preset. Do not add `-tune ull` for this mode.

- [ ] **Step 6: Run tests and commit**

Run:

```powershell
rtk go test ./internal/obsrtmp ./internal/video -run 'FFmpeg|Bitrate|Encoder|Latency' -count=1
```

Commit:

```powershell
rtk git add internal/obsrtmp/manager.go internal/obsrtmp/manager_test.go internal/video/video_encoder.go internal/video/video_encoder_test.go
rtk git commit -m "feat: tune OBS RTSP bitrate and encoder modes"
```

### Task 3: Route Delivery Pipeline by Transport

**Files:**
- Modify: `internal/obsrtmp/manager.go`
- Test: `internal/obsrtmp/manager_test.go`
- Test: `internal/obsrtmp/mediamtx_test.go`

- [ ] **Step 1: Add tests for RTSP profile sidecar routing**

Assert `LatencyModeRTSPLow`, `LatencyModeRTSPUltra`, and `LatencyModeRTSPRealtime` all use MediaMTX sidecar and produce `rtsp://` public URLs.

- [ ] **Step 2: Replace mode checks with transport checks**

Change:

```go
sidecar := latency.Mode == LatencyModeLLHLS || latency.Mode == LatencyModeRTSPT
```

to:

```go
sidecar := latency.Transport == LatencyModeRTSPT
```

Change all `latency.Mode == LatencyModeRTSPT` public RTSP checks to:

```go
latency.Transport == LatencyModeRTSPT
```

Leave `ProxyLLHLS` returning false for all selectable modes.

- [ ] **Step 3: Remove LL-HLS readiness dependency for selectable modes**

Only run `runtime.llhlsReady` when `latency.Transport == LatencyModeLLHLS`, which should now only be reachable by direct internal legacy test setup, not by normalized user input.

- [ ] **Step 4: Run tests and commit**

Run:

```powershell
rtk go test ./internal/obsrtmp -run 'RTSP|MediaMTX|Latency|ProxyLLHLS' -count=1
```

Commit:

```powershell
rtk git add internal/obsrtmp/manager.go internal/obsrtmp/manager_test.go internal/obsrtmp/mediamtx_test.go
rtk git commit -m "fix: route OBS latency profiles by transport"
```

### Task 4: Update API Tests and Version

**Files:**
- Modify: `internal/server/server_test.go`
- Modify: `internal/server/obs_http_acceptance_test.go`
- Modify: `internal/about/about.go`
- Modify: `winres/winres.json`

- [ ] **Step 1: Update server tests**

Change expected capability count/order to:

```go
[]string{"hls-high", "hls", "rtsp-low", "rtsp-ultra", "rtsp-realtime"}
```

Assert POSTing old values stores normalized values:

```go
// "lhls" -> "rtsp-low"
// "llhls" -> "rtsp-ultra"
// "rtspt" -> "rtsp-realtime"
```

- [ ] **Step 2: Bump dev version**

Update:

```go
Version = "v1.4.7-dev7"
FileVersion = "1.4.7.7"
```

Update `winres/winres.json` from dev6 to dev7.

- [ ] **Step 3: Run tests and commit**

Run:

```powershell
rtk go test ./internal/obsrtmp ./internal/server ./internal/video -count=1
rtk go test ./... -count=1
```

Commit:

```powershell
rtk git add internal/server/server_test.go internal/server/obs_http_acceptance_test.go internal/about/about.go winres/winres.json
rtk git commit -m "chore: bump dev build to dev7"
```

### Task 5: Build Dev7 Without Overwriting Dev6

**Files:**
- Create: `dist/1.4.7/dev/dev7/win/imagepadserver-v1.4.7-dev7-windows-amd64.exe`
- Create: `dist/1.4.7/dev/dev7/win/imagepadserver-v1.4.7-dev7-windows-amd64.zip`
- Modify: `cmd/imagepadserver/rsrc_windows_amd64.syso`

- [ ] **Step 1: Generate Windows resources**

Run:

```powershell
rtk proxy pwsh -NoProfile -Command 'go run github.com/tc-hib/go-winres@latest make --in winres/winres.json --arch amd64 --out cmd/imagepadserver/rsrc'
```

- [ ] **Step 2: Build dev7 into a fresh directory only**

Run:

```powershell
rtk proxy pwsh -NoProfile -Command '$ErrorActionPreference="Stop"; $version="v1.4.7-dev7"; $outDir="dist/1.4.7/dev/dev7/win"; New-Item -ItemType Directory -Force -Path $outDir | Out-Null; $exe=Join-Path $outDir "imagepadserver-$version-windows-amd64.exe"; if (Test-Path $exe) { throw "refusing to overwrite existing dev7 exe: $exe" }; $env:CGO_ENABLED="0"; $env:GOOS="windows"; $env:GOARCH="amd64"; go build -trimpath -ldflags "-H=windowsgui" -o $exe ./cmd/imagepadserver; $zip=Join-Path $outDir "imagepadserver-$version-windows-amd64.zip"; if (Test-Path $zip) { throw "refusing to overwrite existing dev7 zip: $zip" }; Compress-Archive -Path $exe -DestinationPath $zip; (Get-Item $exe).VersionInfo | Select-Object FileVersion,ProductVersion | ConvertTo-Json -Depth 3; Get-FileHash $exe -Algorithm SHA256; Get-FileHash $zip -Algorithm SHA256'
```

- [ ] **Step 3: Commit resources**

Run:

```powershell
rtk git add cmd/imagepadserver/rsrc_windows_amd64.syso
rtk git commit -m "chore: update dev7 windows resources"
```

- [ ] **Step 4: Report artifact paths and hashes**

Report the dev7 exe/zip paths and SHA256 hashes. Do not launch dev7 unless explicitly asked.

### Self-Review

- Spec coverage: all five requested modes are represented, legacy LHLS/LL-HLS are not selectable, RTSP bitrates scale x1/x2/x3, HLS gets highest-quality and normal profiles.
- Placeholder scan: no TBD/TODO placeholders remain.
- Type consistency: new constants are used consistently by normalization, capabilities, FFmpeg args, and server tests.
