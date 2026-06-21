package video

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// ---------------------------------------------------------------------------
// renderLoudnessLayer
// ---------------------------------------------------------------------------

// renderLoudnessLayer renders the whole-track loudness graph including:
//
//   - Four horizontal guide lines (1 px at 720 p, 22 % foreground opacity)
//   - Detailed loudness curve (2 px at 720 p, 80 % foreground opacity)
//   - Smoothed loudness trend curve (3 px at 720 p, 95 % foreground opacity)
//
// The graph is first rasterised at 4× the target graph resolution, then
// downsampled with a separable Lanczos‑3 filter for crisp anti-aliased
// results.  Only the loudness graph rectangle is painted; the remainder
// of the returned image is fully transparent.
//
// Parameters:
//   - envelope: 1000 normalized relative-loudness samples (values 0…1).
//   - trend:    1000 smoothed trend samples (e.g. from SmoothLoudnessTrend).
//   - mode:     ForegroundMode whose AccentColor is applied to all elements.
//   - layout:   pre-scaled VisualizerLayout containing the Loudness rectangle.
//   - width, height: output frame dimensions in pixels.
func renderLoudnessLayer(
	envelope, trend [1000]float64,
	mode ForegroundMode,
	layout VisualizerLayout,
	width, height int,
) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, width, height))

	lx, ly := layout.Loudness.X, layout.Loudness.Y
	lw, lh := layout.Loudness.W, layout.Loudness.H
	if lw <= 0 || lh <= 0 {
		return out
	}

	// -----------------------------------------------------------------------
	// 4× supersampled buffer (graph area only)
	// -----------------------------------------------------------------------
	const ss = 4 // supersampling factor
	sw, sh := lw*ss, lh*ss
	sup := image.NewRGBA(image.Rect(0, 0, sw, sh))

	// -----------------------------------------------------------------------
	// Element colours
	// -----------------------------------------------------------------------
	guideCol := mode.AccentColor
	guideCol.A = uint8(math.Round(0.22 * 255))
	guideCol = premultiplyRGBA(guideCol)

	detailCol := mode.AccentColor
	detailCol.A = uint8(math.Round(0.80 * 255))
	detailCol = premultiplyRGBA(detailCol)

	trendCol := mode.AccentColor
	trendCol.A = uint8(math.Round(0.95 * 255))
	trendCol = premultiplyRGBA(trendCol)

	// -----------------------------------------------------------------------
	// Line widths at 4× (sub-pixel circle radii)
	//
	// Target line widths are specified at 720p (3 px for trend, 2 px for
	// detail).  We compute the radius directly in supersampled coordinates
	// before any rounding, so that sub-pixel precision is preserved through
	// the 4× Lanczos downsample.
	//
	//   ss       = 4 (supersampling factor)
	//   sf       = height / 720  (scale factor)
	//   radius   = round(tw × sf / 2 × ss)
	//            = round(tw × sf × 2)
	//   tw       = target width at 720p (3 for trend, 2 for detail)
	// -----------------------------------------------------------------------
	sf := float64(height) / 720.0
	detailR := max(1, int(math.Round(2.0*sf/2.0*float64(ss))))
	trendR := max(1, int(math.Round(3.0*sf/2.0*float64(ss))))

	// -----------------------------------------------------------------------
	// 1. Guide lines — four horizontal scale marks
	// -----------------------------------------------------------------------
	guideOffsets := []float64{6.0 / 80.0, 28.0 / 80.0, 50.0 / 80.0, 72.0 / 80.0}
	for _, off := range guideOffsets {
		gy := int(math.Round(off * float64(sh-1)))
		halfH := ss / 2
		for x := 0; x < sw; x++ {
			for dy := -halfH; dy <= halfH; dy++ {
				yy := gy + dy
				if yy >= 0 && yy < sh {
					sup.SetRGBA(x, yy, guideCol)
				}
			}
		}
	}

	// -----------------------------------------------------------------------
	// 2. Detailed loudness curve — one filled circle per envelope sample
	// -----------------------------------------------------------------------
	for i := 0; i < 1000; i++ {
		v := clamp01(envelope[i])
		xc := int(math.Round(float64(i) * float64(sw-1) / 999.0))
		yc := int(math.Round((1.0 - v) * float64(sh-1)))
		fillCircleSet(sup, xc, yc, detailR, detailCol)
	}

	// -----------------------------------------------------------------------
	// 3. Smoothed trend curve — monotone cubic Hermite interpolation
	// -----------------------------------------------------------------------
	interp := monotoneHermite(trend[:], sw)
	if len(interp) == sw {
		for i := 0; i < sw; i++ {
			v := clamp01(interp[i])
			yc := int(math.Round((1.0 - v) * float64(sh-1)))
			fillCircleSet(sup, i, yc, trendR, trendCol)
		}
	}

	// -----------------------------------------------------------------------
	// 4. Lanczos‑3 downsample from 4× to target resolution
	// -----------------------------------------------------------------------
	down := lanczos3Scale(sup, lw, lh)

	// -----------------------------------------------------------------------
	// 5. Composite onto the full-frame output
	// -----------------------------------------------------------------------
	dstRect := image.Rect(lx, ly, lx+lw, ly+lh)
	draw.Draw(out, dstRect, down, image.Point{}, draw.Over)

	return out
}

