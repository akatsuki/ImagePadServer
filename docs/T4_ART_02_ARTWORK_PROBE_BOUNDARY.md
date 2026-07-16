# T4-ART-02: Artwork-only diagnostic probe boundary

The probe is diagnostic-only and must not alter the production route. Text
overlay diagnostics remain independent and must continue to pass.

## Reserved sequence and binding

Existing reserved sequences are occupied by glyph/text diagnostics. Allocate a
named constant for artwork isolation (not a magic number in callers), for
example `DIAG_ARTWORK_ONLY = 0xfffffffffffffffc`. The sidecar shader must
recognize this sequence only when an explicit diagnostic session is active;
normal render requests with that sequence are rejected by the probe boundary.

The artwork texture remains binding 0 (the existing artwork binding). The
probe disables glyph, waveform, loudness, progress, and text-overlay layers
and composites the artwork using the normal crop/blur/overlay transform. It
returns RGBA8 readback plus an `ArtworkReceipt`; no new production binding is
introduced in this ticket.

## CPU cover-crop helper

Add a pure helper in `internal/video`:

```go
RenderArtworkOnlyRGBA(src image.Image, dstW, dstH int, transform ArtworkTransform) (*image.RGBA, error)
```

It must use integer/fixed-point crop coordinates, explicit sRGB conversion,
and the same premultiplied SrcOver order as the existing CPU compositor. The
helper returns the exact crop used for comparison; it does not call FFmpeg.

## Compare report fields

Add an optional `sceneEvidence.artworkProbe` value containing:

```text
sequence, source_hash, request_payload_hash, receipt_payload_hash,
width, height, row_stride, crop_rect, blur_radius, blur_strength,
overlay_rgba, readback_hash, alpha_coverage, mae, rmse, iou, error
```

The report records errors per probe and never converts a missing diagnostic
into a full-frame pass/fail.

## Tier3 implementation tickets

1. **ART-02a constants/protocol:** define the named sequence in Go/Rust and
   reject it outside diagnostic sessions; add protocol tests.
2. **ART-02b CPU helper:** implement the pure cover-crop helper and golden
   source/destination tests (square, portrait, landscape, transparent source).
3. **ART-02c sidecar probe:** isolate artwork binding/layers and return
   readback plus receipt under a three-second timeout; production shader route
   unchanged.
4. **ART-02d report adapter:** record one bounded probe result in the compare
   report; no text fields are overwritten.
5. **ART-02e parity tests:** compare the CPU helper result and GPU readback in
   the same crop; require receipt/source hash equality, crop bounds within one
   pixel, IoU >= 0.99, MAE <= 1, RMSE <= 2.

## Safety acceptance

- Existing reserved glyph/text probes remain byte-compatible.
- Requests without the diagnostic session cannot trigger artwork isolation.
- Production `RunAudioVisualizerHLSGPU`, encoder arguments, bindings, and
  route selection are unchanged.
- Go and Rust unit/protocol tests pass; repeated probe cancellation leaves no
  sidecar process or temporary output.
