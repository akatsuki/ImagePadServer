# T4-ART-02: GPU-compatible cover formula

The WGSL artwork tile computes normalized coordinates from the destination
rectangle, then center-crops by aspect ratio:

```text
tileAspect   = tileWidth / tileHeight
sourceAspect = sourceWidth / sourceHeight
tileU = (dstX - tileX) / tileWidth
tileV = (dstY - tileY) / tileHeight
if sourceAspect > tileAspect:
    u = (tileU - .5) * tileAspect / sourceAspect + .5
    v = tileV
else:
    u = tileU
    v = (tileV - .5) * sourceAspect / tileAspect + .5
u,v = clamp(u,v,0,1)
```

The CPU helper must evaluate this at pixel centers (`x+0.5`, `y+0.5`) using
float64, clamp before converting to source texels, and use the same sampler
policy as the GPU artwork sampler (linear, clamp-to-edge). Do not use integer
crop rounding or a separate `scaleCover` branch.

## Golden cases

1. 4:3 source into 16:9 tile: horizontal crop; center source columns are
   retained, left/right UVs are clipped symmetrically.
2. 16:9 source into 1:1 tile: vertical crop; center source rows are retained.
3. Equal aspect: UV is exactly tile UV at all four pixel centers.
4. One-pixel tile and odd dimensions: no divide-by-zero; edge samples clamp.
5. Transparent source: RGB remains premultiplied and alpha follows the same
   edge fade (`smoothstep(0, .025, min edge UV)`) as WGSL.

Acceptance compares CPU helper UVs against a reference implementation of the
formula at every destination pixel (maximum absolute error <= 1e-6 before
texture filtering), then compares isolated CPU/GPU artwork readback with crop
bounds <=1px, IoU >=.99, MAE<=1, RMSE<=2. Text overlay metrics must remain
unchanged and passing.
