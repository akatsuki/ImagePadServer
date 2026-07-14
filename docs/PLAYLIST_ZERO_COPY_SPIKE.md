# GPU-to-Encoder Zero-Copy Spike

The default delivery path remains GPU readback through `GPUFrameTransport` and
CPU row packing to `yuv420p`. This spike defines an optional measurement gate;
it does not enable zero-copy implicitly.

## Evidence schema

Each candidate records adapter/backend, texture format, encoder API, sync mode,
p95 render latency, p95 render+encode latency, CPU utilization, dropped frames,
and receiver continuity. A candidate is `PASS` only when all metrics improve
against the readback baseline and H.264/PTS/RTSP acceptance remains green.
Missing interop, unsupported format, or any regression is `BLOCKED` and stays on
readback.

## Current status

No hardware-surface interop is enabled. Hardware validation is pending on
labeled GPU/encoder runners; this document intentionally prevents a synthetic
or software result from being treated as zero-copy success.
