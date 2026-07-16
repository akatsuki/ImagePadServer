# Tier4: Overlay probe extraction plan

## Scope

This plan isolates the diagnostic text-overlay probe in `cmd/music-render-compare`
without changing the production renderer or the frame-0 comparison contract.
The current implementation is intentionally kept as the compatibility baseline
until the aggregation boundary is verified.

## Dependency-ordered work

1. **Frame-0 compatibility (baseline).** Preserve the existing scene-0 report
   fields and the current single-probe behavior. Add a regression test that
   serializes an unchanged frame-0 report and verifies all existing fields.
2. **Evidence aggregation struct.** Introduce a private `overlayProbeEvidence`
   value that contains receipt, payload hashes, composite hash, and per-region
   metrics. Conversion to `sceneEvidence` remains in one adapter function;
   no probe calls belong in the adapter.
3. **Probe helper.** Extract a context-bounded helper that accepts one scene,
   one point label, and one executable. It owns exactly one cancellation scope
   per sidecar call and returns an evidence value or a recorded error.
4. **Three-point loop.** Iterate the fixed ordered points `start`, `mid`, and
   `end`, deriving scene time/dynamics before each call. The loop only merges
   helper results; it must not perform rendering or mutate production state.
5. **Report adapter and tests.** Merge aggregated evidence into the existing
   report schema while retaining frame-0 field names for compatibility.

## Acceptance conditions

- `go test ./cmd/music-render-compare ./internal/video` passes.
- Frame-0 report JSON remains schema-compatible and byte-stable for unchanged
  input (apart from generated timestamp fields).
- Helper calls are bounded by the existing three-second timeout; every child
  context is cancelled immediately after its call, including errors.
- Exactly three labels are emitted, in deterministic order: `start`, `mid`,
  `end`. A failed point records an error and does not suppress other points.
- Region rectangles are clipped to the frame and duplicate/empty rectangles
  are omitted before comparison.
- No changes to `RunAudioVisualizerHLSGPU`, sidecar production selection,
  shader routing, ASS ownership, or encoder arguments.
- Existing untracked artifacts are not staged.

## Non-goals

This change does not assert GPU/CPU visual parity and does not switch the
production route. Pixel thresholds, overlay payload contracts, and GPU readback
are separate tasks after this diagnostic boundary is stable.
