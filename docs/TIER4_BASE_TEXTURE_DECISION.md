# Tier4 base-texture parity decision

## Evidence

The CPU music renderer creates a reusable base image before the frame loop:

1. cover-scale and crop the artwork;
2. apply FFmpeg `gblur=sigma=64` to the full canvas;
3. apply the adaptive readability overlay;
4. composite the rounded foreground artwork and shadow.

The GPU path currently receives only the canonical scene payload. Its shader
reconstructs the base with a three-sample blur and a single-pass approximation.
That is not the same operation, so full-frame parity cannot be reached by
tuning a few coefficients.

## Decision

For the parity gate, upload the CPU-produced base image once per render job as
a canonical `base_texture`. The GPU shader then owns the per-frame work only:

- spectrum bars and waveform;
- loudness envelope/trend;
- progress marker;
- text/artwork metadata layers that are explicitly enabled.

This preserves GPU frame generation while making the static compositor a
shared artifact. A later GPU-native two-pass Gaussian implementation can
replace this upload behind the same contract after parity is proven.

## Acceptance gates

- base texture receipt: SHA-256, dimensions, row stride, color space;
- CPU base vs GPU base-only diagnostic: IoU `>= 0.999`, MAE `<= 1`;
- dynamic-only probe over the same base: IoU `>= 0.995`, RMSE `<= 3`;
- full three-point frame comparison and mux/frame cleanup checks remain
  mandatory; isolated probe success never grants GO by itself.

