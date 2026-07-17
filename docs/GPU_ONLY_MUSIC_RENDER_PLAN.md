# GPU-only Music Renderer Plan

## Goal

The production music renderer owns every visual layer in the wgpu compositor.
CPU work is limited to audio analysis, font shaping/atlas preparation, transport,
encoding, and parity verification. FFmpeg `showwaves` and ASS are diagnostic-only.

## Current contract gap

`MusicFeature` currently carries `spectrum_q16`, `rms_q15`, and `peak_q15`, but no
bounded waveform sample/envelope payload. A GPU waveform cannot be reconstructed
from those fields without changing the visual result. The contract must therefore
add a bounded, deterministic waveform input with an explicit frame clock and PTS.

## Contract extension

- Add `waveform_q16` (fixed maximum sample count per frame) or an equivalent
  precomputed envelope field to the sidecar request.
- Include `sample_rate_hz`, `frame_index`, and `pts_ns` in the same request.
- Reject payloads that exceed the bound or whose clock fields are inconsistent.
- Keep the schema versioned; old clients must fail closed rather than silently
  falling back to CPU drawing.

## GPU ownership order

1. Background/artwork tile, blur, mask, and shadow.
2. Loudness and progress primitives.
3. Spectrum bars and waveform from GPU storage buffers.
4. Glyph atlas sampling and text placement/composite.
5. Single RGBA output and YUV/encode transport.

Each layer requires a GPU sentinel, exact bounds/alpha tests, and a 150-frame
parity report before becoming the production default.

## Acceptance gate

- CPU and GPU duration, PTS, and frame count match.
- `max MAE <= 1` and `max RMSE <= 2` across 150 frames.
- No production FFmpeg `showwaves`, `showfreqs`, or ASS filters.
- CPU-generated visual textures are absent from the production GPU route.
- Diagnostic CPU/FFmpeg routes remain explicitly opt-in and mutually exclusive.
