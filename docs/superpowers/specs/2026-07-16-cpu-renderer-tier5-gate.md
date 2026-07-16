# Tier5 Gate: Zero-Based GPU Renderer

Date: 2026-07-16

## Decision

**NO-GO for implementation or production-route switch.** The inventory and
execution plan are useful, but the following gates must be resolved first.

## P0 blockers

- Define numeric per-region acceptance thresholds: MAE/RMSE/max error/alpha
  coverage, sampled frame times, and failure rules. “Matches golden” is not a
  deterministic gate.
- Resolve waveform rectangle/filtergraph source, platform font fallback order,
  playlist transition/fade behavior, and HLS tail policy before Wave 0/1.

## P1 blockers

- Freeze pixel conversion: input/output pixel formats, BT.709 limited/full range,
  chroma subsampling, scaling flags, transfer/primaries, and alpha handling.
- Complete CPU call-path evidence for waveform, mux, and error helpers.
- Specify or fixture-derive shadow/rounded-corner/cover interpolation, blur
  kernel and border behavior, fallback fingerprint ray constants, and exact
  FFmpeg/font versions.
- Define the NVIDIA/AMD/Metal/unsupported adapter matrix, feature requirements,
  evidence schema, and error-path assertions.
- Add measurable cancellation bounds, cleanup assertions, and error precedence.

## P2 strengthening

- Require boundary/time samples beyond start/mid/end for scroll and fade.
- Require per-region frame masks, not only aggregate metrics.
- Pin exact font identity/version and shaping/fallback behavior, including
  Japanese, combining marks, and RTL fixtures.

## Positive findings

- CPU element categories and layer order are broad and useful.
- Tier3 dependency order, ownership, reversible waves, and user-controlled final
  switch are clear.
- The plan correctly bans `len+6`, spectrum-derived waveform, placeholder glyphs,
  simple-blur substitution, and aggregate-RMSE-only acceptance.

## Go criteria

Tier4 must resolve every P0/P1 item, update the inventory with measurable gates,
and Tier5 must return PASS. Until then, no GPU implementation or production
route switch is authorized.
