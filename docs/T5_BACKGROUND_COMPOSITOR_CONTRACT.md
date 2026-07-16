# T5: Canonical background/compositor contract

The text and isolated artwork gates pass, but full-frame CPU/GPU parity is not
met because the two renderers still implement the background compositor
independently. This contract defines the shared boundary before another shader
rewrite.

## Canonical inputs

- `Palette`: primary, accent, background, overlay RGBA, blur strength,
  readability, all in sRGB8 plus explicit premultiplied rules.
- `ArtworkTransformReceipt`: source/output dimensions, cover crop, pixel-center
  convention, sampler mode, and source SHA.
- `Dynamics`: RMS, peak, progress, fade-in/out, spectrum, loudness envelope and
  trend, all with fixed quantization and frame index/PTS.
- `Layout`: one coordinate space and one scale transform for artwork, text,
  spectrum, loudness and progress rectangles.

## Required operation order

1. Decode artwork bytes into the declared color space.
2. Apply the receipt's cover transform and blur kernel.
3. Composite readability overlay using premultiplied SrcOver.
4. Composite artwork tile.
5. Composite glow, waveform, spectrum, loudness and progress layers in the
   declared order.
6. Composite canonical text overlay bytes.
7. Convert to the declared output format and clamp once.

CPU and GPU must use the same kernel taps, pixel-center coordinates, rounding,
alpha threshold, and sRGB/linear conversion. No renderer-specific coefficients
are permitted after the contract is enabled.

## Acceptance evidence

- Contract receipt hash and renderer fingerprints match.
- Flat-background, artwork-only, text-only and full-frame probes use the same
  640x360/1280x720 domain and crop coordinates.
- Each layer has a golden RGBA readback and SHA.
- Full-frame start/mid/end: size match, MAE <= 1, RMSE <= 2, mismatch ratio <=
  0.01, and bounds equal within one pixel.
- Existing text IoU=1 and artwork IoU=1 gates remain green.
- Production GPU selection remains unchanged until the final Tier5 review.

## Non-goals

This contract does not switch the production route or replace the CPU renderer
until all layer and frame-contract evidence is green.
