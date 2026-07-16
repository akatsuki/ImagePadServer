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
