package video

import (
	"image"
	"math"
)

// ---------------------------------------------------------------------------
// SpectrumFadeHeight
// ---------------------------------------------------------------------------

// SpectrumFadeHeight returns the fixed bottom-fade height in pixels for the
// given canvas width.  At the 1280 px (720p) canonical width the fade is 10
// px; it scales linearly to 15 px at 1920 (1080p) and 5 px at 640 (360p).
//
// Spec: section 5.5 — Fixed fade region.
func SpectrumFadeHeight(width int) int {
	h := int(math.Round(10.0 * float64(width) / 1280.0))
	if h < 1 {
		return 1
	}
	return h
}

// ---------------------------------------------------------------------------
// drawSpectrumFixedFade — fixed-fade variant
// ---------------------------------------------------------------------------

// drawSpectrumFixedFade draws 24 logarithmic frequency bars with a fixed-height
// vertical alpha gradient.
//
// The gradient reaches alpha=0 at the common bottom row (Y = layout bottom)
// and reaches the normal bar opacity (82 %) fadeHeight pixels above.  Pixels
// above the fade region keep the normal bar opacity.
//
// For bars shorter than fadeHeight, the gradient is applied across the
// complete bar without dividing by zero.
//
// Spec: section 10 (layout) and 5.5 (fixed fade).
func drawSpectrumFixedFade(canvas *image.RGBA, spectrum [24]float64, mode ForegroundMode, layout VisualizerLayout) {
	s := float64(canvas.Bounds().Dx()) / 1280.0

	barW := int(math.Round(18 * s))
	barGap := int(math.Round(13 * s))
	firstBarX := layout.Spectrum.X + int(math.Round(11*s))
	barBottom := layout.Spectrum.Y + layout.Spectrum.H
	maxBarH := layout.Spectrum.H - int(math.Round(16*s))
	minBarH := int(math.Round(4 * s))
	if minBarH < 1 {
		minBarH = 1
	}

	maxAlpha := uint8(math.Round(0.82 * 255.0))
	barColor := mode.Color
	barColor.A = maxAlpha

	// Fixed fade height (spec section 5.5).
	fadePx := SpectrumFadeHeight(canvas.Bounds().Dx())
	if fadePx < 1 {
		fadePx = 1
	}

	for b := 0; b < 24 && b < len(spectrum); b++ {
		val := spectrum[b]
		if val < 0 {
			val = 0
		}
		if val > 1 {
			val = 1
		}

		barH := minBarH + int(val*float64(maxBarH-minBarH))
		x := firstBarX + b*(barW+barGap)
		y := barBottom - barH

		// Effective fade: for bars shorter than the canonical fade, use
		// the full bar height so the gradient spans the complete bar.
		effFade := fadePx
		if barH < effFade {
			effFade = barH
		}

		for dx := 0; dx < barW; dx++ {
			for dy := 0; dy < barH; dy++ {
				cx, cy := x+dx, y+dy
				if cx < 0 || cx >= canvas.Bounds().Dx() || cy < 0 || cy >= canvas.Bounds().Dy() {
					continue
				}

				// Vertical alpha gradient: alpha=0 at bottom edge, reaches
				// maxAlpha at effFade pixels above bottom.
				bottomDist := barH - 1 - dy // 0 at bottom, barH-1 at top
				var alpha uint8
				if bottomDist >= effFade {
					alpha = maxAlpha
				} else if effFade == 1 {
					alpha = 0
				} else {
					t := float64(bottomDist) / float64(effFade-1)
					alpha = uint8(math.Round(float64(maxAlpha) * t))
				}

				c := barColor
				c.A = alpha
				blendPixel(canvas, cx, cy, c)
			}
		}
	}
}
