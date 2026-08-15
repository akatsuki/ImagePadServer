# GPU-only Music Renderer Plan (Historical / Frozen)

> **Status (2026-08-11):** This document describes the superseded GPU
> production proposal. GPU direct H.264, RenderV2, and surface-ring work is
> frozen as diagnostic/technical evidence. It is not the current production
> plan. See [`CPU_MUSIC_RENDERER_PRODUCTION.md`](CPU_MUSIC_RENDERER_PRODUCTION.md)
> for the active decision and completion criteria.

## Goal

The production music renderer owns every visual layer in the wgpu compositor.
CPU work is limited to audio analysis, font shaping/atlas preparation, transport,
encoding, and parity verification. FFmpeg `showwaves` and ASS are diagnostic-only.

## Current contract gap

The Go scene contract now carries a bounded `waveform_q16` sample/envelope payload
alongside `spectrum_q16`, `rms_q15`, and `peak_q15`. The production `RenderV2`
route is still not connected to the native shader dispatch, so the payload is not
yet a production GPU-renderer guarantee.

The remaining production contract requirement is to carry this bounded,
deterministic waveform input with an explicit frame clock and PTS through the
versioned sidecar request.

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

## Draft V2 waveform slice

The diagnostic Draft V2 path now exercises the waveform contract end to end:

- binding 2 is a read-only `waveform_q16` storage buffer;
- the scene uniform reuses its reserved header words for waveform flags and
  logical column count;
- signed min/max pairs are interpolated in WGSL and drawn around the spectrum
  center line, preserving narrow transients;
- missing waveform payloads retain the raw-PCM fallback for Draft compatibility;
- the diagnostic exporter derives a deterministic 752-column min/max payload
  from its PCM fixture before GPU dispatch.

This is diagnostic evidence only. The exporter still performs GPU readback and
CPU YUV420P packing, and `production_ready` remains false until the native
RenderV2 pass graph and GPU-owned encoder consumer are connected.
