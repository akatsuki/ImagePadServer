# V2 YUV rendering and transport contract

The GPU compositor must perform RGBA-to-YUV420 pixel conversion on the GPU.
The final path must keep the completed frame in VRAM and pass a GPU-owned surface
or plane directly to a GPU-capable encoder. CPU readback is not part of the final
architecture. A staging readback may exist only as a temporary compatibility
transport while FFmpeg remains a separate process; it is not a GO state.
CPU-side color matrix, chroma resampling, compositing, or other pixel generation
is forbidden.

Required evidence for the direct path:

- resource receipts identify the adapter, backend, buffer IDs, strides, and
  frame sequence;
- the current compatibility receipt records `OwnedByTransport` after GPU compute;
- the final production receipt records a GPU-owned surface/plane imported by the encoder (a new ownership value or equivalent external-memory receipt is required);
- CPU reference mode remains available only for golden comparison;
- the final production receipt proves a VRAM-owned output and direct encoder handoff;
- any temporary compatibility readback may only pack the computed plane bytes and
  must not alter pixel values;
- odd width/height uses `(width+1)/2` and `(height+1)/2` chroma dimensions;
- the final stream is `yuv420p`, BT.709, limited range, with matching PTS.

External-memory or shared-device interop into NVENC/AMF/VideoToolbox is required
for the final direct-handoff path. FFmpeg's current separate-process pipe is a
temporary integration limitation and does not satisfy the final VRAM-to-encoder
requirement.
