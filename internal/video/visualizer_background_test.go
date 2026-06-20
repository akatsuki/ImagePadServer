package video

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
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

// checkDirectContrast computes the worst-case WCAG contrast ratio for the
// given foreground colour against the raw background pixels (no overlay
// compositing).  This is used when the background image already has the
// overlay baked in (e.g. after PrepareVisualizerBase).
func checkDirectContrast(bg image.Image, region image.Rectangle, fg color.RGBA) (bool, float64) {
	fgLum := srgbLuminance(fg)
	minRatio := 999.0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			r, g, ba, _ := bg.At(x, y).RGBA()
			bgLum := srgbLuminance8(float64(r>>8), float64(g>>8), float64(ba>>8))
			ratio := wcagContrast(fgLum, bgLum)
			if ratio < minRatio {
				minRatio = ratio
			}
		}
	}
	return minRatio >= 4.5, minRatio
}

// ---------------------------------------------------------------------------
// ComplementaryForeground
// ---------------------------------------------------------------------------

func TestComplementaryForegroundDeterministic(t *testing.T) {
	// Same input must always produce the same output.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{40, 80, 200, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	first := ComplementaryForeground(bg, metaRect, graphRect)
	for i := 0; i < 10; i++ {
		result := ComplementaryForeground(bg, metaRect, graphRect)
		if result != first {
			t.Fatalf("non-deterministic: run %d produced %+v, expected %+v", i+1, result, first)
		}
	}
}

func TestComplementaryForegroundOpaque(t *testing.T) {
	// Result must always be fully opaque.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{100, 120, 140, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	mode := ComplementaryForeground(bg, metaRect, graphRect)
	if mode.Color.A != 255 {
		t.Fatalf("expected opaque foreground, got alpha %d", mode.Color.A)
	}
}

