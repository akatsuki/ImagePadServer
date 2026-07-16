# Tier4: Artwork/background and mux parity plan

Text-overlay parity is now a PASS (IoU 1.0). The overall gate remains NO-GO;
this plan isolates the remaining independent causes without reopening the text
diagnostic path.

## Root-cause boundaries

1. **Artwork/background:** CPU and GPU do not share a canonical compositing
   contract. Crop, blur, overlay opacity, color space, and rounding are
   implemented in separate FFmpeg/Go and WGSL paths. A full-frame mismatch is
   therefore not evidence against the text overlay.
2. **Frame tail:** CPU HLS includes three additional muxed frames (153 vs the
   canonical 150). This is a raw-video PTS/mux policy difference, not a GPU
   raster issue. The contract must choose canonical video frames and explicitly
   define audio-tail handling.

## Bounded tickets

### T4-ART-01 — canonical artwork receipt

Add a versioned artwork transform receipt to the scene evidence: source hash,
crop mode, source/destination rectangles, blur radius/strength, overlay RGBA,
color space, and premultiplication. No renderer switch. Acceptance: CPU and
GPU requests carry identical receipt fields; malformed dimensions/stride are
rejected; existing artwork-only scenes remain valid.

### T4-ART-02 — deterministic artwork probe

Add an opt-in sidecar probe that composites only the artwork/background over a
flat known color and returns RGBA readback plus SHA256. Compare CPU and GPU in
the same crop and color space. Acceptance: receipt hash equality, crop bounds
within one pixel, and per-pixel MAE/RMSE recorded. Do not use full-frame scores
as a text verdict.

### T4-ART-03 — shared transform implementation

After evidence identifies the first mismatch, implement one transform at a
time (crop, then blur, then overlay/color). Each change has a golden PNG and a
GPU readback test. Acceptance target for the isolated artwork probe is IoU
>=0.99, MAE<=1, RMSE<=2; text-overlay gate remains unchanged and must stay
PASS.

### T4-MUX-01 — canonical raw-frame contract

Define `expected_frames = ceil(duration*fps)` and zero-origin PTS for raw video.
Emit a manifest containing frame index, PTS, duration, and dimensions before
audio mux. Acceptance: both CPU/GPU raw streams contain exactly the manifest
frames; first PTS=0; no duplicate or tail frame.

### T4-MUX-02 — single mux policy

Use the same FFmpeg mux arguments and audio-tail policy for both paths
(`-fps_mode passthrough`, explicit `-frames:v`, deterministic HLS segment
settings). Keep audio encoding separate from raw-frame generation. Acceptance:
153/150 discrepancy is eliminated, duration delta <= one frame, and playlist
segment boundaries match.

### T4-MUX-03 — cancellation/cleanup evidence

Run bounded cancellation tests against the real HLS GPU function, asserting
process exit, output directory cleanup, and no orphan FFmpeg/sidecar process.
Acceptance: cancellation completes within timeout in three repeated runs.

## Gate order

Text PASS is a prerequisite and must not regress. Then pass ART-02/03, MUX-01,
MUX-02, and MUX-03 independently. Only after all evidence is green may the
full screenshot gate be reconsidered; production GPU route remains unchanged
until that final review.
