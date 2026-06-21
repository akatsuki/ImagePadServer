package video

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
)

// maxOverlayOpacity caps the readability-overlay opacity. The strong
// background blur already flattens the artwork, so a heavy overlay washes it
// out to a pale field and hides the thumbnail. Keeping the cap low preserves
// the visible blurred background while the complementary foreground supplies
// most of the contrast. Contrast may fall slightly below 4.5:1 on busy
// backgrounds, which is an accepted trade-off for blur visibility.
const maxOverlayOpacity = 0.35

// srgbLinearizeLUT is a 256-entry lookup table for srgbLinearize(v/255.0).
// It eliminates math.Pow calls in the hot path of regionContrastOK.
var srgbLinearizeLUT [256]float64

func init() {
	for i := 0; i < 256; i++ {
		srgbLinearizeLUT[i] = srgbLinearize(float64(i) / 255.0)
	}
}

// ---------------------------------------------------------------------------
// ComplementaryForeground
// ---------------------------------------------------------------------------

// ComplementaryForeground derives a global foreground colour from the blurred
// full-frame background by computing the complementary hue in OKLCH and
// searching OKLCH lightness in 0.01 increments for a variant that passes
// WCAG 4.5:1 in both the metadata and bottom-graph regions.
//
// Unlike the naive approach of checking contrast against raw background
// pixels, this function searches overlay opacity first — for each chromatic
// OKLCH candidate it composites the candidate over the background at various
// overlay opacities (0–60 %, 5‑point steps) and evaluates WCAG contrast
// against the composited result.  This allows candidates that would fail raw
// contrast to pass once the overlay is applied.
//
//  1. Pixels in metadataRect and graphRect are sampled and their average sRGB
//     colour computed.
//  2. The average is converted to OKLCH, hue rotated 180°, chroma clamped to
//     [0.05, 0.18].
//  3. Lightness is searched in 0.01 increments from 0.01 to 0.99.
//  4. For each lightness candidate, overlay opacity is searched from 0 % to
//     60 % in 5‑point steps.  The first opacity that achieves WCAG >= 4.5:1
//     in both regions (after Porter‑Duff Over compositing) is recorded.
//  5. The candidate whose L is closest to the original complement L is
//     returned together with its overlay.
//  6. If no chromatic candidate passes with any overlay, the function falls
//     back to white or black with the lowest overlay opacity that reaches
//     4.5:1 in both regions.
func ComplementaryForeground(bg image.Image, metadataRect, graphRect image.Rectangle) ForegroundMode {
	avg := averageColorForRegions(bg, metadataRect, graphRect)

	oklch := SRGBToOKLCH(avg)
	comp := oklch.RotateHue(180).ClampChroma(0.05, 0.18)
	origL := comp.L

	type chromaPair struct {
		color   color.RGBA
		overlay color.RGBA
	}
	var best chromaPair
	bestDiff := math.MaxFloat64

	for l := 0.01; l < 1.0; l += 0.01 {
		cand := OKLCH{L: l, C: comp.C, H: comp.H}
		rgba := OKLCHToSRGB(cand)
		fgLum := srgbLuminance(rgba)

		// Pick overlay colour: dark overlay for light text, light overlay
		// for dark text.
		var overlayColor color.RGBA
		if fgLum > 0.5 {
			overlayColor = color.RGBA{R: 0, G: 0, B: 0, A: 255}
		} else {
			overlayColor = color.RGBA{R: 255, G: 255, B: 255, A: 255}
		}

		for opacity := 0.0; opacity <= maxOverlayOpacity; opacity += 0.05 {
			roundedAlpha := uint8(math.Round(opacity * 255))
			actualAlpha := float64(roundedAlpha) / 255.0

			metaOK := regionContrastOK(bg, metadataRect, overlayColor, actualAlpha, fgLum, 4.5)
			graphOK := regionContrastOK(bg, graphRect, overlayColor, actualAlpha, fgLum, 4.5)
			if metaOK && graphOK {
				diff := math.Abs(l - origL)
				if diff < bestDiff {
					best = chromaPair{
						color:   rgba,
						overlay: color.RGBA{R: overlayColor.R, G: overlayColor.G, B: overlayColor.B, A: roundedAlpha},
					}
					bestDiff = diff
				}
				break
			}
		}
	}

	if bestDiff != math.MaxFloat64 {
		return ForegroundMode{Color: best.color, Overlay: best.overlay}
	}

	return fallbackWithOverlay(bg, metadataRect, graphRect)
}

