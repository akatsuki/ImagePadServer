# T5-1 overlay multiframe boundary

The compare CLI currently constructs one canonical scene at frame index 0 for
static payload evidence, while HLS screenshots are produced later from the
rendered playlist. A truthful start/mid/end overlay probe must construct three
independent payloads:

1. `CanonicalMusicScene(inputSpec, frameIndex, ptsNS)` at indices 0, midpoint,
   and final canonical frame (`ceil(duration*FPS)-1`).
2. Pass each payload to `ProbeGPUSceneTextOverlayComposite` with a bounded
   three-second context.
3. Rasterize each payload with `RenderTextOverlayScreenRGBA` and compare the
   GPU readback using `CompareOverlayParityCPUImageGPUImage` for every role
   crop.

The existing single-scene evidence must remain compatible; multiframe results
belong in a separate `textOverlayParityByPoint` map. Do not duplicate one
metric across points, because time text and dynamic scene metadata differ.
