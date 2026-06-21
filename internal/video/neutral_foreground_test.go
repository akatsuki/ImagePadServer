package video

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
)

func solidBG(c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(img, img.Bounds(), &image.Uniform{c}, image.Point{}, draw.Src)
	return img
}

func chromaSpread(c color.RGBA) int {
	hi, lo := c.R, c.R
	for _, v := range []uint8{c.G, c.B} {
		if v > hi {
			hi = v
		}
		if v < lo {
			lo = v
		}
	}
	return int(hi) - int(lo)
}

// TestNeutralBackgroundUsesAchromaticForeground locks the fix for "everything
// turns navy". A near-neutral (low-chroma) background has no meaningful hue to
// complement; forcing one produced a navy tint for every muted/blurred
// artwork. Neutral backgrounds must use a white/black foreground, while genuine
// colors still get a chromatic complement.
func TestNeutralBackgroundUsesAchromaticForeground(t *testing.T) {
	meta := image.Rect(0, 0, 120, 40)
	graph := image.Rect(0, 120, 200, 160)

	neutrals := map[string]color.RGBA{
		"mid_gray":   {128, 128, 128, 255},
		"dark_gray":  {30, 30, 30, 255},
		"light_gray": {230, 230, 230, 255},
		"warm_gray":  {134, 130, 126, 255},
	}
	for name, c := range neutrals {
		bg := solidBG(c)
		mode := AdaptiveForeground(bg, bg, []image.Rectangle{meta}, []image.Rectangle{graph})
		if s := chromaSpread(mode.PrimaryColor); s > 8 {
			t.Errorf("%s: primary %v is tinted (chroma spread %d) instead of white/black", name, mode.PrimaryColor, s)
		}
		if mode.AccentColor != mode.PrimaryColor {
			t.Errorf("%s: neutral accent %v differs from primary %v", name, mode.AccentColor, mode.PrimaryColor)
		}
	}

	// Genuinely colorful backgrounds must still get a chromatic complement.
	colorful := map[string]color.RGBA{
		"red":   {200, 40, 40, 255},
		"green": {40, 180, 60, 255},
		"blue":  {40, 70, 200, 255},
	}
	for name, c := range colorful {
		bg := solidBG(c)
		mode := AdaptiveForeground(bg, bg, []image.Rectangle{meta}, []image.Rectangle{graph})
		accent := SRGBToOKLCH(mode.AccentColor)
		if accent.C < 0.035 {
			t.Errorf("%s: accent %v lost its chromatic complement (OKLCH C %.3f)", name, mode.AccentColor, accent.C)
		}
		if accent.L < 0.18 || accent.L > 0.92 {
			t.Errorf("%s: accent %v has imperceptible extreme lightness %.3f", name, mode.AccentColor, accent.L)
		}
	}
}
