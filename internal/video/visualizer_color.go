package video

import (
	"image"
	"image/color"
	"math"

	xdraw "golang.org/x/image/draw"
)

const (
	artworkAnalysisMaxSize        = 32
	artworkNeutralChroma          = 0.04
	artworkMinLightness           = 0.08
	artworkMaxLightness           = 0.92
	artworkHueBins                = 24
	artworkWeightChromaCap        = 0.12
	artworkAccentMaxChroma        = 0.12
	artworkAccentMinDisplayChroma = 0.04
	artworkAccentMinLightness     = 0.20
	artworkAccentMaxLightness     = 0.90
	artworkMinimumAlpha           = 128
)

// ---------------------------------------------------------------------------
// OKLCH — cylindrical OKLab colour space (Björn Ottosson)
// ---------------------------------------------------------------------------

// OKLCH represents a colour in the OKLCH (lightness / chroma / hue) space.
//   - L: perceived lightness, 0..1
//   - C: chroma (saturation intensity), ≥ 0
//   - H: hue angle in degrees, 0..360
type OKLCH struct {
	L float64
	C float64
	H float64
}

// prepareArtworkAnalysis downsamples artwork to fit inside a 32 px square and
// applies one 3x3 box-blur pass. The small, lightly blurred image preserves the
// cover's palette while preventing individual noisy pixels from selecting the
// visualizer accent.
func prepareArtworkAnalysis(src image.Image) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}

	scale := math.Min(float64(artworkAnalysisMaxSize)/float64(w), float64(artworkAnalysisMaxSize)/float64(h))
	if scale > 1 {
		scale = 1
	}
	dw := max(1, int(math.Round(float64(w)*scale)))
	dh := max(1, int(math.Round(float64(h)*scale)))
	scaled := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), src, b, xdraw.Src, nil)

	blurred := image.NewRGBA(scaled.Bounds())
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sr, sg, sb, sa, n int
			for oy := -1; oy <= 1; oy++ {
				sy := min(max(y+oy, 0), dh-1)
				for ox := -1; ox <= 1; ox++ {
					sx := min(max(x+ox, 0), dw-1)
					c := scaled.RGBAAt(sx, sy)
					sr += int(c.R)
					sg += int(c.G)
					sb += int(c.B)
					sa += int(c.A)
					n++
				}
			}
			blurred.SetRGBA(x, y, color.RGBA{R: uint8(sr / n), G: uint8(sg / n), B: uint8(sb / n), A: uint8(sa / n)})
		}
	}
	return blurred
}

// artworkAccent returns the restrained OKLCH complement of the artwork's
// dominant chromatic hue. Neutral artwork reports no accent.
func artworkAccent(src image.Image) (OKLCH, bool) {
	analysis := prepareArtworkAnalysis(src)
	colors := make([]color.RGBA, 0, analysis.Bounds().Dx()*analysis.Bounds().Dy())
	for y := analysis.Bounds().Min.Y; y < analysis.Bounds().Max.Y; y++ {
		for x := analysis.Bounds().Min.X; x < analysis.Bounds().Max.X; x++ {
			colors = append(colors, analysis.RGBAAt(x, y))
		}
	}
	return artworkAccentFromColors(colors)
}

func artworkAccentFromColors(colors []color.RGBA) (OKLCH, bool) {
	type hueBin struct {
		weight float64
		sinH   float64
		cosH   float64
		chroma float64
	}
	var bins [artworkHueBins]hueBin

	for _, c := range colors {
		if c.A < artworkMinimumAlpha {
			continue
		}
		v := SRGBToOKLCH(c)
		if v.L < artworkMinLightness || v.L > artworkMaxLightness || v.C < artworkNeutralChroma {
			continue
		}
		weight := 1 + math.Min(v.C, artworkWeightChromaCap)/artworkWeightChromaCap
		idx := int(math.Floor(v.H / (360.0 / artworkHueBins)))
		if idx >= artworkHueBins {
			idx = 0
		}
		radians := v.H * math.Pi / 180
		bins[idx].weight += weight
		bins[idx].sinH += math.Sin(radians) * weight
		bins[idx].cosH += math.Cos(radians) * weight
		bins[idx].chroma += v.C * weight
	}

	best := -1
	for i := range bins {
		if bins[i].weight > 0 && (best < 0 || bins[i].weight > bins[best].weight) {
			best = i
		}
	}
	if best < 0 {
		return OKLCH{}, false
	}

	h := math.Atan2(bins[best].sinH, bins[best].cosH) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	c := math.Min(bins[best].chroma/bins[best].weight, artworkAccentMaxChroma)
	return OKLCH{C: c, H: math.Mod(h+180, 360)}, true
}

