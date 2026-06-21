# Visualizer Adaptive Colors Design

Date: 2026-06-20  
Status: Approved

## 1. Goal

Make the audio visualizer readable over arbitrary artwork without losing its
artwork-driven identity. Primary UI text uses an achromatic, contrast-safe
foreground. Decorative and data-visualization elements use a restrained accent
derived from the artwork.

This replaces the single global complementary foreground contract. It also
eliminates the undefined-hue behavior that turns muted backgrounds navy.

## 2. Scope

The change applies to the completed visualizer base and all elements currently
colored with `ForegroundMode.Color`.

- Primary UI: title, artist, album, elapsed time, and total time.
- Accent UI: spectrum bars, waveform, detailed and smoothed loudness curves,
  graph guides, progress track and marker, fallback fingerprint, and fallback
  music note.
- Readability overlay: remains a single full-frame white or black overlay with
  opacity capped at the existing 35 percent.

Layout, animation, audio analysis, font rendering, and HLS encoding are outside
this change.

## 3. Color roles

Replace the single foreground color with two explicit roles:

```go
type ForegroundMode struct {
    PrimaryColor color.RGBA
    AccentColor  color.RGBA
    Overlay      color.RGBA
}
```

`PrimaryColor` is always opaque black or white. `AccentColor` is opaque and may
be chromatic. Every renderer must use the role assigned in section 2; callers
must not choose between the roles independently.

## 4. Source images and ordering

Generate both color roles before drawing the foreground artwork, text, or
graphs.

1. Scale and center-crop the source artwork to the output aspect ratio.
2. From that crop, create the existing strongly blurred full-frame background.
3. Independently downsample the crop to a small analysis image and apply only a
   light blur. This analysis image supplies artwork hue and chroma; it must not
   include the readability overlay.
4. Select the overlay and both foreground colors.
5. Apply the overlay to the strong-blur background.
6. Draw artwork, text, and visualizer elements using their assigned roles.

The analysis image prevents the strong background blur from collapsing most
artwork into the same near-neutral average. The final composited background,
not the analysis image, remains the authority for legibility.

Fallback artwork follows the same pipeline. Its deterministic palette image is
the source for both the strong-blur background and the light-blur analysis
image.

## 5. Artwork accent extraction

Downsample the cropped artwork to fit within 32 by 32 pixels in sRGB, preserving
aspect ratio, then apply one three-by-three box-blur pass. For the canonical
16:9 crop this produces a 32 by 18 analysis image.

Convert samples to OKLCH and reject:

- pixels with alpha below 128;
- pixels with OKLCH lightness below 0.08 or above 0.92;
- pixels with OKLCH chroma below 0.04.

Group the remaining samples into 24 deterministic 15-degree hue bins. Give each
pixel weight `1 + min(C, 0.12) / 0.12`, so chroma can at most double a pixel's
contribution and a tiny saturated logo cannot dominate a large, moderately
colored cover. Select the highest-weight bin. Ties select the lower bin index.
Compute its circular mean hue and weighted mean chroma using the same weights.

Rotate the selected hue by 180 degrees. Set the initial accent chroma to
`min(weightedMeanChroma, 0.12)`; do not impose a positive chroma floor on
neutral artwork. The analysis size, blur kernel, bin count, rejection
thresholds, weighting cap, and chroma ceiling must be named constants and
locked by tests.

If no chromatic samples remain, the artwork has no meaningful accent hue and
`AccentColor` must equal `PrimaryColor`.

## 6. Overlay and primary selection

Evaluate black text with a white overlay and white text with a black overlay.
Search overlay opacity from zero through the existing 35 percent cap in
deterministic five-percentage-point increments.

For each pair, measure the worst pixel-level WCAG contrast in both the metadata
and graph regions after compositing the candidate overlay. Prefer a pair that
reaches 4.5:1 in both regions at the lowest opacity. If both pass at the same
opacity, choose the pair with the higher worst-region contrast.

