package video

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"testing"
)

func TestArtworkAccentNeutralReturnsNoHue(t *testing.T) {
	img := solidAdaptiveImage(160, 90, color.RGBA{R: 128, G: 128, B: 128, A: 255})
	if accent, ok := artworkAccent(img); ok {
		t.Fatalf("neutral artwork returned accent %+v", accent)
	}
}

func TestArtworkAccentDominantAreaBeatsSaturatedOutlier(t *testing.T) {
	img := solidAdaptiveImage(160, 90, color.RGBA{R: 190, G: 50, B: 45, A: 255})
	img.SetRGBA(0, 0, color.RGBA{R: 0, G: 40, B: 255, A: 255})

	accent, ok := artworkAccent(img)
	if !ok {
		t.Fatal("colorful artwork returned no accent")
	}
	wantHue := SRGBToOKLCH(color.RGBA{R: 190, G: 50, B: 45, A: 255}).RotateHue(180).H
	if d := hueDistance(accent.H, wantHue); d > 20 {
		t.Fatalf("accent hue %.1f differs from dominant complement %.1f by %.1f degrees", accent.H, wantHue, d)
	}
	if accent.C > artworkAccentMaxChroma+1e-9 {
		t.Fatalf("accent chroma %.4f exceeds cap %.4f", accent.C, artworkAccentMaxChroma)
	}
}

func TestArtworkAccentCircularMeanCrossesZero(t *testing.T) {
	left := OKLCHToSRGB(OKLCH{L: 0.65, C: 0.10, H: 355})
	right := OKLCHToSRGB(OKLCH{L: 0.65, C: 0.10, H: 5})
	colors := make([]color.RGBA, 0, 100)
	for i := 0; i < 50; i++ {
		colors = append(colors, left, right)
	}

	accent, ok := artworkAccentFromColors(colors)
	if !ok {
		t.Fatal("color samples returned no accent")
	}
	if d := hueDistance(accent.H, 180); d > 12 {
		t.Fatalf("complement hue %.1f should be near 180 degrees, distance %.1f", accent.H, d)
	}
}

func TestPrepareArtworkAnalysisPreservesAspectWithin32Pixels(t *testing.T) {
	img := solidAdaptiveImage(160, 90, color.RGBA{R: 200, G: 80, B: 40, A: 255})
	got := prepareArtworkAnalysis(img)
	if got.Bounds().Dx() != 32 || got.Bounds().Dy() != 18 {
		t.Fatalf("analysis bounds = %v, want 32x18", got.Bounds())
	}
}

func TestAdaptiveForegroundDarkBackgroundUsesWhitePrimaryAndChromaticAccent(t *testing.T) {
	bg := solidAdaptiveImage(200, 120, color.RGBA{R: 28, G: 30, B: 34, A: 255})
	source := solidAdaptiveImage(200, 120, color.RGBA{R: 190, G: 50, B: 45, A: 255})
	primary := []image.Rectangle{image.Rect(10, 10, 190, 40)}
	accent := []image.Rectangle{image.Rect(10, 60, 190, 110)}

	mode := AdaptiveForeground(bg, source, primary, accent)
	if mode.PrimaryColor != (color.RGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Fatalf("primary = %+v, want white", mode.PrimaryColor)
	}
	if chromaSpread(mode.AccentColor) < 12 {
		t.Fatalf("accent = %+v, want chromatic artwork complement", mode.AccentColor)
	}
	assertAdaptiveContrast(t, bg, primary, mode.Overlay, mode.PrimaryColor)
	assertAdaptiveContrast(t, bg, accent, mode.Overlay, mode.AccentColor)
	if mode.Overlay.A > uint8(math.Round(maxOverlayOpacity*255)) {
		t.Fatalf("overlay alpha %d exceeds cap", mode.Overlay.A)
	}
}

func TestAdaptiveForegroundLightBackgroundUsesBlackPrimary(t *testing.T) {
	bg := solidAdaptiveImage(200, 120, color.RGBA{R: 230, G: 232, B: 235, A: 255})
	source := solidAdaptiveImage(200, 120, color.RGBA{R: 40, G: 80, B: 200, A: 255})
	rects := []image.Rectangle{image.Rect(0, 0, 200, 120)}

	mode := AdaptiveForeground(bg, source, rects, rects)
	if mode.PrimaryColor != (color.RGBA{R: 0, G: 0, B: 0, A: 255}) {
		t.Fatalf("primary = %+v, want black", mode.PrimaryColor)
	}
	assertAdaptiveContrast(t, bg, rects, mode.Overlay, mode.PrimaryColor)
	assertAdaptiveContrast(t, bg, rects, mode.Overlay, mode.AccentColor)
}

