# GPU text parity decision

Status: NOT GO

## Evidence

The current GPU path does not reproduce the CPU text renderer. The CPU
reference uses FFmpeg/libass with the resolved Noto Sans JP family and
per-field weights (title 600, artist 500, album 400, time 500). The GPU path
uses one Go Regular atlas rasterized at 48px and approximates the baseline in
Rust. The atlas metadata carries font names and weights, but the uploaded
texture is still a single face and does not encode those per-run raster
choices.

Additional confirmed differences are:

- GPU baseline is derived from a fixed 7px ink-top estimate, not the ASS
  baseline and ascent/descent metrics.
- GPU glyph instances are not clipped to the CPU field rectangles.
- GPU coverage is reduced by a production `0.85` gain and composited through
  the scene color path rather than the CPU source-over text path.
- Isolated atlas probes can pass while the production screenshot remains
  visibly different; therefore manifest/count parity is not sufficient.

## Required redesign before GO

1. Make the CPU-rendered text mask (or an atlas generated from the exact
   resolved ASS faces/weights) the canonical text asset for both routes.
2. Carry per-run clip rectangles and premultiplied color/alpha in the shared
   contract.
3. Composite that asset with the same source-over equation and pixel-center
   rules on CPU and GPU.
4. Add a production-path acceptance gate that compares title, artist, album,
   and time crops, not only isolated glyph diagnostics.

The current single-face atlas implementation must not be presented as CPU/GPU
visual parity.

## Ordered implementation worklist

- **T4-TXT-01**: extend `TextOverlayMetadata` with an explicit screen-space
  payload semantic, alpha mode, pixel origin, and full-frame stride validation.
- **T4-TXT-02**: rasterize the same ASS/libass input used by the CPU route into
  a transparent full-frame RGBA payload for each canonical frame.
- **T4-TXT-03**: add a texture-only sidecar probe and verify payload hash,
  dimensions, stride, and alpha-edge metrics before enabling production use.
- **T4-TXT-04**: replace the production glyph loop with one-to-one
  premultiplied source-over sampling of that payload; retain the atlas only for
  diagnostics and backward compatibility.
- **T5-TXT-GATE**: require start/mid/end text-crop parity and Unicode fallback
  parity before GO.

The same gate discipline applies to the remaining dynamic layers. The current
GPU loudness path omits the CPU guide lines and down-samples the envelope, and
the spectrum path uses approximate bar geometry rather than the CPU fixed-fade
layout. These are separate blockers and must receive region-level probes
before full-frame approval.

## First texture-only evidence

Fixture: `embedded-cover-latin.wav`, 1280x720 render, 150 frames, NVIDIA
GeForce RTX 5070 Ti / Vulkan. The updated comparison CLI generated the
libass-backed screen payload and the sidecar read it back at start, middle,
and end. For title, artist, album, and time crops the measured values were
IoU `1.0`, MAE `0`, and RMSE `0` at every sampled point. This proves the
screen-payload transport/raster path, but does not prove full-frame parity:
the production screenshot still has large background, spectrum, loudness,
and progress differences. The overall gate therefore remains NOT GO.
