# Music renderer fixtures

`manifest.json` is the versioned CPU reference inventory.  It deliberately records
inputs, deterministic seeds, metadata and boundary timestamps without embedding
machine-specific paths.  Canonical rectangles are copied from `LayoutForSize`.

Generate deterministic PCM/WAV inputs and a feature-seed report with:

```text
go run ./cmd/music-fixture -manifest testdata/music-render/manifest.json -out .tmp/music-fixtures
```

The generator never invokes the GPU or changes production rendering.  A golden
render is only valid when the report contains the manifest SHA-256, Go version,
FFmpeg version and CPU renderer commit.  Missing fonts, FFmpeg, or artwork are
reported as unavailable rather than silently accepted.