func TestAdaptiveForegroundNeutralArtworkUsesPrimaryAsAccent(t *testing.T) {
	bg := solidAdaptiveImage(200, 120, color.RGBA{R: 80, G: 80, B: 80, A: 255})
	source := solidAdaptiveImage(200, 120, color.RGBA{R: 128, G: 128, B: 128, A: 255})
	rects := []image.Rectangle{image.Rect(0, 0, 200, 120)}

	mode := AdaptiveForeground(bg, source, rects, rects)
	if mode.AccentColor != mode.PrimaryColor {
		t.Fatalf("neutral accent %+v differs from primary %+v", mode.AccentColor, mode.PrimaryColor)
	}
}

func TestPrimaryOverlayFallbackHonorsOpacityCap(t *testing.T) {
	bg := image.NewRGBA(image.Rect(0, 0, 200, 100))
	draw.Draw(bg, image.Rect(0, 0, 100, 100), &image.Uniform{C: color.RGBA{A: 255}}, image.Point{}, draw.Src)
	draw.Draw(bg, image.Rect(100, 0, 200, 100), &image.Uniform{C: color.RGBA{R: 255, G: 255, B: 255, A: 255}}, image.Point{}, draw.Src)

	_, overlay := selectPrimaryAndOverlay(bg, []image.Rectangle{bg.Bounds()})
	want := uint8(math.Round(maxOverlayOpacity * 255))
	if overlay.A != want {
		t.Fatalf("fallback overlay alpha = %d, want capped alpha %d", overlay.A, want)
	}
}

func TestVisualizerGraphRenderersUseAccentColor(t *testing.T) {
	layout, _ := LayoutForSize(1280, 720)
	mode := ForegroundMode{
		PrimaryColor: color.RGBA{R: 220, G: 20, B: 20, A: 255},
		AccentColor:  color.RGBA{R: 20, G: 220, B: 20, A: 255},
		Color:        color.RGBA{R: 20, G: 20, B: 220, A: 255},
	}

	canvas := solidAdaptiveImage(1280, 720, color.RGBA{A: 255})
	var spectrum [24]float64
	spectrum[0] = 1
	drawSpectrumFixedFade(canvas, spectrum, mode, layout)
	barPixel := canvas.RGBAAt(layout.Spectrum.X+12, layout.Spectrum.Y+20)
	if barPixel.G <= barPixel.R || barPixel.G <= barPixel.B {
		t.Fatalf("spectrum pixel %+v does not use green accent", barPixel)
	}

	drawProgress(canvas, mode, layout, 0, 1)
	progressPixel := canvas.RGBAAt(layout.Progress.X+layout.Progress.W/2, layout.Progress.Y+layout.Progress.H/2)
	if progressPixel.G <= progressPixel.R || progressPixel.G <= progressPixel.B {
		t.Fatalf("progress pixel %+v does not use green accent", progressPixel)
	}

	args := audioVisualizerFFmpegArgs("audio.wav", "visualizer.ass", ".", "id", QualityPreset{Height: 720}, &mode)
	if !strings.Contains(strings.Join(args, " "), "#14DC14@0.55") {
		t.Fatalf("showwaves args do not use accent color: %v", args)
	}
}

func TestFinalizeFallbackSourceUsesSelectedAccent(t *testing.T) {
	initial := solidAdaptiveImage(8, 8, color.RGBA{R: 30, G: 30, B: 30, A: 255})
	want := color.RGBA{R: 20, G: 210, B: 160, A: 255}
	called := false
	got, err := finalizeFallbackSource(initial, ForegroundMode{AccentColor: want}, func(c color.RGBA) (*image.RGBA, error) {
		called = true
		if c != want {
			t.Fatalf("renderer color = %+v, want %+v", c, want)
		}
		return solidAdaptiveImage(8, 8, c), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("fallback renderer was not called")
	}
	if got.RGBAAt(4, 4) != want {
		t.Fatalf("final fallback pixel = %+v, want %+v", got.RGBAAt(4, 4), want)
	}
}

func assertAdaptiveContrast(t *testing.T, bg image.Image, rects []image.Rectangle, overlay, fg color.RGBA) {
	t.Helper()
	for _, rect := range rects {
		if ok := regionContrastOK(bg, rect, overlay, float64(overlay.A)/255, srgbLuminance(fg), 4.5); !ok {
			t.Fatalf("foreground %+v fails contrast in %v with overlay %+v", fg, rect, overlay)
		}
	}
}

func solidAdaptiveImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return img
}

func hueDistance(a, b float64) float64 {
	d := math.Abs(a - b)
	if d > 180 {
		d = 360 - d
	}
	return d
}
