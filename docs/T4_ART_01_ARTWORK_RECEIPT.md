# T4-ART-01: ArtworkReceipt schema

This is a transport/evidence change only. It does not select the GPU
production route or alter the existing artwork shader.

## Go request fields

Extend `ArtworkMetadata` (or add an adjacent `ArtworkTransform`) with:

```text
source_hash       string  // SHA-256 of decoded source RGBA bytes
texture_id        string
source_width      uint32
source_height     uint32
dest_x,dest_y     int32   // canonical screen origin
dest_width       uint32
dest_height      uint32
crop_mode        enum    // cover | contain | stretch
blur_radius      uint16
blur_strength    float32
overlay_rgba     [4]uint8
format           enum    // rgba8
color_space      enum    // srgb
premultiplied    bool
payload_hash     string  // hash of exact bytes sent, if payload is present
transform_version string
```

`ArtworkMetadata` remains backward compatible; absent transform fields mean
the legacy defaults. `payload_hash` is not inferred from `source_hash`.

## Rust receipt

Add to `GpuFrame`:

```text
artwork_receipt: Option<ArtworkReceipt>
```

The receipt echoes immutable request facts only:

```text
source_hash, payload_hash, texture_id,
source_width, source_height, dest_x, dest_y,
dest_width, dest_height, crop_mode,
blur_radius, blur_strength, overlay_rgba,
format, color_space, premultiplied, transform_version
```

It must not claim readback parity. A future artwork probe may add
`readback_hash` separately.

## Validation

- SHA fields are empty or exactly 64 lowercase hexadecimal characters.
- Source and destination dimensions are non-zero and within existing maximums.
- `dest_x/dest_y` may be negative only when the clipped destination remains
  representable; clipping is recorded by the probe, not silently in receipt.
- `row_stride >= source_width * 4` and payload length equals
  `row_stride * source_height` when payload is present.
- `blur_strength` is finite and in `[0,1]`; `blur_radius` has a fixed upper
  bound of 256.
- Only RGBA8/sRGB/premultiplied combinations are accepted in v1.
- `transform_version` is non-empty and bounded to 32 bytes.

## Wiring and acceptance

1. Go JSON encode/decode roundtrip preserves every field.
2. Rust validation rejects malformed hashes, dimensions, stride, NaN/Inf
   blur values, unsupported format/color-space, and payload length mismatch.
3. `gpu_render.rs` constructs `ArtworkReceipt` at upload time and returns it
   in `GpuFrame`; no shader or route selection changes.
4. Compare CLI records request and receipt hashes/transform fields and marks
   mismatch as diagnostic evidence only.
5. Existing scenes without transform metadata serialize and render unchanged.
6. `go test ./internal/video ./cmd/music-render-compare` and
   `cargo test --manifest-path gpu/playlist-compositord/Cargo.toml` pass.

The production GPU route and ASS ownership remain untouched until a later
isolated artwork readback gate passes.
