# Tier5 Gate: Zero-Based GPU Renderer

Date: 2026-07-16

## Decision

**RETURNED TO TIER5 FOR RECHECK (implementation still NO-GO).** Tier4 has
resolved the specification questions below in the inventory. This document is
not a Tier5 PASS; the owner must wait for a fresh read-only audit.

## P0 blockers — resolved in Tier4, pending Tier5 evidence audit

- Numeric region thresholds, frame samples, and fail-closed rules are now D1.
- Waveform source/rect, font order, playlist fade, and exact HLS tail policy are
  now D2, with fixture fields and source ownership.

## P1 blockers — resolved in Tier4, pending source/fixture verification

- Pixel format/color range/alpha contract is D3.
- CPU call-path evidence and completeness criteria are D7.
- Blur, crop, shadow, rounded corners, fallback constants are D4; tool/font
  fingerprints are D5.
- NVIDIA/AMD/Metal/unsupported adapter requirements and evidence are D5.
- Cancellation bounds, cleanup, and error precedence are D6.

## P2 strengthening — resolved in Tier4

- Boundary samples are D8; per-region metrics and masks are D1.
- Font identity, versions, shaping, Japanese, combining marks, and RTL are D2,
  D5, and D9.

## Positive findings

- CPU element categories and layer order are broad and useful.
- Tier3 dependency order, ownership, reversible waves, and user-controlled final
  switch are clear.
- The plan correctly bans `len+6`, spectrum-derived waveform, placeholder glyphs,
  simple-blur substitution, and aggregate-RMSE-only acceptance.

## Tier5 recheck request

Tier5 must verify every D1–D9 decision against the cited source files and
fixture-generation commands, then return PASS or identify a concrete residual.
Until that audit returns PASS, no GPU implementation or production-route
switch is authorized. Any unresolved source value must remain an explicit
fixture-derived field; it must not be silently guessed by a shader.

## Residuals intentionally left for Tier5

- Confirm the exact current CPU rounded-corner/shadow symbols and whether their
  constants match D4 (the inventory records the required value and source).
- Confirm the installed font paths and FFmpeg/libass/FreeType versions on each
  target; missing hardware or fonts is evidence-unavailable, not PASS.
- Confirm that the proposed region thresholds are reproducible on the frozen
  CPU fixtures and adjust only through a versioned Tier5 decision.

## Tier5 re-audit result (2026-07-16)

**NO-GO remains.** The contract is more explicit, but evidence and current
implementation are not yet aligned:

- D1 thresholds and sample times are specified, but the golden fixture set has
  not been generated, so reproducibility is unproven.
- The cross-platform font contract conflicts with the current embedded
  NotoSansJP/fallback implementation.
- Shadow opacity constants in D4 disagree with the current CPU implementation;
  CatmullRom/crop behavior also needs fixture authority.
- The required `testdata/music-render` fixture set and JSON evidence reports are
  absent.
- The current GPU path still contains `len(Frames)+6`, contrary to the exact
  frame-count policy.
- GPU mux output does not yet prove explicit colorspace, primaries, transfer and
  color-range flags required by D3.
- Cancellation/cleanup time bounds and adapter/toolchain fingerprints remain
  unverified.

No implementation or production-route switch is authorized until these
residuals are resolved and Tier5 returns PASS. The final execution decision
remains with the owner.

## Tier4 closeout handoff (2026-07-16)

Tier4 has now made the residuals actionable without changing implementation:

| Severity | Residual | Required evidence/owner |
|---|---|---|
| P1 | Eight fixture manifests and CPU goldens absent | Generate with the command and schema in inventory; Tier2 owns generation, Tier5 audits hashes. |
| P1 | `len(Frames)+6` emits forbidden tail frames | Tier3 removes the path in Waves 4/6; raw count/PTS/HLS probe must pass. |
| P1 | Color flags not proven at mux boundary | Tier3 emits explicit BT.709/tv flags; Tier2 ffprobe test asserts stream metadata. |
| P1 | Cancellation cleanup bounds untested | Tier2 adds the five named bounded tests; Tier5 reviews timing and error precedence. |
| P1 | Font/blur/shadow prose conflicted with CPU source | Resolved: embedded NotoSansJP hashes and source-read CatmullRom/shadow constants are fixture authority. |
| P1 | Toolchain/adapter fingerprints absent | Fixture generator records all commands and sidecar hello evidence; Tier3 executes adapters, Tier5 audits. |

The handoff is ready for a fresh Tier5 read-only audit. This remains a
specification-only closeout: no code change, fixture generation, or production
switch is implied by this document. Tier5 should return PASS only after the
fixture artifacts and evidence commands actually exist; otherwise retain
NO-GO with the concrete severity above.