// fallbackWithOverlay tries black and white at increasing overlay opacities
// (0–60 %, 5‑point steps) and picks the best pair that achieves WCAG
// >= 4.5:1 in both regions.  When both colours pass, the one with the
// higher minimum contrast ratio at 60 % opacity wins and its lowest
// passing overlay opacity (0–60 %) is used.  When neither reaches 4.5:1
// the colour with the higher minimum contrast ratio at 60 % opacity is used.
func fallbackWithOverlay(bg image.Image, metadataRect, graphRect image.Rectangle) ForegroundMode {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}

	type attempt struct {
		mode    ForegroundMode
		ratio60 float64
		passed  bool
	}

	// tryFg evaluates the given foreground colour with its corresponding
	// overlay colour (dark overlay for light fg, light overlay for dark fg)
	// across the opacity range.
	tryFg := func(fg, overlay color.RGBA) attempt {
		fgLum := srgbLuminance(fg)

		// Always compute the minimum contrast ratio at 60 % opacity for
		// selection (used when both black and white pass).
		mr := minRegionContrast(bg, metadataRect, overlay, maxOverlayOpacity, fgLum)
		gr := minRegionContrast(bg, graphRect, overlay, maxOverlayOpacity, fgLum)
		ratio60 := mr
		if gr < mr {
			ratio60 = gr
		}

		for opacity := 0.0; opacity <= maxOverlayOpacity; opacity += 0.05 {
			roundedAlpha := uint8(math.Round(opacity * 255))
			actualAlpha := float64(roundedAlpha) / 255.0
			if regionContrastOK(bg, metadataRect, overlay, actualAlpha, fgLum, 4.5) &&
				regionContrastOK(bg, graphRect, overlay, actualAlpha, fgLum, 4.5) {
				return attempt{
					mode: ForegroundMode{
						Color:   fg,
						Overlay: color.RGBA{R: overlay.R, G: overlay.G, B: overlay.B, A: roundedAlpha},
					},
					ratio60: ratio60,
					passed:  true,
				}
			}
		}
		return attempt{ratio60: ratio60}
	}

	// White fg → dark overlay.
	aWhite := tryFg(white, color.RGBA{R: 0, G: 0, B: 0, A: 255})
	// Black fg → light overlay.
	aBlack := tryFg(black, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	switch {
	case aWhite.passed && aBlack.passed:
		// Both pass: pick the one with higher minimum contrast at 60 %.
		if aWhite.ratio60 >= aBlack.ratio60 {
			return aWhite.mode
		}
		return aBlack.mode
	case aWhite.passed:
		return aWhite.mode
	case aBlack.passed:
		return aBlack.mode
	default:
		cappedA := uint8(math.Round(maxOverlayOpacity * 255))
		if aWhite.ratio60 >= aBlack.ratio60 {
			return ForegroundMode{
				Color:   white,
				Overlay: color.RGBA{R: 0, G: 0, B: 0, A: cappedA},
			}
		}
		return ForegroundMode{
			Color:   black,
			Overlay: color.RGBA{R: 255, G: 255, B: 255, A: cappedA},
		}
	}
}

// averageColorForRegions computes the mean sRGB colour across all pixels in the
// given regions of img. Returns an opaque colour.
func averageColorForRegions(img image.Image, rects ...image.Rectangle) color.RGBA {
	var sumR, sumG, sumB float64
	var n int

	for _, rect := range rects {
		rect = rect.Intersect(img.Bounds())
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				sumR += float64(r >> 8)
				sumG += float64(g >> 8)
				sumB += float64(b >> 8)
				n++
			}
		}
	}

	if n == 0 {
		return color.RGBA{R: 128, G: 128, B: 128, A: 255}
	}

	return color.RGBA{
		R: uint8(math.Round(sumR / float64(n))),
		G: uint8(math.Round(sumG / float64(n))),
		B: uint8(math.Round(sumB / float64(n))),
		A: 255,
	}
}

// ---------------------------------------------------------------------------
// ForegroundMode
// ---------------------------------------------------------------------------

// ForegroundMode holds the global foreground colour and the readability-overlay
// colour (including its final alpha) that the renderer applies for the entire
// frame.
type ForegroundMode struct {
	PrimaryColor color.RGBA
	AccentColor  color.RGBA
	Overlay      color.RGBA

	// Color is retained during the renderer migration and mirrors AccentColor.
	// New code must use the explicit role fields.
	Color color.RGBA
}

