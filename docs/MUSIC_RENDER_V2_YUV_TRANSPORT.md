# V2 YUV transport acceptance contract

The GPU compositor must expose Y, U, and V as GPU-owned storage resources to
the encoder bridge. The production path must not create `MAP_READ` buffers or
copy the planes into a CPU byte slice before encoding.

Required evidence for the direct path:

- resource receipts identify the adapter, backend, buffer IDs, strides, and
  frame sequence;
- the encoder bridge consumes the same GPU resources or an external-memory
  handle without a readback;
- CPU reference mode remains available only for golden comparison;
- a diagnostic build may read back planes, but its receipt must be marked
  `DiagnosticOnly` and cannot satisfy the NativeGPU GO gate;
- odd width/height uses `(width+1)/2` and `(height+1)/2` chroma dimensions;
- the final stream is `yuv420p`, BT.709, limited range, with matching PTS.

The current experimental implementation is intentionally not accepted: it
still allocates `MAP_READ` staging buffers and returns CPU-owned plane vectors.
