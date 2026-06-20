package video

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"
)

// TestOverlayOpacityCappedToPreserveBlur guards the readability-overlay cap.
// A high white overlay washes the strongly blurred background out to a pale
// flat field, hiding the thumbnail. The cap keeps the scrim subtle so the
// blurred artwork stays visible (complementary foreground is preserved).
func TestOverlayOpacityCappedToPreserveBlur(t *testing.T) {
	maxA := uint8(math.Round(maxOverlayOpacity * 255))

	solid := func(c color.RGBA) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 200, 200))
		draw.Draw(img, img.Bounds(), &image.Uniform{c}, image.Point{}, draw.Src)
		return img
	}
	meta := image.Rect(0, 0, 120, 40)
	graph := image.Rect(0, 120, 200, 160)

	for name, c := range map[string]color.RGBA{
		"saturated_blue": {30, 60, 200, 255},
		"mid_red":        {180, 40, 40, 255},
		"dark_gray":      {30, 30, 30, 255},
		"bright_yellow":  {240, 230, 60, 255},
	} {
		mode := ComplementaryForeground(solid(c), meta, graph)
		if mode.Overlay.A > maxA {
			t.Errorf("%s: overlay alpha %d exceeds cap %d; blurred background will wash out", name, mode.Overlay.A, maxA)
		}
	}
}
