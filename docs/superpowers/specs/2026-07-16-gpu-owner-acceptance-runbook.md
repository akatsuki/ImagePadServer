# GPU owner acceptance runbook

This is the remaining owner-controlled step. It does not switch production
routing; it creates evidence for the Tier5 gate.

## Windows/NVIDIA or AMD

Set the actual binaries and adapter label, then run the hardware integration
and one fixture comparison:

```powershell
$env:IMAGEPAD_PLAYLIST_COMPOSITORD = (Resolve-Path 'dist/gpu/0.1.0/playlist-compositord.exe').Path
$env:IMAGEPAD_FFMPEG = (Resolve-Path "$env:APPDATA/ImagePadServer/bin/v1.6.1-dev25/ffmpeg.exe").Path
$env:IMAGEPAD_FFPROBE = (Resolve-Path "$env:APPDATA/ImagePadServer/bin/v1.6.1-dev25/ffprobe.exe").Path
$env:IMAGEPAD_GPU_ADAPTER = 'REPLACE_WITH_ACTUAL_ADAPTER'
go test ./internal/video -run 'TestGPUMusicRenderIntegration|TestGPUSidecarRenderIntegration' -count=1 -v
go run ./cmd/music-render-compare -input .tmp/music-fixtures/embedded-cover-latin.wav -output-dir .tmp/gpu-owner-compare -height 360
```

## Required evidence

- sidecar hello reports adapter/backend/toolchain fingerprints;
- GPU HLS ffprobe reports 30fps, exact frame count, monotonic PTS, H.264
  `yuv420p`, BT.709 primaries/transfer/colorspace, and TV range;
- start/mid/end GPU PNG SHA-256 and per-region metrics are present;
- sidecar cancellation and partial-output cleanup complete within the D6 bounds;
- the report does not contain `gpuAdapter=unknown` and
  `comparisonGate.fingerprintRecorded=true`.

## Gate

Tier5 may return **GO** only when every item is present in a fresh report. A
hardware adapter initialization without these artifacts is not acceptance.
The owner decides whether to run this external-state step.
