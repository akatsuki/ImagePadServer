# Playlist GPU compatibility and acceptance

This document is the acceptance ledger for the GPU-required playlist compositor.
It is deliberately separate from the CPU-only audio/HLS tests: a green software
adapter or a CPU render is **not** evidence of support.

## Adapter policy

| Adapter class | Result | Meaning |
| --- | --- | --- |
| Discrete GPU | `PASS` (preferred) | Hardware adapter selected by `wgpu`. |
| Integrated GPU | `PASS` (supported) | Hardware adapter selected by `wgpu`; record the adapter name and backend. |
| `Cpu`, `Other`, or software/llvmpipe | `BLOCKED` | Rendering is unavailable; never downgrade to CPU rendering. |
| No adapter / device creation failure | `BLOCKED` | Record the diagnostic and keep image-only server startup separate. |

The sidecar must report `gpu_renderer_unavailable` for the last two rows. The
acceptance harness treats that result as **blocked**, not passed.

## Required evidence per hardware lane

Each Windows, macOS, and Linux lane should attach one JSON record produced by
`scripts/verify-gpu-acceptance.ps1` (or the equivalent CI wrapper), containing:

- OS and commit;
- `adapter_name`, `device_type`, and `backend`;
- compositor protocol version and sidecar binary hash;
- music scene, fallback scene, and transition checks;
- HLS/RTSP URL readiness, first segment availability, and `ffprobe` output;
- PTS monotonicity, codec/pixel-format/audio checks;
- soak duration, frame drops, queue depth, and device-loss result.

An unavailable adapter produces a record with `status: "BLOCKED"` and a
non-empty `reason`; it must never be rewritten as a successful smoke test.

## Matrix

| Lane | Backend target | Hardware evidence | Status |
| --- | --- | --- | --- |
| Windows | D3D12 (Vulkan fallback) | RTX 5070 Ti adapter, sidecar render, music FFmpeg smoke, fallback frame smoke; AMD Radeon(TM) Graphics 1800s soak (`output/gpu-acceptance-amd-1800s.json`, 2,165,455 frames) | `PASS_SHORT_SMOKE; AMD_SOAK_PASS` |
| macOS | Metal | No macOS runner available in this workspace; no unsupported pass claimed | `BLOCKED_ENVIRONMENT` |
| Linux | Vulkan | No Linux runner available in this workspace; no unsupported pass claimed | `BLOCKED_ENVIRONMENT` |
| Headless CI | no software adapter allowed | negative probe only | `BLOCKED` unless a labeled GPU runner is attached |

Do not mark a lane `PASS` from a headless or software-adapter run. A lane is
accepted only after the same sidecar artifact and protocol version used for the
release have passed the runtime checks above.
