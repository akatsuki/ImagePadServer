# V2 YUV rendering and transport contract

The GPU compositor must perform RGBA-to-YUV420 pixel conversion on the GPU.
Readback into staging memory is permitted solely to cross the sidecar/FFmpeg
process boundary; it is transport, not CPU rendering. CPU-side color matrix,
chroma resampling, compositing, or other pixel generation is forbidden.

Required evidence for the direct path:

- resource receipts identify the adapter, backend, buffer IDs, strides, and
  frame sequence;
- the production receipt records `OwnedByTransport` after GPU compute;
- CPU reference mode remains available only for golden comparison;
- the readback stage may only pack the computed plane bytes; it must not alter
  pixel values;
- odd width/height uses `(width+1)/2` and `(height+1)/2` chroma dimensions;
- the final stream is `yuv420p`, BT.709, limited range, with matching PTS.

External-memory zero-copy into NVENC/AMF/VideoToolbox is a later optimization.
It is not part of the visual-parity GO gate because FFmpeg currently runs as a
separate process and the product requirement is GPU rendering, not mandatory
zero-copy encoding.