// AdaptiveForeground selects a monochrome primary foreground and an
// artwork-derived accent. Both are validated against the same capped
// readability overlay, so hue comes from the artwork while legibility remains
// tied to the pixels that are actually displayed.
func AdaptiveForeground(background, accentSource image.Image, primaryRects, accentRects []image.Rectangle) ForegroundMode {
	primary, overlay := selectPrimaryAndOverlay(background, primaryRects)
	mode := ForegroundMode{PrimaryColor: primary, AccentColor: primary, Overlay: overlay, Color: primary}

	accent, ok := artworkAccent(accentSource)
	if !ok {
		return mode
	}
	if selected, ok := selectAccentColor(background, accentRects, overlay, primary, accent); ok {
		mode.AccentColor = selected
		mode.Color = selected
		return mode
	}

	startStep := int(math.Round((float64(overlay.A) / 255) / 0.05))
	maxStep := int(math.Round(maxOverlayOpacity / 0.05))
	for step := startStep + 1; step <= maxStep; step++ {
		candidateOverlay := overlay
		candidateOverlay.A = uint8(math.Round(float64(step) * 0.05 * 255))
		if !allRegionsContrast(background, primaryRects, candidateOverlay, primary, 4.5) {
			continue
		}
		selected, found := selectAccentColor(background, accentRects, candidateOverlay, primary, accent)
		if found {
			mode.Overlay = candidateOverlay
			mode.AccentColor = selected
			mode.Color = selected
			return mode
		}
	}
	return mode
}

func selectAccentColor(background image.Image, accentRects []image.Rectangle, overlay, primary color.RGBA, accent OKLCH) (color.RGBA, bool) {
	targetL := 0.0
	if primary.R == 255 {
		targetL = 1.0
	}
	bestDiff := math.MaxFloat64
	bestChroma := -1.0
	var best color.RGBA
	found := false

	for li := int(math.Round(artworkAccentMinLightness * 100)); li <= int(math.Round(artworkAccentMaxLightness*100)); li++ {
		l := float64(li) / 100
		minDisplayChroma := math.Min(accent.C, artworkAccentMinDisplayChroma)
		for c := accent.C; c >= minDisplayChroma-1e-9; c -= 0.005 {
			if c < 0 {
				c = 0
			}
			candidate := OKLCH{L: l, C: c, H: accent.H}
			if !oklchInSRGB(candidate) {
				continue
			}
			rgba := OKLCHToSRGB(candidate)
			if !allRegionsContrast(background, accentRects, overlay, rgba, 4.5) {
				break
			}
			diff := math.Abs(l - targetL)
			if !found || diff < bestDiff-1e-9 || (math.Abs(diff-bestDiff) <= 1e-9 && c > bestChroma) {
				best = rgba
				bestDiff = diff
				bestChroma = c
				found = true
			}
			break
		}
	}

	if found {
		return best, true
	}
	return color.RGBA{}, false
}