// premultiplyRGBA converts straight-alpha channel values into the
// alpha-premultiplied representation required by image.RGBA and draw.Over.
func premultiplyRGBA(c color.RGBA) color.RGBA {
	a := uint16(c.A)
	return color.RGBA{
		R: uint8((uint16(c.R)*a + 127) / 255),
		G: uint8((uint16(c.G)*a + 127) / 255),
		B: uint8((uint16(c.B)*a + 127) / 255),
		A: c.A,
	}
}

// ---------------------------------------------------------------------------
// fillCircleSet
// ---------------------------------------------------------------------------

// fillCircleSet draws a filled circle centred at (cx, cy) with the given
// radius, writing pixels directly with SetRGBA (no alpha blending).  The
// circle uses the provided colour as-is.
//
// This function is intended for drawing into a supersampled buffer where
// overlapping coverage should not accumulate alpha.
func fillCircleSet(dst *image.RGBA, cx, cy, r int, c color.RGBA) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				x, y := cx+dx, cy+dy
				if x >= 0 && x < dst.Bounds().Dx() && y >= 0 && y < dst.Bounds().Dy() {
					dst.SetRGBA(x, y, c)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Separable Lanczos‑3 resampler
// ---------------------------------------------------------------------------

// lanczos3Scale downsamples src from its current dimensions to dstW × dstH
// using a separable Lanczos‑3 (a=3) filter.
func lanczos3Scale(src *image.RGBA, dstW, dstH int) *image.RGBA {
	srcB := src.Bounds()
	srcW := srcB.Dx()
	srcH := srcB.Dy()

	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	}

	// ---- Horizontal pass: src → horizontal intermediate ----
	hImg := image.NewRGBA(image.Rect(0, 0, dstW, srcH))
	const a = 3

	for y := 0; y < srcH; y++ {
		for x := 0; x < dstW; x++ {
			// Fractional source position.
			sx := float64(x) * float64(srcW-1) / float64(dstW-1)

			var sumR, sumG, sumB, sumA, sumW float64
			ix := int(math.Floor(sx))
			for k := ix - a + 1; k <= ix+a; k++ {
				if k < 0 || k >= srcW {
					continue
				}
				w := lanczosKernel(sx-float64(k), a)
				if w == 0 {
					continue
				}
				c := src.RGBAAt(k, y)
				sumR += w * float64(c.R)
				sumG += w * float64(c.G)
				sumB += w * float64(c.B)
				sumA += w * float64(c.A)
				sumW += w
			}

			var pix color.RGBA
			if sumW > 0 {
				pix = color.RGBA{
					R: uint8(math.Round(clamp01f64(sumR/sumW, 255) * 255)),
					G: uint8(math.Round(clamp01f64(sumG/sumW, 255) * 255)),
					B: uint8(math.Round(clamp01f64(sumB/sumW, 255) * 255)),
					A: uint8(math.Round(clamp01f64(sumA/sumW, 255) * 255)),
				}
			}
			hImg.SetRGBA(x, y, pix)
		}
	}

	// ---- Vertical pass: horizontal intermediate → final ----
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	for y := 0; y < dstH; y++ {
		sy := float64(y) * float64(srcH-1) / float64(dstH-1)

		for x := 0; x < dstW; x++ {
			var sumR, sumG, sumB, sumA, sumW float64
			iy := int(math.Floor(sy))
			for k := iy - a + 1; k <= iy+a; k++ {
				if k < 0 || k >= srcH {
					continue
				}
				w := lanczosKernel(sy-float64(k), a)
				if w == 0 {
					continue
				}
				c := hImg.RGBAAt(x, k)
				sumR += w * float64(c.R)
				sumG += w * float64(c.G)
				sumB += w * float64(c.B)
				sumA += w * float64(c.A)
				sumW += w
			}

			var pix color.RGBA
			if sumW > 0 {
				pix = color.RGBA{
					R: uint8(math.Round(clamp01f64(sumR/sumW, 255) * 255)),
					G: uint8(math.Round(clamp01f64(sumG/sumW, 255) * 255)),
					B: uint8(math.Round(clamp01f64(sumB/sumW, 255) * 255)),
					A: uint8(math.Round(clamp01f64(sumA/sumW, 255) * 255)),
				}
			}
			dst.SetRGBA(x, y, pix)
		}
	}

	return dst
}

// lanczosKernel returns the Lanczos‑a value at position x.
func lanczosKernel(x float64, a int) float64 {
	ax := math.Abs(x)
	if ax >= float64(a) {
		return 0
	}
	if ax < 1e-10 {
		return 1
	}
	pix := math.Pi * x
	pia := pix / float64(a)
	return (math.Sin(pix) / pix) * (math.Sin(pia) / pia)
}

// clamp01f64 clamps v to [0, limit] and returns the fraction v/limit.
func clamp01f64(v, limit float64) float64 {
	if v < 0 {
		return 0
	}
	if v > limit {
		return 1
	}
	if limit == 0 {
		return 0
	}
	return v / limit
}
