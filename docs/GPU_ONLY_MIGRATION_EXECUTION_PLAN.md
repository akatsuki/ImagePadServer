# GPU-only music renderer execution plan

Status: ARCHIVED — the large parity implementation was rolled back to the
pre-`029ef5f` shader-rebuild baseline. The acceptance evidence below is
historical and must not be treated as proof of the current working tree.

The production music path may use CPU for audio decoding, feature analysis,
font selection/atlas preparation, metadata, and muxing. It must not use CPU
rasterization or CPU color conversion for the rendered picture.

## Ordered work

1. **Freeze the scene contract (M0)**
   - Keep `CanonicalMusicScene` as the single layer-order contract.
   - Keep CPU raster helpers as reference fixtures only.
   - Add a production-path static check for the forbidden CPU drawing calls.

2. **Artwork and base (M1)**
   - Upload normalized artwork pixels and palette/readability metadata.
   - Implement cover crop, rounded mask, shadow, blur, and fallback geometry in
     WGSL using the artwork binding and scene rectangles.
   - Remove `RenderVisualizerBaseCPUWithFallback` and `BaseTexture` from the
     production GPU route; retain them only for reference comparison.

3. **Dynamic layers (M2)**
   - Transport loudness/trend/guides as bounded Q16 arrays, not a raster.
   - Use GPU spectrum and signed min/max waveform primitives.
   - Remove `showwaves`, `RenderSpectrum*CPU`, and `WaveformTexture` from the
     production route. The existing min/max payload is experimental until the
     parity gate passes.

4. **Text (M3)**
   - CPU may select fonts and prepare a glyph atlas/run list.
   - WGSL draws title, artist, album, and time, including clipping, scrolling,
     fallback glyphs, and deterministic missing-glyph behavior.
   - Remove ASS generation, measurement, and post-YUV ASS filtering from the
     production route.

5. **Frame transport and encode (M4)**
   - Keep sidecar RGBA rendering, then add a wgpu compute pass for YUV420P
     planes (or a verified hardware-import path).
   - CPU remains responsible only for audio/mux delivery after the GPU planes
     are produced. `rgbaToYUV420p` remains a reference implementation.

6. **Convergence and release gate (M5)**
   - Single and playlist routes both call `CanonicalMusicScene`.
   - Remove experimental flags only after validation.
   - Run 150 frames on NVIDIA, AMD/iGPU, and macOS where available.

## Strict acceptance gate

- frame count, PTS, and duration agree for both routes;
- maximum visual MAE <= 1 and maximum RMSE <= 2 across 150 frames;
- artwork, waveform, loudness, spectrum, text, and fallback masks each pass
  their layer-specific checks;
- production-path static audit finds no CPU raster, `showwaves`, ASS, or CPU
  RGBA-to-YUV call;
- no-GPU startup fails explicitly; no silent CPU renderer is allowed.

## Current acceptance evidence

- `artifacts/strict-v84-final-150/report.json`: cached server audio, 1280x720,
  150/150 CPU-reference and NativeGPU frames, nine comparison points, maximum
  all-frame MAE `0.265865`, maximum all-frame RMSE `1.235736`, duration delta
  `0.033334s`, NVIDIA RTX 5070 Ti through wgpu/Vulkan; gate `pass: true`.
- AMD Radeon integrated graphics negotiated wgpu/DX12 YUV420P output and passed
  the real playlist MP4 render/ffprobe smoke.
- Production static guards reject CPU base/spectrum/waveform/ASS/RGBA-to-YUV
  calls in both the single-track and playlist GPU routes.
- `go test ./...`, Rust `cargo test` (53 passed, 1 ignored), GPU-required
  fail-closed tests, NVIDIA playlist smoke, and AMD playlist smoke pass.
- CPU composition remains callable only through explicitly named reference and
  comparison APIs; production rendering fails closed without a hardware GPU.
