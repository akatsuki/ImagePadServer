package video

import (
	"image"
	"image/color"
	"testing"
)

func TestRenderArtworkCoverGoldenCases(t *testing.T) {
	for _, c := range []struct{ w, h int }{{1, 1}, {2, 1}, {1, 2}, {4, 3}, {16, 9}} {
		s := image.NewRGBA(image.Rect(0, 0, 3, 2))
		s.SetRGBA(0, 0, color.RGBA{255, 0, 0, 255})
		o := RenderArtworkCoverCPU(s, c.w, c.h)
		if o.Bounds().Dx() != c.w || o.Bounds().Dy() != c.h {
			t.Fatal(c)
		}
	}
}