func selectPrimaryAndOverlay(background image.Image, rects []image.Rectangle) (color.RGBA, color.RGBA) {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	type choice struct {
		fg      color.RGBA
		overlay color.RGBA
		opacity float64
		worst   float64
		passed  bool
	}

	evaluate := func(fg, overlayBase color.RGBA) choice {
		cappedOverlay := overlayBase
		cappedOverlay.A = uint8(math.Round(maxOverlayOpacity * 255))
		result := choice{fg: fg, overlay: cappedOverlay, opacity: maxOverlayOpacity}
		for step := 0; step <= int(math.Round(maxOverlayOpacity/0.05)); step++ {
			opacity := float64(step) * 0.05
			overlay := overlayBase
			overlay.A = uint8(math.Round(opacity * 255))
			if allRegionsContrast(background, rects, overlay, fg, 4.5) {
				result.overlay = overlay
				result.opacity = opacity
				result.passed = true
				break
			}
		}
		result.worst = worstRegionsContrast(background, rects, result.overlay, fg)
		return result
	}

	light := evaluate(white, color.RGBA{R: 0, G: 0, B: 0, A: 255})
	dark := evaluate(black, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	selected := light
	switch {
	case light.passed && dark.passed:
		if dark.opacity < light.opacity || (math.Abs(dark.opacity-light.opacity) < 1e-9 && dark.worst > light.worst) {
			selected = dark
		}
	case dark.passed:
		selected = dark
	case !light.passed && dark.worst > light.worst:
		selected = dark
	}
	return selected.fg, selected.overlay
}

func allRegionsContrast(background image.Image, rects []image.Rectangle, overlay, fg color.RGBA, ratio float64) bool {
	fgLum := srgbLuminance(fg)
	alpha := float64(overlay.A) / 255
	for _, rect := range rects {
		if !regionContrastOK(background, rect, overlay, alpha, fgLum, ratio) {
			return false
		}
	}
	return true
}

func worstRegionsContrast(background image.Image, rects []image.Rectangle, overlay, fg color.RGBA) float64 {
	worst := math.MaxFloat64
	alpha := float64(overlay.A) / 255
	fgLum := srgbLuminance(fg)
	for _, rect := range rects {
		ratio := minRegionContrast(background, rect, overlay, alpha, fgLum)
		if ratio < worst {
			worst = ratio
		}
	}
	return worst
}

// ---------------------------------------------------------------------------
// SelectForegroundMode
// ---------------------------------------------------------------------------

// SelectForegroundMode examines the blurred full-frame background and the two
// text/graph regions, then picks the global foreground mode that provides at
// least WCAG 4.5:1 contrast.
//
//  1. Average pixel luminance is measured in the metadata and graph regions.
//  2. If both regions average below 128 → light mode (white text, black
//     overlay starting at 36 %). Otherwise → dark mode (black text, white
//     overlay starting at 28 %).
//  3. The readability-overlay opacity is increased in 5‑percentage‑point
//     steps, up to 60 %, until both regions reach 4.5:1.
func SelectForegroundMode(background image.Image, metadataRect, graphRect image.Rectangle) ForegroundMode {
	metaLum := averageLuminance(background, metadataRect)
	graphLum := averageLuminance(background, graphRect)

	var fgColor, overlayColor color.RGBA
	var startOpacity float64

	if metaLum < 128 && graphLum < 128 {
		// Light mode — white text, black overlay.
		fgColor = color.RGBA{R: 255, G: 255, B: 255, A: 255}
		overlayColor = color.RGBA{R: 0, G: 0, B: 0, A: 255}
		startOpacity = 0.36
	} else {
		// Dark mode — black text, white overlay.
		fgColor = color.RGBA{R: 0, G: 0, B: 0, A: 255}
		overlayColor = color.RGBA{R: 255, G: 255, B: 255, A: 255}
		startOpacity = 0.28
	}

	// Find the lowest overlay opacity that passes 4.5:1.
	opacity := startOpacity
	fgLum := srgbLuminance(fgColor)

	for opacity <= 0.60 {
		metaOK := regionContrastOK(background, metadataRect, overlayColor, opacity, fgLum, 4.5)
		graphOK := regionContrastOK(background, graphRect, overlayColor, opacity, fgLum, 4.5)
		if metaOK && graphOK {
			break
		}
		opacity += 0.05
	}
	if opacity > 0.60 {
		opacity = 0.60
	}

	return ForegroundMode{
		Color:   fgColor,
		Overlay: color.RGBA{R: overlayColor.R, G: overlayColor.G, B: overlayColor.B, A: uint8(math.Round(opacity * 255))},
	}
}

// ---------------------------------------------------------------------------
// Luminance helpers (pixel-average and WCAG)
// ---------------------------------------------------------------------------

// averageLuminance returns the mean of (R+G+B)/3 across all pixels in the
// given rectangle of img.  Values are 0–255.
func averageLuminance(img image.Image, rect image.Rectangle) float64 {
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() {
		return 0
	}

	var sum float64
	n := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// RGBA() returns 16-bit values; shift right 8 to get 8‑bit.
			sum += float64((r>>8)+(g>>8)+(b>>8)) / 3.0
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// srgbLuminance computes the WCAG 2.1 relative luminance of an 8‑bit sRGB
// colour.
func srgbLuminance(c color.RGBA) float64 {
	r := srgbLinearize(float64(c.R) / 255.0)
	g := srgbLinearize(float64(c.G) / 255.0)
	b := srgbLinearize(float64(c.B) / 255.0)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

func srgbLinearize(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// wcagContrast returns the WCAG 2.1 contrast ratio between two luminances.
func wcagContrast(l1, l2 float64) float64 {
	if l1 > l2 {
		return (l1 + 0.05) / (l2 + 0.05)
	}
	return (l2 + 0.05) / (l1 + 0.05)
}

// regionContrastOK reports whether every pixel in the given rectangle, after
// the semi‑transparent overlay is applied, achieves at least minRatio:1
// contrast against the foreground colour.  The overlay is applied using
// Porter‑Duff Over compositing with integer arithmetic (matching
// draw.Draw with draw.Over) so that the check agrees with the actual
// rendered output.
func regionContrastOK(img image.Image, rect image.Rectangle, overlayColor color.RGBA, overlayAlpha, fgLum, minRatio float64) bool {
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() {
		return true
	}

	// Pre‑compute the integer alpha used by draw.Draw / draw.Over.
	alpha := uint8(math.Round(overlayAlpha * 255))
	invAlpha := 255 - alpha // 255 when overlayAlpha==0

	or := int(overlayColor.R) * int(alpha)
	og := int(overlayColor.G) * int(alpha)
	ob := int(overlayColor.B) * int(alpha)

	worst := 999.0 // sentinel — minimum contrast ratio found

	// Fast-path for *image.RGBA: pixel access via RGBAAt is faster than
	// the image.Image interface dispatch.
	if rgba, ok := img.(*image.RGBA); ok {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				c := rgba.RGBAAt(x, y)

				// Porter‑Duff Over with truncating integer division.
				effR := uint8((or + int(c.R)*int(invAlpha)) / 255)
				effG := uint8((og + int(c.G)*int(invAlpha)) / 255)
				effB := uint8((ob + int(c.B)*int(invAlpha)) / 255)

				lum := 0.2126*srgbLinearizeLUT[effR] + 0.7152*srgbLinearizeLUT[effG] + 0.0722*srgbLinearizeLUT[effB]
				ratio := wcagContrast(fgLum, lum)
				if ratio < worst {
					worst = ratio
				}
			}
		}
	} else {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				// 16‑bit → 8‑bit.
				br := int(r >> 8)
				bg := int(g >> 8)
				bb := int(b >> 8)

				// Porter‑Duff Over with truncating integer division.
				effR := uint8((or + br*int(invAlpha)) / 255)
				effG := uint8((og + bg*int(invAlpha)) / 255)
				effB := uint8((ob + bb*int(invAlpha)) / 255)

				lum := 0.2126*srgbLinearizeLUT[effR] + 0.7152*srgbLinearizeLUT[effG] + 0.0722*srgbLinearizeLUT[effB]
				ratio := wcagContrast(fgLum, lum)
				if ratio < worst {
					worst = ratio
				}
			}
		}
	}

	if worst == 999.0 {
		return true
	}

	return worst >= minRatio
}