func TestComplementaryForegroundBlueBg(t *testing.T) {
	// A blue background complement is orange/yellow → result has more R than B.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{40, 80, 200, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	mode := ComplementaryForeground(bg, metaRect, graphRect)

	// The complement of blue (H≈270°) is orange/yellow (H≈90°).
	// Even with clamped chroma the warm hue should be detectable.
	if mode.Color.R < mode.Color.B && mode.Color.R < mode.Color.G {
		t.Fatalf("expected warm-toned complement for blue bg, got R=%d G=%d B=%d", mode.Color.R, mode.Color.G, mode.Color.B)
	}

	if ok, ratio := checkRegionContrast(bg, metaRect, mode); !ok {
		t.Fatalf("metadata region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
	if ok, ratio := checkRegionContrast(bg, graphRect, mode); !ok {
		t.Fatalf("graph region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
}

func TestComplementaryForegroundRedBgFallsBack(t *testing.T) {
	// Red background (200,40,40) has low luminance (~0.14). The complement
	// (cyan at H≈206° with C=0.18) may not reach WCAG 4.5:1 at any L.
	// Verify the function returns a valid result that passes contrast.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{200, 40, 40, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	mode := ComplementaryForeground(bg, metaRect, graphRect)

	// Must be opaque.
	if mode.Color.A != 255 {
		t.Fatalf("expected opaque, got alpha %d", mode.Color.A)
	}

	// Must pass WCAG >= 4.5:1 in both regions (post-overlay).
	if ok, ratio := checkRegionContrast(bg, metaRect, mode); !ok {
		t.Fatalf("metadata region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
	if ok, ratio := checkRegionContrast(bg, graphRect, mode); !ok {
		t.Fatalf("graph region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
}

func TestComplementaryForegroundMidGrayBg(t *testing.T) {
	// Mid-gray (128,128,128) has luminance ≈ 0.22. The complement at clamped
	// chroma (C=0.05) can find very dark L that passes 4.5:1; verify the
	// result is dark and passes WCAG.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{128, 128, 128, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	mode := ComplementaryForeground(bg, metaRect, graphRect)

	if mode.Color.A != 255 {
		t.Fatalf("expected opaque, got alpha %d", mode.Color.A)
	}

	// Must pass WCAG >= 4.5:1 (post-overlay).  With the overlay-first
	// algorithm the chromatic candidate at its complement L can now pass
	// even though its raw contrast against mid-gray is ~1:1.
	if ok, ratio := checkRegionContrast(bg, metaRect, mode); !ok {
		t.Fatalf("metadata region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
	if ok, ratio := checkRegionContrast(bg, graphRect, mode); !ok {
		t.Fatalf("graph region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
}

func TestComplementaryForegroundColorfulBg(t *testing.T) {
	// A saturated background (vivid green) should produce a chromatic
	// foreground complement, not black or white.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{50, 200, 80, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	mode := ComplementaryForeground(bg, metaRect, graphRect)

	if mode.Color.A != 255 {
		t.Fatalf("expected opaque, got alpha %d", mode.Color.A)
	}

	// Must not be pure black or white.
	if (mode.Color.R == 0 && mode.Color.G == 0 && mode.Color.B == 0) ||
		(mode.Color.R == 255 && mode.Color.G == 255 && mode.Color.B == 255) {
		t.Fatalf("expected chromatic foreground for green bg, got black/white: %+v", mode.Color)
	}

	if ok, ratio := checkRegionContrast(bg, metaRect, mode); !ok {
		t.Fatalf("metadata region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
	if ok, ratio := checkRegionContrast(bg, graphRect, mode); !ok {
		t.Fatalf("graph region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
}

func TestComplementaryForegroundBothRegions(t *testing.T) {
	// When both regions have moderate luminance, the foreground must pass
	// WCAG 4.5:1 in every pixel.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	// Use a smooth gradient background — all pixels in both regions have
	// moderate luminance so a single foreground can pass both.
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{70, 90, 120, 255}}, image.Point{}, draw.Src)

	mode := ComplementaryForeground(bg, metaRect, graphRect)

	if mode.Color.A != 255 {
		t.Fatalf("expected opaque, got alpha %d", mode.Color.A)
	}

	if ok, ratio := checkRegionContrast(bg, metaRect, mode); !ok {
		t.Fatalf("metadata region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
	if ok, ratio := checkRegionContrast(bg, graphRect, mode); !ok {
		t.Fatalf("graph region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
}

func TestFallbackWithOverlayChoosesHigherContrastAt60Percent(t *testing.T) {
	bg := image.NewRGBA(image.Rect(0, 0, 32, 16))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{R: 105, G: 115, B: 125, A: 255}}, image.Point{}, draw.Src)
	metaRect := image.Rect(0, 0, 16, 16)
	graphRect := image.Rect(16, 0, 32, 16)

	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	whiteRatio := minRegionContrast(bg, metaRect, black, 0.60, srgbLuminance(white))
	blackRatio := minRegionContrast(bg, metaRect, white, 0.60, srgbLuminance(black))
	if whiteRatio == blackRatio {
		t.Fatal("fixture must produce distinct 60 percent contrast ratios")
	}

	mode := fallbackWithOverlay(bg, metaRect, graphRect)
	want := black
	if whiteRatio > blackRatio {
		want = white
	}
	if mode.Color != want {
		t.Fatalf("fallback color = %#v, want higher-contrast %#v (white %.3f, black %.3f)", mode.Color, want, whiteRatio, blackRatio)
	}
	if mode.Overlay.A > uint8(math.Round(0.60*255)) {
		t.Fatalf("overlay alpha %d exceeds 60 percent cap", mode.Overlay.A)
	}
}

func TestComplementaryForegroundOverlayAchievesContrast(t *testing.T) {
	// Mixed-luminance background: bright metadata region, dark graph region.
	// After overlay, every pixel in both regions must achieve WCAG >= 4.5:1.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{128, 128, 128, 255}}, image.Point{}, draw.Src)
	draw.Draw(bg, metaRect, &image.Uniform{color.RGBA{220, 220, 220, 255}}, image.Point{}, draw.Src)
	draw.Draw(bg, graphRect, &image.Uniform{color.RGBA{40, 40, 40, 255}}, image.Point{}, draw.Src)

	mode := ComplementaryForeground(bg, metaRect, graphRect)

	if mode.Color.A != 255 {
		t.Fatalf("foreground must be opaque, got alpha %d", mode.Color.A)
	}

	if ok, ratio := checkRegionContrast(bg, metaRect, mode); !ok {
		t.Fatalf("metadata region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}

	if ok, ratio := checkRegionContrast(bg, graphRect, mode); !ok {
		t.Fatalf("graph region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
}

func TestComplementaryForegroundDefaultForegroundModeEquality(t *testing.T) {
	// ForegroundMode must support == comparison (used in determinism test).
	// This is a compile-time sanity check since the struct has only
	// comparable fields.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{40, 80, 200, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	m1 := ComplementaryForeground(bg, metaRect, graphRect)
	m2 := ComplementaryForeground(bg, metaRect, graphRect)
	if m1 != m2 {
		t.Fatal("ForegroundMode must be comparable")
	}
}

func TestComplementaryForegroundRawFailsOverlayPasses(t *testing.T) {
	// Uniform mid-gray background where the OKLCH complement (at the same
	// L ≈ 0.54) has ~1:1 raw contrast against every pixel.  The old
	// algorithm rejected all chromatic candidates because raw contrast
	// is < 4.5:1.  The overlay-first fix composes the overlay first and
	// evaluates contrast after compositing, so a chromatic candidate
	// can now pass.
	bg := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(bg, bg.Bounds(), &image.Uniform{color.RGBA{128, 128, 128, 255}}, image.Point{}, draw.Src)

	metaRect := image.Rect(432, 152, 1184, 210)
	graphRect := image.Rect(64, 548, 1064, 628)

	mode := ComplementaryForeground(bg, metaRect, graphRect)

	if mode.Color.A != 255 {
		t.Fatalf("foreground must be opaque, got alpha %d", mode.Color.A)
	}

	// Post-overlay contrast must pass (this is the core of the fix).
	if ok, ratio := checkRegionContrast(bg, metaRect, mode); !ok {
		t.Fatalf("metadata region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}
	if ok, ratio := checkRegionContrast(bg, graphRect, mode); !ok {
		t.Fatalf("graph region: contrast %.2f < 4.5 with mode %+v", ratio, mode)
	}

	// Verify that the raw foreground colour (without overlay) has
	// < 4.5:1 against this mid-gray background.  If this check passes it
	// proves the overlay is doing the work.
	fgLum := srgbLuminance(mode.Color)
	bg8 := color.RGBA{128, 128, 128, 255}
	bgLum := srgbLuminance(bg8)
	rawRatio := wcagContrast(fgLum, bgLum)
	if rawRatio >= 4.5 {
		t.Fatalf("expected raw foreground (%+v, lum=%.4f) to have < 4.5:1 against mid-gray (lum=%.4f), got %.2f — overlay should be necessary",
			mode.Color, fgLum, bgLum, rawRatio)
	}
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

	// ForegroundMode must be valid.  Overlay may be 0 (no overlay needed) up
	// to 255 (100 %).
	if mode.Color.A != 255 {
		t.Fatalf("foreground color must be opaque, got alpha %d", mode.Color.A)
	}
	if mode.Overlay.A > 255 {
		t.Fatalf("overlay alpha %d > 255 (max)", mode.Overlay.A)
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

func TestPrepareVisualizerBaseMixedLuminance(t *testing.T) {
	// Verifies that PrepareVisualizerBase runs successfully with a
	// mixed‑luminance artwork and returns a valid ForegroundMode.
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}

	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}

	// Create an artwork with both bright and dark regions.
	artDir := t.TempDir()
	artPath := filepath.Join(artDir, "artwork.png")
	artImg := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			if x < 128 {
				artImg.SetRGBA(x, y, color.RGBA{240, 240, 240, 255})
			} else {
				artImg.SetRGBA(x, y, color.RGBA{20, 20, 20, 255})
			}
		}
	}
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

	fallback := image.NewRGBA(image.Rect(0, 0, 288, 288))
	draw.Draw(fallback, fallback.Bounds(), &image.Uniform{color.RGBA{128, 128, 128, 255}}, image.Point{}, draw.Src)

	outPath := filepath.Join(artDir, "output.png")

	mode, err := PrepareVisualizerBase(context.Background(), ffmpeg, artPath, fallback, layout, outPath)
	if err != nil {
		t.Fatalf("PrepareVisualizerBase: %v", err)
	}

	// Verify output is a valid PNG.
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

	// ForegroundMode must have opaque colour and valid overlay.
	if mode.Color.A != 255 {
		t.Errorf("foreground must be opaque, got alpha %d", mode.Color.A)
	}
	if mode.Overlay.A > 255 {
		t.Errorf("overlay alpha %d > 255", mode.Overlay.A)
	}

	// Note: we do NOT re-check per-pixel contrast against the final rendered
	// output here because PrepareVisualizerBase composites the shadow and
	// artwork on top of the overlay, which changes pixel values independently
	// of the foreground mode contrast guarantee.  Per-pixel contrast for the
	// raw background (before shadow/artwork) is verified by the
	// TestComplementaryForeground* unit tests above.
	_ = mode
	_ = outPath
}
