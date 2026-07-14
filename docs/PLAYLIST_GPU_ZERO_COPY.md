# GPU playlist zero-copy spike (T8)

The production renderer continues to use the bounded `GpuFrame` readback ring
and the existing RGBA8/BGRA8-to-yuv420p conversion. This spike only defines a
measurement contract for a future hardware-surface path; it does not select a
new default or provide a CPU rendering fallback.

## Readiness gate

Zero-copy is **READY** only when all of the following are proven on the same
OS, adapter, driver, and encoder build:

1. wgpu exports a surface handle (`shared_texture`, `dma_buf`, or `iosurface`)
   with the exact dimensions, format, and color space requested by the encoder.
2. The encoder imports that handle without an intermediate CPU map/copy.
3. A 30-second run completes with `imported_frames == frames`,
   `copy_bytes == 0`, monotonic PTS, and no device-loss/reconnect events.
4. The encoded output passes the existing H.264/AAC, RTSP, and playlist
   acceptance tests.

If the hardware surface or encoder import API is unavailable, record
`status: "BLOCKED"` and a stable reason such as
`encoder_import_interop_unavailable`. Do not change the readback default.

## Evidence schema v1

Each probe writes one JSON object matching the following shape:

```json
{
  "schema": 1,
  "status": "READY | BLOCKED | UNSUPPORTED",
  "reason": "string",
  "backend": "d3d12 | vulkan | metal | dx11 | unknown",
  "handle": "shared_texture | dma_buf | iosurface | opaque",
  "frames": 0,
  "elapsed_ms": 0,
  "readback_bytes": 0,
  "imported_frames": 0,
  "copy_bytes": 0,
  "p50_ms": null,
  "p95_ms": null
}
```

`readback_bytes` and `copy_bytes` are byte counters, not estimates. A missing
counter is invalid evidence. The benchmark must also record OS, adapter name,
driver, sidecar version, encoder name/version, dimensions, pixel format,
commit, and capture timestamp in its surrounding run metadata.

## Current result

The repository has no cross-platform wgpu-to-FFmpeg hardware-surface import
bridge yet. Therefore T8 remains **BLOCKED** pending an implementation and a
hardware/encoder run. The new Rust `HardwareSurface` trait and
`ZeroCopyEvidence` type are a compile-time contract only.
