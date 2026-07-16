# T4-ART-02: Artwork-only compare metrics

`RenderArtworkCoverCPU` and the GPU artwork probe now exist. This contract
keeps their comparison in an isolated, flat domain; full-frame scores are not
used to judge artwork parity.

## Report schema

Add optional `sceneEvidence.artworkOnly`:

```text
sourceHash, requestPayloadHash, receiptPayloadHash
width, height, rowStride, cropRect
cpuReadbackHash, gpuReadbackHash
cpuAlphaCoverage, gpuAlphaCoverage
intersectionPixels, unionPixels, iou
mae, rmse, maxAbsError
bboxCPU, bboxGPU, bboxDelta
threshold, sampler, error
```

Both images are rendered over opaque black with identical dimensions and
RGBA8/sRGB premultiplied semantics. Metrics are calculated over the complete
probe rectangle (not source-atlas coordinates). Alpha coverage uses a common
`alpha > 8` threshold; RGB error is measured only where either alpha mask is
non-zero. Empty/failed probes set `error` and do not produce a false PASS.

## Acceptance

- Request/receipt/source hashes agree; dimensions and crop rectangle match.
- CPU/GPU readback hashes may differ before parity, but are always recorded.
- Isolated artwork gate: IoU >= 0.99, MAE <= 1, RMSE <= 2, maxAbsError <= 8,
  and bounding-box delta <= 1px.
- Five golden cases from the cover formula spec pass independently: 4:3→16:9,
  16:9→1:1, equal aspect, one-pixel/odd dimensions, and transparent source.
- Text overlay IoU=1 gate remains present and unchanged in the same report.
- Full screenshot mismatch does not downgrade the isolated metric; it remains
  a separate background/compositor gate.

## Implementation boundary

The compare command invokes the existing bounded GPU probe once per case,
converts its readback into the flat-domain image, calls the CPU helper with
the same transform, and writes this schema. No production route, shader
binding, mux policy, or ASS ownership changes are permitted in this ticket.
