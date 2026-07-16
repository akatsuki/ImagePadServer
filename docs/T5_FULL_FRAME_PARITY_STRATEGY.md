# T5 full-frame parity strategy

The isolated text and artwork gates are green, but the full frame still
differs because the CPU reference and WGSL compositor use different background
and dynamic-layer algorithms. The current evidence shows that changing a
single coefficient will not establish parity.

## Decision boundary

Keep the production route unchanged until one of these two strategies is
proven on a diagnostic branch:

1. **Shared layer kernels:** implement the same blur, glow, waveform,
   loudness, progress and alpha-composite kernels in both CPU and WGSL, with
   identical quantization and pixel-center rules.
2. **Canonical base texture:** generate a canonical RGBA base-layer receipt
   (background + artwork + readability) once, upload that exact texture to the
   GPU, and let both routes apply only the shared dynamic/text layers.

The second strategy is the lower-risk parity bridge; it does not require
switching production rendering and preserves GPU execution for dynamic layers.

## Diagnostic milestones

- Flat background only: byte-identical CPU/GPU readback.
- Background + artwork + readability: IoU >= .99, MAE <= 1, RMSE <= 2.
- Dynamic waveform/loudness/progress: same thresholds at start/mid/end.
- Text overlay: retain the existing IoU=1 gate.
- Full frame: mismatch ratio <= .01 and RMSE <= 2 at all three points.

Each milestone must record the layer SHA, dimensions, stride, color space,
premultiplication, backend fingerprint, and frame/PTS. No production shader or
route switch is authorized before all milestones pass Tier5 review.