If neither reaches 4.5:1 within the cap, keep the cap and choose the pair with
the higher worst-region contrast. Do not increase the overlay beyond 35
percent.

After selecting the minimum overlay required by `PrimaryColor`, attempt accent
selection. If no chromatic accent passes, increase the selected overlay in the
same five-percentage-point increments and retry, stopping at the first opacity
where both color roles pass. The overlay color and primary color do not change.
If no accent passes at 35 percent, keep the primary selection and use
`PrimaryColor` as the accent.

## 7. Accent lightness selection

Keep the hue selected from the light-blur artwork analysis. Search OKLCH
lightness from 0.20 through 0.90 in deterministic 0.01 increments. This avoids
nominally chromatic candidates that are visually indistinguishable from black
or white. For each
lightness, reduce chroma from its initial value in 0.005 increments until the
candidate is inside the sRGB gamut. When the source accent chroma is at least
0.04, do not reduce display chroma below 0.04; otherwise use the source chroma
as the lower bound. Test the unclamped linear-sRGB components for gamut
membership; do not accept a candidate merely because its converted 8-bit
channels were clipped.

Evaluate every candidate against the same final background produced by the
selected primary overlay. A candidate passes only if it reaches 4.5:1 in every
pixel of the `Spectrum`, `Loudness`, `Progress`, and `Time` layout rectangles.
The `Time` rectangle is included because the progress marker can visually meet
the time label at small output sizes. Among passing candidates, prefer:

1. the candidate with lightness closest to 1.0 when `PrimaryColor` is white, or
   closest to 0.0 when `PrimaryColor` is black;
2. then the candidate with higher retained chroma;
3. then the lower numeric lightness for deterministic tie-breaking.

This produces a light, pastel-like accent over a darkened background and a dark,
muted accent over a lightened background. Pastel lightness is not forced because
it is illegible over light backgrounds.

If no chromatic candidate passes, set `AccentColor` equal to `PrimaryColor`.

## 8. Stability and accessibility

One `ForegroundMode` is selected per render job and remains unchanged for the
complete track. Colors must not react per frame.

Primary information never depends on hue. The accent supplements shape,
position, and motion already present in the graphs; it does not become the sole
carrier of information.

All calculations use sRGB input and output. Conversion and gamut handling must
be deterministic across supported platforms.

## 9. Compatibility and migration

- Remove the current neutral-background shortcut only after the new extraction
  path and role split are covered by tests. Its intent remains represented by
  the no-chromatic-samples fallback.
- Update every `mode.Color` use to the role explicitly assigned in section 2.
- Preserve existing per-element alpha values; only the RGB source changes.
- Keep `SelectForegroundMode` only if another caller still needs it. Otherwise,
  remove the duplicate selection path rather than maintaining two policies.
- Update the earlier rendering-corrections specification by treating this
  document as the superseding color contract.

## 10. Verification

Unit tests must cover:

- dark, light, mid-gray, warm-gray, and cool-gray backgrounds;
- saturated red, green, and blue artwork;
- muted artwork whose strong blur is neutral but whose source retains a usable
  hue;
- monochrome artwork, which must produce identical primary and accent colors;
- deterministic hue-bin selection and circular hue averaging near 0/360 degrees;
- rejection of borders and tiny saturated outliers;
- pixel-level 4.5:1 checks after the selected overlay;
- fallback when the 35 percent cap cannot reach 4.5:1;
- correct primary/accent role use by every renderer;
- deterministic output for identical input.

Runtime verification must extract decoded 360p, 720p, and 1080p frames from
encoded H.264 output. Fixtures must include colorful, muted, grayscale, dark,
and light artwork. Verification must confirm that text remains achromatic,
accent color varies across genuinely different artwork, no neutral input
defaults to navy, and the artwork remains visible beneath the capped overlay.
