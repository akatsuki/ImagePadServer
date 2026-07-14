# Unified Music Scene Design

## Goal

Make single-track music HLS and playlist/radio rendering produce the same
visual specification while using one GPU production renderer. Pixel-identical
output is not required; layout, content, timing, colors, and visual behavior
must match within documented rasterization tolerances.

## Scope

In scope:

- single-track music HLS;
- playlist and radio track MP4 rendering;
- shared audio-feature normalization;
- artwork, background, waveform, spectrum, text, glow, and fade behavior;
- GPU frame generation and FFmpeg muxing;
- CPU reference rendering and comparison fixtures.

Out of scope:

- zero-copy encoder interop;
- new visual themes;
- changing audio codecs or delivery profiles;
- accepting software adapters as production renderers.

## Architecture

```text
Audio analysis + metadata
          |
          v
   MusicSceneSpec (versioned)
       |             |
       v             v
 GPU production   CPU reference
 renderer         renderer/tests only
       |
       v
 packed RGBA frames
       |
       v
 FFmpeg HLS or MP4 muxer
```

`MusicSceneSpec` is the source of truth. It contains the canonical canvas,
normalized feature values, artwork placement, palette, waveform and spectrum
geometry, text boxes, fade intervals, and frame/PTS information. Both output
types consume the same frame stream; only the final FFmpeg muxer arguments
differ.

## Rendering contract

- Canonical design size is 1280x720, scaled proportionally to 360p/720p/1080p.
- Frame rate is 30 fps and PTS is monotonic from zero per render job.
- Spectrum uses 24 normalized bands; RMS and peak use Q15 normalization.
- Waveform geometry, color, opacity, and baseline come from the scene spec.
- Artwork and metadata use the same normalized rectangles and font metrics.
- GPU owns background, waveform, spectrum, artwork compositing, and glow.
- Text is owned by exactly one layer; ASS/drawtext duplication is prohibited.
- Edge fades are represented in the scene spec and applied identically for
  single and playlist outputs.
- GPU rasterization differences are accepted only within per-channel and
  structural tolerances defined by comparison fixtures.

## Migration

1. Extract the current CPU layout constants and feature transforms into the
   versioned scene model.
2. Add deterministic quiet/loud/artwork/text fixtures and CPU reference hashes.
3. Extend WGSL and the sidecar protocol to consume the scene model.
4. Route `RunAudioVisualizerHLS` through the GPU frame producer.
5. Keep `RenderRadioTrack` on the same producer and only vary the mux tail.
6. Remove production CPU fallback; retain CPU code only for reference tests.

## Error handling

- Missing sidecar, adapter, device, shader, or frame readback returns
  `gpu_required` or `gpu_renderer_unavailable`.
- A sidecar loss cancels the active mux and never silently switches to CPU.
- Invalid scene dimensions, feature ranges, or PTS fail before FFmpeg starts.
- The scene schema version is checked at the Go/Rust boundary.

## Verification

- Unit tests validate scene normalization and all geometry at 360p/720p/1080p.
- Dynamic fixtures compare quiet/loud feature changes and frame timing.
- GPU-vs-reference tests compare structure, colors, text ownership, and fades
  with documented tolerances.
- Single HLS and playlist MP4 integration tests use the same fixture and verify
  codec, pixel format, frame count, duration, and monotonic timestamps.
- Existing Windows NVIDIA/AMD hardware soaks remain acceptance evidence.
