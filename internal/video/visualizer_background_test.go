package video

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// SelectForegroundMode
// ---------------------------------------------------------------------------

func TestSelectForegroundModeDark(t *testing.T) {
	// Solid dark background → light foreground (white text, dark overlay).
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{20, 20, 20, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	mode := SelectForegroundMode(bg, metaRect, graphRect)

	// Light mode → white foreground.
	if mode.Color.R != 255 || mode.Color.G != 255 || mode.Color.B != 255 {
		t.Fatalf("expected white foreground, got %+v", mode.Color)
	}
	// Overlay must be black (light mode).
	if mode.Overlay.R != 0 || mode.Overlay.G != 0 || mode.Overlay.B != 0 {
		t.Fatalf("expected black overlay, got %+v", mode.Overlay)
	}
	// Overlay alpha should be >= 36% (92) and <= 60% (153).
	if mode.Overlay.A < 92 || mode.Overlay.A > 153 {
		t.Fatalf("overlay alpha %d out of range [92,153]", mode.Overlay.A)
	}
}

func TestSelectForegroundModeLight(t *testing.T) {
	// Solid light background → dark foreground (black text, light overlay).
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{235, 235, 235, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	mode := SelectForegroundMode(bg, metaRect, graphRect)

	// Dark mode → black foreground.
	if mode.Color.R != 0 || mode.Color.G != 0 || mode.Color.B != 0 {
		t.Fatalf("expected black foreground, got %+v", mode.Color)
	}
	// Overlay must be white (dark mode).
	if mode.Overlay.R != 255 || mode.Overlay.G != 255 || mode.Overlay.B != 255 {
		t.Fatalf("expected white overlay, got %+v", mode.Overlay)
	}
	// Overlay alpha should be >= 28% (71) and <= 60% (153).
	if mode.Overlay.A < 71 || mode.Overlay.A > 153 {
		t.Fatalf("overlay alpha %d out of range [71,153]", mode.Overlay.A)
	}
}

func TestSelectForegroundModeContrastRatio(t *testing.T) {
	// Create a background with mid-range luminance to test overlay adjustment.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{128, 128, 128, 255}}, image.Point{}, draw.Src)
	// Make metadata region lighter to push toward dark mode.
	draw.Draw(bg, metaRect, &image.Uniform{color.RGBA{200, 200, 200, 255}}, image.Point{}, draw.Src)
	// Make graph region dark to verify it also passes contrast.
	draw.Draw(bg, graphRect, &image.Uniform{color.RGBA{60, 60, 60, 255}}, image.Point{}, draw.Src)

	mode := SelectForegroundMode(bg, metaRect, graphRect)

	// Verify contrast >= 4.5 in the metadata region.
	if ok, ratio := checkRegionContrast(bg, metaRect, mode); !ok {
		t.Fatalf("metadata region contrast ratio %.2f < 4.5 with mode %+v", ratio, mode)
	}

	// Verify contrast >= 4.5 in the graph region.
	if ok, ratio := checkRegionContrast(bg, graphRect, mode); !ok {
		t.Fatalf("graph region contrast ratio %.2f < 4.5 with mode %+v", ratio, mode)
	}
}

// checkRegionContrast computes the worst-case WCAG contrast ratio for the given
// region after applying the overlay and comparing against the foreground color.
func checkRegionContrast(bg image.Image, region image.Rectangle, mode ForegroundMode) (bool, float64) {
	overlayAlpha := float64(mode.Overlay.A) / 255.0
	fgLum := srgbLuminance(mode.Color)

	minRatio := 999.0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			r, g, ba, _ := bg.At(x, y).RGBA()
			bg8 := color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(ba >> 8), 255}

			// Apply overlay.
			eff := color.RGBA{
				R: uint8(float64(mode.Overlay.R)*overlayAlpha + float64(bg8.R)*(1-overlayAlpha)),
				G: uint8(float64(mode.Overlay.G)*overlayAlpha + float64(bg8.G)*(1-overlayAlpha)),
				B: uint8(float64(mode.Overlay.B)*overlayAlpha + float64(bg8.B)*(1-overlayAlpha)),
				A: 255,
			}
			bgLum := srgbLuminance(eff)
			ratio := wcagContrast(fgLum, bgLum)
			if ratio < minRatio {
				minRatio = ratio
			}
		}
	}
	return minRatio >= 4.5, minRatio
}



// ---------------------------------------------------------------------------
// PrepareVisualizerBase
// ---------------------------------------------------------------------------

func TestPrepareVisualizerBaseWithArtwork(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}

	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}

	// Create a test artwork PNG (400x400 red square).
	artDir := t.TempDir()
	artPath := filepath.Join(artDir, "artwork.png")
	artImg := image.NewRGBA(image.Rect(0, 0, 400, 400))
	draw.Draw(artImg, artImg.Bounds(), &image.Uniform{color.RGBA{200, 50, 50, 255}}, image.Point{}, draw.Src)
	{
		f, err := os.Create(artPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, artImg); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
	}

	// Create a fallback image (won't be used since artworkPath is set).
	fallback := image.NewRGBA(image.Rect(0, 0, 288, 288))
	draw.Draw(fallback, fallback.Bounds(), &image.Uniform{color.RGBA{100, 100, 200, 255}}, image.Point{}, draw.Src)

	outPath := filepath.Join(artDir, "output.png")

	mode, err := PrepareVisualizerBase(context.Background(), ffmpeg, artPath, fallback, layout, outPath)
	if err != nil {
		t.Fatalf("PrepareVisualizerBase: %v", err)
	}

	// Verify output file exists and is a valid PNG.
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatalf("output is not a valid PNG: %v", err)
	}

	// ForegroundMode must be valid.
	if mode.Color.A != 255 {
		t.Fatalf("foreground color must be opaque, got alpha %d", mode.Color.A)
	}
	if mode.Overlay.A < 71 || mode.Overlay.A > 153 {
		t.Fatalf("overlay alpha %d out of range [71,153]", mode.Overlay.A)
	}
}

func TestPrepareVisualizerBaseFallback(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}

	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}

	// Create fallback image.
	outDir := t.TempDir()
	fallback := image.NewRGBA(image.Rect(0, 0, 288, 288))
	draw.Draw(fallback, fallback.Bounds(), &image.Uniform{color.RGBA{60, 60, 180, 255}}, image.Point{}, draw.Src)

	outPath := filepath.Join(outDir, "output.png")

	mode, err := PrepareVisualizerBase(context.Background(), ffmpeg, "", fallback, layout, outPath)
	if err != nil {
		t.Fatalf("PrepareVisualizerBase with empty artwork: %v", err)
	}

	// Verify output file exists and is a valid PNG.
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatalf("output is not a valid PNG: %v", err)
	}

	// ForegroundMode must be valid.
	if mode.Color.A != 255 {
		t.Fatalf("foreground color must be opaque, got alpha %d", mode.Color.A)
	}
}
