# Playlist GPU rendering baseline (T0)

This document records the pre-wgpu rendering contract and the evidence required
before enabling the GPU compositor. It is intentionally a baseline, not a GPU
implementation specification.

## Current CPU reference path

The existing Go renderer in `internal/video` remains the visual reference while
the GPU path is introduced. The reference includes:

- artwork/background compositing and deterministic fallback artwork;
- audio visualizer layers (spectrum, loudness, adaptive colour and text);
- 30 fps media timing and the existing H.264/AAC output contracts;
- the golden fixture `internal/video/testdata/golden/fallback-720.png`.

`TestRenderFallbackArtworkGolden` is build-specific and may skip when the local
FFmpeg/font toolchain is unavailable. A skipped test is not GPU compatibility
evidence; record the reason in the run log.

## Capability matrix

| Capability | Required policy | Evidence | Result states |
|---|---|---|---|
| Discrete hardware adapter | Prefer when present | wgpu adapter report | supported / unavailable |
| Integrated hardware adapter | Accept as a first-class adapter | wgpu adapter report | supported / unavailable |
| Software/CPU adapter | Never select for production rendering | adapter backend/type report | rejected |
| GPU device creation | Must succeed before music/playlist rendering | sidecar health handshake | ready / unavailable |
| GPU readback/staging | Must satisfy the bounded frame contract | row-pitch/format probe | ready / incompatible |
| CPU reference renderer | Test oracle only; not a runtime fallback | Go golden tests | test-only |

The matrix must record OS, adapter name, backend, driver, renderer version,
output dimensions and the exact test command. Do not infer hardware support
from a build succeeding: a runtime adapter report is required.

## Golden fixture protocol

Before GPU work changes the output contract, capture the current reference
results with:

```text
go test ./internal/video -run '^TestRenderFallbackArtworkGolden$' -count=1 -v
go test ./internal/video -run 'Test(AudioVisualizer|Visualizer|Fallback)' -count=1
```

The fixture is compared byte-for-byte where the existing test permits it. GPU
comparisons will use the scene contract and a documented per-channel tolerance;
they must not silently replace or regenerate the CPU fixture.

## No-hardware and software-adapter rules

The compositor must fail closed when no discrete or integrated hardware adapter
is available. It must also fail closed when wgpu exposes only a software/CPU
adapter, even if that adapter can create a device. The error must be surfaced as
the stable `gpu_renderer_unavailable` condition for music/playlist rendering.

Image-only server startup is independent and may continue without a rendering
adapter. There is no production CPU-rendering mode.

## Baseline completion checklist

- [x] Existing CPU renderer and golden fixture identified.
- [x] Output/timing/codec contracts recorded.
- [x] Discrete, integrated and software adapter policy recorded.
- [ ] Run and attach a real hardware adapter report for each supported CI/manual
      environment (owned by T1/T7 once the sidecar exists).
- [ ] Add the wgpu no-adapter/software rejection test (owned by T1).
