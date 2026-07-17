# Music Render V2 protocol

`render_v2` is an opt-in wire contract for the CPU-parity migration. It is
separate from the legacy `render` request so a CPU-completed screen raster
cannot be accepted accidentally.

```json
{
  "type": "render_v2",
  "width": 1280,
  "height": 720,
  "sequence": 0,
  "pts_ns": 0,
  "job": {
    "width": 1280,
    "height": 720,
    "fps": 30,
    "artwork_mode": "Fallback",
    "layers": [
      {"name": "background", "provider": "NativeGPU"},
      {"name": "artwork", "provider": "NativeGPU"},
      {"name": "spectrum", "provider": "NativeGPU"},
      {"name": "waveform", "provider": "NativeGPU"},
      {"name": "loudness", "provider": "NativeGPU"},
      {"name": "text", "provider": "NativeGPU"},
      {"name": "progress", "provider": "NativeGPU"},
      {"name": "fade", "provider": "NativeGPU"}
    ]
  },
  "output": "rgba8"
}
```

Until the V2 pass graph is wired, a valid request returns
`music_render_v2_not_implemented`. Invalid geometry, implicit artwork mode,
`screen_rgba`, or `GoldenUpload` returns a validation error before rendering.

The request may become a GO candidate only after all eight layer receipts are
`NativeGPU` and raw CPU/GPU frame comparison passes the Tier5 gates.
