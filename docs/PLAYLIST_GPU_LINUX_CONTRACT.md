# Linux GPU contract evidence

The Linux contract lane was executed in the Linux Docker image on 2026-07-14.

## Result

- Docker build: PASS
- Rust tests: PASS (15 tests)
- Release sidecar: PASS (`playlist-compositord 0.1.0`)
- Software-adapter negative probe: PASS

The probe sent `hello`, `health`, and `shutdown` requests. The sidecar returned:

```json
{"type":"error","code":"gpu_renderer_unavailable","message":"no hardware GPU adapter"}
{"type":"health","ready":false,"protocol":1}
{"type":"bye"}
```

This is intentional evidence that a Linux software adapter is rejected. It is
not a Linux hardware-GPU acceptance result; that still requires a Vulkan-capable
Linux runner.