// minRegionContrast computes the minimum WCAG contrast ratio for the given
// region after the semi‑transparent overlay is composited over the background,
// against the given foreground luminance.  This is the ratio-producing
// counterpart of regionContrastOK.
func minRegionContrast(img image.Image, rect image.Rectangle, overlayColor color.RGBA, overlayAlpha, fgLum float64) float64 {
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() {
		return 999.0
	}

	alpha := uint8(math.Round(overlayAlpha * 255))
	invAlpha := 255 - alpha
	or := int(overlayColor.R) * int(alpha)
	og := int(overlayColor.G) * int(alpha)
	ob := int(overlayColor.B) * int(alpha)

	worst := 999.0

	if rgba, ok := img.(*image.RGBA); ok {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				c := rgba.RGBAAt(x, y)
				effR := uint8((or + int(c.R)*int(invAlpha)) / 255)
				effG := uint8((og + int(c.G)*int(invAlpha)) / 255)
				effB := uint8((ob + int(c.B)*int(invAlpha)) / 255)
				lum := 0.2126*srgbLinearizeLUT[effR] + 0.7152*srgbLinearizeLUT[effG] + 0.0722*srgbLinearizeLUT[effB]
				ratio := wcagContrast(fgLum, lum)
				if ratio < worst {
					worst = ratio
				}
			}
		}
	} else {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				br := int(r >> 8)
				bg := int(g >> 8)
				bb := int(b >> 8)
				effR := uint8((or + br*int(invAlpha)) / 255)
				effG := uint8((og + bg*int(invAlpha)) / 255)
				effB := uint8((ob + bb*int(invAlpha)) / 255)
				lum := 0.2126*srgbLinearizeLUT[effR] + 0.7152*srgbLinearizeLUT[effG] + 0.0722*srgbLinearizeLUT[effB]
				ratio := wcagContrast(fgLum, lum)
				if ratio < worst {
					worst = ratio
				}
			}
		}
	}

	if worst == 999.0 {
		return 999.0
	}
	return worst
}

