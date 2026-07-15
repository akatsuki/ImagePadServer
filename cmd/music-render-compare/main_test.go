package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writeTestPNG(t *testing.T, name string, fill, mark color.RGBA) string {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			im.SetRGBA(x, y, fill)
		}
	}
	im.SetRGBA(1, 1, mark)
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadImageMetricsFindsNonBackgroundBounds(t *testing.T) {
	p := writeTestPNG(t, "a.png", color.RGBA{10, 20, 30, 255}, color.RGBA{240, 220, 200, 255})
	m, err := loadImageMetrics(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 4 || m.Height != 3 || m.NonBackgroundPixels != 1 {
		t.Fatalf("metrics = %+v", m)
	}
	if m.NonBackgroundBounds == nil || m.NonBackgroundBounds.MinX != 1 || m.NonBackgroundBounds.MinY != 1 {
		t.Fatalf("bounds = %+v", m.NonBackgroundBounds)
	}
}

func TestCompareImagesReportsDifference(t *testing.T) {
	a := writeTestPNG(t, "a.png", color.RGBA{0, 0, 0, 255}, color.RGBA{255, 0, 0, 255})
	b := writeTestPNG(t, "b.png", color.RGBA{0, 0, 0, 255}, color.RGBA{0, 255, 0, 255})
	c, err := compareImages(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SizeMatch || c.MismatchedPixels != 1 || c.MismatchRatio <= 0 || c.RMSE <= 0 {
		t.Fatalf("comparison = %+v", c)
	}
}
