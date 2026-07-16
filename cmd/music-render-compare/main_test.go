package main

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSHA256FileMatchesBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "frame.png")
	want := sha256.Sum256([]byte("frame-fixture"))
	if err := os.WriteFile(p, []byte("frame-fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := sha256File(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != fmt.Sprintf("%x", want) {
		t.Fatalf("sha256 = %q, want %x", got, want)
	}
}

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

func TestCPUOnlyMarkdownDoesNotClaimGPUComparison(t *testing.T) {
	dir := t.TempDir()
	writeMarkdown(dir, report{Input: "fixture.wav", CPUOnly: true, CPU: renderResult{Probe: probeResult{Duration: 1.5, Frames: 45}}})
	b, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "# CPU music render fixture") {
		t.Fatalf("markdown title = %q", s)
	}
	if !strings.Contains(s, "| GPU | 0.000 | 0.000 | 0 |  |") {
		t.Fatalf("missing explicit empty GPU row: %q", s)
	}
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