// srgbLuminance8 is a fast path that accepts 8‑bit R/G/B directly.
func srgbLuminance8(r, g, b float64) float64 {
	r = srgbLinearize(r / 255.0)
	g = srgbLinearize(g / 255.0)
	b = srgbLinearize(b / 255.0)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// ---------------------------------------------------------------------------
// PrepareVisualizerBase
// ---------------------------------------------------------------------------

// PrepareVisualizerBase renders the blurred full-frame background and the
// foreground artwork tile with its shadow, then saves the result as a PNG.
//
//  1. If artworkPath is non-empty, FFmpeg scales it with a cover operation to
//     fill the canvas and applies gblur=sigma=64.
//  2. If artworkPath is empty or invalid, the fallback *image.RGBA is written
//     to a temporary file and processed the same way.
//  3. The foreground artwork is scaled/cropped to fill the artwork rectangle,
//     masked with rounded corners and composited over the background together
//     with its shadow.
//  4. The result is saved to outPath and the selected ForegroundMode returned.
func PrepareVisualizerBase(ctx context.Context, ffmpeg, artworkPath string, fallback *image.RGBA, layout VisualizerLayout, outPath string) (ForegroundMode, error) {
	return prepareVisualizerBase(ctx, ffmpeg, artworkPath, fallback, nil, layout, outPath)
}

func prepareVisualizerBase(ctx context.Context, ffmpeg, artworkPath string, fallback *image.RGBA, fallbackRenderer func(color.RGBA) (*image.RGBA, error), layout VisualizerLayout, outPath string) (ForegroundMode, error) {
	// Derive canvas dimensions from the layout.
	canvasW := int(math.Round(float64(layout.Artwork.W) * 1280.0 / 288.0))
	canvasH := int(math.Round(float64(layout.Artwork.H) * 720.0 / 288.0))

	// -----------------------------------------------------------------------
	// 1. Blurred background
	// -----------------------------------------------------------------------
	tmpDir, err := os.MkdirTemp("", "imagepad-visualizer-bg-*")
	if err != nil {
		return ForegroundMode{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	sourcePath := artworkPath
	cleanupSource := false
	if sourcePath == "" {
		// Write the fallback tile to a temp PNG so FFmpeg can process it.
		sourcePath = filepath.Join(tmpDir, "fallback.png")
		if err := savePNG(sourcePath, fallback); err != nil {
			return ForegroundMode{}, fmt.Errorf("save fallback: %w", err)
		}
		cleanupSource = true
	}
	if cleanupSource {
		defer os.Remove(sourcePath)
	}

	blurredPath := filepath.Join(tmpDir, "blurred.png")

	// Use ffmpeg to scale with cover, center crop, then Gaussian blur.
	filter := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,gblur=sigma=64",
		canvasW, canvasH, canvasW, canvasH,
	)
	args := []string{
		"-y",
		"-i", sourcePath,
		"-vf", filter,
		"-frames:v", "1",
		blurredPath,
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	if output, err := CombinedOutputTrackedFFmpeg(cmd); err != nil {
		return ForegroundMode{}, fmt.Errorf("ffmpeg background blur failed: %w\n%s", err, string(output))
	}

	bg, err := loadPNG(blurredPath)
	if err != nil {
		return ForegroundMode{}, fmt.Errorf("load blurred background: %w", err)
	}
	bgRGBA := toRGBA(bg)

	var fgSrc image.Image
	if artworkPath != "" {
		fgSrc, err = loadAnyPNG(artworkPath)
		if err != nil {
			return ForegroundMode{}, fmt.Errorf("load artwork: %w", err)
		}
	} else {
		fgSrc = fallback
	}
	accentSource := scaleCover(fgSrc, canvasW, canvasH)
	primaryRects := []image.Rectangle{
		layoutImageRect(layout.Title),
		layoutImageRect(layout.Artist),
		layoutImageRect(layout.Album),
		layoutImageRect(layout.Time),
	}
	accentRects := []image.Rectangle{
		layoutImageRect(layout.Spectrum),
		layoutImageRect(layout.Loudness),
		layoutImageRect(layout.Progress),
		layoutImageRect(layout.Time),
	}
	mode := AdaptiveForeground(bgRGBA, accentSource, primaryRects, accentRects)
	if artworkPath == "" {
		fgSrc, err = finalizeFallbackSource(fallback, mode, fallbackRenderer)
		if err != nil {
			return ForegroundMode{}, fmt.Errorf("finalize fallback artwork: %w", err)
		}
	}
	// The readability overlay belongs to the background layer. Applying it
	// before the shadow and artwork keeps the foreground cover color-accurate.
	//
	// NOTE: we use color.NRGBA here because Go 1.26.3's drawFillOver has a
	// bug when passed a color.RGBA (non-premultiplied) source — it
	// under-composites the source colour, producing a result near the
	// original background instead of the expected Porter-Duff Over.
	draw.Draw(bgRGBA, bgRGBA.Bounds(), &image.Uniform{color.NRGBA{
		R: mode.Overlay.R,
		G: mode.Overlay.G,
		B: mode.Overlay.B,
		A: mode.Overlay.A,
	}}, image.Point{}, draw.Over)

	// -----------------------------------------------------------------------
	// 2. Foreground artwork — scale to fill artwork rect
	// -----------------------------------------------------------------------
	artRect := image.Rect(
		layout.Artwork.X, layout.Artwork.Y,
		layout.Artwork.X+layout.Artwork.W, layout.Artwork.Y+layout.Artwork.H,
	)

	fgScaled := scaleCover(fgSrc, artRect.Dx(), artRect.Dy())

	// -----------------------------------------------------------------------
	// 3. Shadow
	// -----------------------------------------------------------------------
	cr := int(math.Round(24.0 * float64(layout.Artwork.W) / 288.0))
	shadowBlur := int(math.Round(24.0 * float64(layout.Artwork.W) / 288.0))
	shadowOffY := int(math.Round(8.0 * float64(layout.Artwork.W) / 288.0))

	renderShadow(bgRGBA, artRect, cr, shadowBlur, shadowOffY)

	// -----------------------------------------------------------------------
	// 4. Rounded-corner mask for foreground artwork
	// -----------------------------------------------------------------------
	masked := applyRoundedCorners(fgScaled, cr)
	draw.Draw(bgRGBA, artRect, masked, image.Point{}, draw.Over)

	if err := savePNG(outPath, bgRGBA); err != nil {
		return ForegroundMode{}, fmt.Errorf("save output: %w", err)
	}

	return mode, nil
}

func finalizeFallbackSource(initial *image.RGBA, mode ForegroundMode, renderer func(color.RGBA) (*image.RGBA, error)) (*image.RGBA, error) {
	if renderer == nil {
		return initial, nil
	}
	return renderer(mode.AccentColor)
}

func layoutImageRect(r Rect) image.Rectangle {
	return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
}

// ---------------------------------------------------------------------------
// Image helpers
// ---------------------------------------------------------------------------

// savePNG writes img as a PNG file to path.
func savePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// loadPNG reads a PNG file from disk.
func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode PNG: %w", err)
	}
	return img, nil
}

// loadAnyPNG reads a PNG file (or other registered format) from disk.
func loadAnyPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return img, nil
}