// RotateHue returns a copy of o whose hue is rotated by degrees (modulo 360).
func (o OKLCH) RotateHue(degrees float64) OKLCH {
	h := math.Mod(o.H+degrees, 360)
	if h < 0 {
		h += 360
	}
	return OKLCH{L: o.L, C: o.C, H: h}
}

// ClampChroma returns a copy of o with chroma clamped to [min, max].
func (o OKLCH) ClampChroma(min, max float64) OKLCH {
	c := o.C
	if c < min {
		c = min
	}
	if c > max {
		c = max
	}
	return OKLCH{L: o.L, C: c, H: o.H}
}

// ---------------------------------------------------------------------------
// sRGB → OKLCH
// ---------------------------------------------------------------------------

// SRGBToOKLCH converts an 8‑bit sRGB colour (RGBA, alpha ignored) to OKLCH.
func SRGBToOKLCH(c color.RGBA) OKLCH {
	r := srgbLinearize(float64(c.R) / 255.0)
	g := srgbLinearize(float64(c.G) / 255.0)
	b := srgbLinearize(float64(c.B) / 255.0)

	// Linear sRGB → LMS.
	l := 0.4122214708*r + 0.5363325363*g + 0.0514459929*b
	m := 0.2119034982*r + 0.6806995451*g + 0.1073969566*b
	s := 0.0883024619*r + 0.2817188376*g + 0.6299787005*b

	// LMS → OKLab (cube root of LMS).
	l_ := math.Cbrt(l)
	m_ := math.Cbrt(m)
	s_ := math.Cbrt(s)

	L := 0.2104542553*l_ + 0.7936177850*m_ - 0.0040720468*s_
	a := 1.9779984951*l_ - 2.4285922050*m_ + 0.4505937099*s_
	be := 0.0259040371*l_ + 0.7827717662*m_ - 0.8086757660*s_

	// OKLab → OKLCH.
	C := math.Hypot(a, be)
	H := math.Atan2(be, a) * 180.0 / math.Pi
	if H < 0 {
		H += 360
	}
	if C < 0 {
		C = 0
	}

	return OKLCH{L: clamp01(L), C: C, H: H}
}

// ---------------------------------------------------------------------------
// OKLCH → sRGB
// ---------------------------------------------------------------------------

// OKLCHToSRGB converts an OKLCH colour to 8‑bit sRGB.
func OKLCHToSRGB(oklch OKLCH) color.RGBA {
	r, g, be := oklchToLinearSRGB(oklch)

	// Linear sRGB → sRGB (gamma).
	rr := srgbDelinearize(clamp01(r))
	gg := srgbDelinearize(clamp01(g))
	bb := srgbDelinearize(clamp01(be))

	return color.RGBA{
		R: uint8(math.Round(rr * 255.0)),
		G: uint8(math.Round(gg * 255.0)),
		B: uint8(math.Round(bb * 255.0)),
		A: 255,
	}
}

func oklchToLinearSRGB(oklch OKLCH) (float64, float64, float64) {
	// OKLCH → OKLab.
	a := oklch.C * math.Cos(oklch.H*math.Pi/180.0)
	b := oklch.C * math.Sin(oklch.H*math.Pi/180.0)

	// OKLab → LMS (linear).
	l_ := oklch.L + 0.3963377774*a + 0.2158037573*b
	m_ := oklch.L - 0.1055613458*a - 0.0638541728*b
	s_ := oklch.L - 0.0894841775*a - 1.2914855480*b

	// Cube: LMS' → linear LMS (preserve sign).
	l := signCube(l_)
	m := signCube(m_)
	s := signCube(s_)

	// LMS → Linear sRGB.
	r := 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g := -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	be := -0.0041960863*l - 0.7034186147*m + 1.7076147010*s

	return r, g, be
}

func oklchInSRGB(oklch OKLCH) bool {
	r, g, b := oklchToLinearSRGB(oklch)
	return r >= 0 && r <= 1 && g >= 0 && g <= 1 && b >= 0 && b <= 1
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// srgbDelinearize converts a linear sRGB value (0..1) to the gamma‑encoded
// sRGB value (0..1).
func srgbDelinearize(v float64) float64 {
	if v <= 0.0031308 {
		return v * 12.92
	}
	return 1.055*math.Pow(v, 1.0/2.4) - 0.055
}

// signCube computes x³ while preserving the sign of x (handles negative
// inputs correctly where math.Pow would fail).
func signCube(x float64) float64 {
	return x * x * x
}

// clamp01 clamps v to the [0, 1] range.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
