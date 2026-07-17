# GPU YUV420 transport contract

Status: design locked; implementation pending.

The production GPU music route must not compute RGB-to-YUV on the CPU. RGBA
readback remains a diagnostic/reference format only. A production frame must
carry either a GPU-produced planar YUV420P payload or a verified hardware
surface import.

## Payload

The sidecar response adds a negotiated `Yuv420p` frame variant with:

- `width`, `height`;
- `y_stride`, `uv_stride`;
- `chroma_width = ceil(width/2)`, `chroma_height = ceil(height/2)`;
- `format = yuv420p`, `color_space = bt709_limited`;
- separate Y, U, and V planes (or one contiguous payload with explicit offsets).

Rows may be padded for GPU alignment. The Go transport may copy/strip row
padding, but must not calculate color values.

## GPU conversion

After RGBA composition, a wgpu compute pass writes:

```
Y = 16  + (66R + 129G + 25B  + 128) >> 8
U = 128 + (-38R - 74G + 112B + 128) >> 8
V = 128 + (112R - 94G - 18B + 128) >> 8
```

U/V use the average of each 2x2 RGB block. Odd edges replicate the last valid
pixel, matching the current reference implementation exactly. Results are
clamped to the legal limited-range byte interval.

## Capability and failure policy

- The hello/capability handshake must advertise GPU YUV support.
- Production requests explicitly require `yuv420p`.
- If the compute pass or capability negotiation fails, the route fails closed
  with `gpu_output_format_required`; it must not silently call
  `RGBA8ToYUV420P`/`rgbaToYUV420p`.
- RGBA output remains available only under an explicit diagnostic/reference
  flag.

## Acceptance

- protocol round-trip and stride/payload validation;
- black, white, and primary-color coefficient vectors;
- odd 3x3 dimensions with edge replication;
- Rust compute smoke test on NVIDIA and AMD/iGPU;
- integration assertion that production YUV frames do not invoke a CPU color
  converter;
- 150-frame PTS/count/duration and MAE/RMSE gate after integration.