// toRGBA converts any image.Image to an *image.RGBA, copying pixels.
func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

// scaleCover scales src to cover dstW×dstH while preserving aspect ratio,
// then center-crops to exactly dstW×dstH.  Uses Catmull‑Rom interpolation.
func scaleCover(src image.Image, dstW, dstH int) *image.RGBA {
	sb := src.Bounds()
	srcW := sb.Dx()
	srcH := sb.Dy()

	if srcW == 0 || srcH == 0 {
		return image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	}

	sf := math.Max(float64(dstW)/float64(srcW), float64(dstH)/float64(srcH))
	interW := int(math.Round(float64(srcW) * sf))
	interH := int(math.Round(float64(srcH) * sf))

	// Scale to intermediate size.
	interImg := image.NewRGBA(image.Rect(0, 0, interW, interH))
	xdraw.CatmullRom.Scale(interImg, interImg.Bounds(), src, sb, xdraw.Src, nil)

	// Center crop.
	cx := (interW - dstW) / 2
	cy := (interH - dstH) / 2
	cropRect := image.Rect(cx, cy, cx+dstW, cy+dstH)
	cropped := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.Draw(cropped, cropped.Bounds(), interImg, cropRect.Min, draw.Src)

	return cropped
}

// ---------------------------------------------------------------------------
// Shadow rendering
// ---------------------------------------------------------------------------

// renderShadow draws a drop shadow for the artwork onto bg.  The shadow is a
// black rounded rectangle at (artRect.Min+(0,offY), artRect.Size()-(0,0))
// blurred by the given radius and scaled to 20 % opacity.
func renderShadow(bg *image.RGBA, artRect image.Rectangle, cornerRadius, blurRadius, offsetY int) {
	if blurRadius <= 0 {
		return
	}
	pad := blurRadius
	sw := artRect.Dx() + 2*pad
	sh := artRect.Dy() + 2*pad

	shadowImg := image.NewRGBA(image.Rect(0, 0, sw, sh))

	// Draw filled rounded rectangle (black, full opacity) at the offset
	// position inside the padded shadow image.
	rr := image.Rect(pad, pad+offsetY, pad+artRect.Dx(), pad+artRect.Dy()+offsetY)
	drawFilledRoundedRect(shadowImg, rr, cornerRadius, color.RGBA{R: 0, G: 0, B: 0, A: 255})

	// Box blur.
	blurred := boxBlurRGBA(shadowImg, blurRadius)

	// Scale to 20 % opacity.
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			c := blurred.RGBAAt(x, y)
			if c.A > 0 {
				newA := uint8(float64(c.A) * 0.20)
				blurred.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: newA})
			}
		}
	}

	// Composite onto background.
	dstRect := image.Rect(
		artRect.Min.X-pad, artRect.Min.Y-pad,
		artRect.Max.X+pad, artRect.Max.Y+pad,
	)
	draw.Draw(bg, dstRect, blurred, image.Point{}, draw.Over)
}

// ---------------------------------------------------------------------------
// Box blur
// ---------------------------------------------------------------------------

// boxBlurRGBA applies two separable box-blur passes (horizontal then vertical)
// with the given radius.
func boxBlurRGBA(src *image.RGBA, radius int) *image.RGBA {
	b := src.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w == 0 || h == 0 || radius <= 0 {
		return src
	}

	// Horizontal pass.
	hBuf := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sumR, sumG, sumB, sumA int64
			cnt := 0
			for dx := -radius; dx <= radius; dx++ {
				px := x + dx
				if px >= 0 && px < w {
					c := src.RGBAAt(px, y)
					sumR += int64(c.R)
					sumG += int64(c.G)
					sumB += int64(c.B)
					sumA += int64(c.A)
					cnt++
				}
			}
			hBuf.SetRGBA(x, y, color.RGBA{
				R: uint8(sumR / int64(cnt)),
				G: uint8(sumG / int64(cnt)),
				B: uint8(sumB / int64(cnt)),
				A: uint8(sumA / int64(cnt)),
			})
		}
	}

	// Vertical pass.
	dst := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sumR, sumG, sumB, sumA int64
			cnt := 0
			for dy := -radius; dy <= radius; dy++ {
				py := y + dy
				if py >= 0 && py < h {
					c := hBuf.RGBAAt(x, py)
					sumR += int64(c.R)
					sumG += int64(c.G)
					sumB += int64(c.B)
					sumA += int64(c.A)
					cnt++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(sumR / int64(cnt)),
				G: uint8(sumG / int64(cnt)),
				B: uint8(sumB / int64(cnt)),
				A: uint8(sumA / int64(cnt)),
			})
		}
	}

	return dst
}

// ---------------------------------------------------------------------------
// Rounded-corner mask
// ---------------------------------------------------------------------------

// applyRoundedCorners returns a copy of src with the given corner radius
// applied via an alpha mask.  Pixels outside the rounded rect become fully
// transparent.
func applyRoundedCorners(src *image.RGBA, radius int) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)

	if radius <= 0 {
		return dst
	}

	// Zero out alpha for pixels outside the rounded rectangle.
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if !inRoundedRect(x, y, image.Rect(0, 0, b.Dx(), b.Dy()), radius) {
				dst.SetRGBA(x, y, color.RGBA{})
			}
		}
	}
	return dst
}

// inRoundedRect reports whether (x, y) falls inside the rectangle with rounded
// corners of the given radius.
//
//nolint:cyclop
func inRoundedRect(x, y int, rect image.Rectangle, radius int) bool {
	if x < rect.Min.X || x >= rect.Max.X || y < rect.Min.Y || y >= rect.Max.Y {
		return false
	}
	r := radius
	if r <= 0 {
		return true
	}

	// Top-left corner.
	if x < rect.Min.X+r && y < rect.Min.Y+r {
		dx := x - (rect.Min.X + r - 1)
		dy := y - (rect.Min.Y + r - 1)
		return dx*dx+dy*dy <= r*r
	}
	// Top-right corner.
	if x >= rect.Max.X-r && y < rect.Min.Y+r {
		dx := x - (rect.Max.X - r)
		dy := y - (rect.Min.Y + r - 1)
		return dx*dx+dy*dy <= r*r
	}
	// Bottom-left corner.
	if x < rect.Min.X+r && y >= rect.Max.Y-r {
		dx := x - (rect.Min.X + r - 1)
		dy := y - (rect.Max.Y - r)
		return dx*dx+dy*dy <= r*r
	}
	// Bottom-right corner.
	if x >= rect.Max.X-r && y >= rect.Max.Y-r {
		dx := x - (rect.Max.X - r)
		dy := y - (rect.Max.Y - r)
		return dx*dx+dy*dy <= r*r
	}
	return true
}

// drawFilledRoundedRect fills a rectangle with rounded corners using the given
// colour.
func drawFilledRoundedRect(img *image.RGBA, rect image.Rectangle, radius int, c color.RGBA) {
	if radius <= 0 {
		draw.Draw(img, rect, &image.Uniform{c}, image.Point{}, draw.Src)
		return
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if inRoundedRect(x, y, rect, radius) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}
