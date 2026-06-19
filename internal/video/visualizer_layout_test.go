package video

import (
	"context"
	"math"
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// LayoutForSize
// ---------------------------------------------------------------------------

func TestLayoutForSize_720p(t *testing.T) {
	got, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatalf("LayoutForSize(1280,720) = %v", err)
	}
	want := VisualizerLayout{
		Artwork:   Rect{X: 96, Y: 152, W: 288, H: 288},
		Title:     Rect{X: 432, Y: 152, W: 752, H: 58},
		Artist:    Rect{X: 432, Y: 224, W: 752, H: 34},
		Album:     Rect{X: 432, Y: 264, W: 752, H: 30},
		Spectrum:  Rect{X: 432, Y: 320, W: 752, H: 168},
		Loudness:  Rect{X: 64, Y: 548, W: 1000, H: 80},
		Progress:  Rect{X: 64, Y: 650, W: 1000, H: 8},
		Time:      Rect{X: 1088, Y: 632, W: 128, H: 32},
	}
	if got != want {
		t.Fatalf("LayoutForSize(1280,720)\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestLayoutForSize_1080p(t *testing.T) {
	got, err := LayoutForSize(1920, 1080)
	if err != nil {
		t.Fatalf("LayoutForSize(1920,1080) = %v", err)
	}
	// Scale factor = 1920/1280 = 1.5
	want := VisualizerLayout{
		Artwork:   Rect{X: 144, Y: 228, W: 432, H: 432},
		Title:     Rect{X: 648, Y: 228, W: 1128, H: 87},
		Artist:    Rect{X: 648, Y: 336, W: 1128, H: 51},
		Album:     Rect{X: 648, Y: 396, W: 1128, H: 45},
		Spectrum:  Rect{X: 648, Y: 480, W: 1128, H: 252},
		Loudness:  Rect{X: 96, Y: 822, W: 1500, H: 120},
		Progress:  Rect{X: 96, Y: 975, W: 1500, H: 12},
		Time:      Rect{X: 1632, Y: 948, W: 192, H: 48},
	}
	if got != want {
		t.Fatalf("LayoutForSize(1920,1080)\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestLayoutForSize_360p(t *testing.T) {
	got, err := LayoutForSize(640, 360)
	if err != nil {
		t.Fatalf("LayoutForSize(640,360) = %v", err)
	}
	// Scale factor = 640/1280 = 0.5
	want := VisualizerLayout{
		Artwork:   Rect{X: 48, Y: 76, W: 144, H: 144},
		Title:     Rect{X: 216, Y: 76, W: 376, H: 29},
		Artist:    Rect{X: 216, Y: 112, W: 376, H: 17},
		Album:     Rect{X: 216, Y: 132, W: 376, H: 15},
		Spectrum:  Rect{X: 216, Y: 160, W: 376, H: 84},
		Loudness:  Rect{X: 32, Y: 274, W: 500, H: 40},
		Progress:  Rect{X: 32, Y: 325, W: 500, H: 4},
		Time:      Rect{X: 544, Y: 316, W: 64, H: 16},
	}
	if got != want {
		t.Fatalf("LayoutForSize(640,360)\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestLayoutForSize_InvalidWidth(t *testing.T) {
	_, err := LayoutForSize(0, 720)
	if err == nil {
		t.Fatal("expected error for width=0")
	}
}

func TestLayoutForSize_InvalidHeight(t *testing.T) {
	_, err := LayoutForSize(1280, -1)
	if err == nil {
		t.Fatal("expected error for height=-1")
	}
}

// ---------------------------------------------------------------------------
// ScrollOffset
// ---------------------------------------------------------------------------

func TestScrollOffset_FitsViewport(t *testing.T) {
	// Text fits in viewport, should always be 0
	for _, elapsed := range []float64{0, 2.999, 3.0, 10, 100} {
		if got := ScrollOffset(elapsed, 500, 752); got != 0 {
			t.Fatalf("ScrollOffset(%v, 500, 752) = %v, want 0", elapsed, got)
		}
	}
}

func TestScrollOffset(t *testing.T) {
	textWidth := 900.0
	viewportWidth := 752.0
	overflow := textWidth - viewportWidth // 148
	cycle := 3.0 + overflow/40.0          // 3.0 + 3.7 = 6.7

	cases := []struct {
		elapsed float64
		want    float64
		desc    string
	}{
		{0, 0, "start"},
		{2.999, 0, "pause-before-end"},
		{3.0, 0, "pause-end"},
		{4.0, -40, "scroll-1s"},
		{5.0, -80, "scroll-2s"},
		{cycle - 0.001, -147.96, "scroll-end"},
		{cycle, 0, "reset"},
		{cycle + 4.0, -40, "second-cycle-scroll"}, // cycle + 3.0 pause + 1.0 scroll
	}

	for _, c := range cases {
		got := ScrollOffset(c.elapsed, textWidth, viewportWidth)
		if math.Abs(got-c.want) > 0.001 {
			t.Fatalf("ScrollOffset(%v, %v, %v) [%s] = %v, want approx %v",
				c.elapsed, textWidth, viewportWidth, c.desc, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// FormatMediaTime
// ---------------------------------------------------------------------------

func TestFormatMediaTime(t *testing.T) {
	cases := []struct {
		seconds int
		want    string
	}{
		{0, "0:00"},
		{59, "0:59"},
		{60, "1:00"},
		{61, "1:01"},
		{599, "9:59"},
		{600, "10:00"},
		{3599, "59:59"},
		{3600, "1:00:00"},
		{3661, "1:01:01"},
		{86399, "23:59:59"},
	}
	for _, c := range cases {
		if got := FormatMediaTime(c.seconds); got != c.want {
			t.Fatalf("FormatMediaTime(%d) = %q, want %q", c.seconds, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// MeasureTextWithFFmpeg (ffmpeg-dependent)
// ---------------------------------------------------------------------------

func TestMeasureTextWithFFmpeg(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	// Use a font that is known to work with the installed FFmpeg. The Noto Sans
	// CJK variable OTF may not render with all FreeType builds; on Windows we
	// fall back to Arial for the measure test.
	fontPath := findTestFont(t, ffmpeg)

	m, err := MeasureTextWithFFmpeg(context.Background(), ffmpeg, fontPath, "Hello", 48)
	if err != nil {
		t.Fatalf("MeasureTextWithFFmpeg: %v", err)
	}
	if m.Width <= 0 || m.Height <= 0 {
		t.Fatalf("MeasureTextWithFFmpeg returned non-positive dimensions: %+v", m)
	}
	t.Logf("Measured 'Hello' at 48px: %+v", m)

	// Wider text should produce larger width
	narrow, _ := MeasureTextWithFFmpeg(context.Background(), ffmpeg, fontPath, "A", 48)
	wide, _ := MeasureTextWithFFmpeg(context.Background(), ffmpeg, fontPath, "WWWWW", 48)
	if narrow.Width >= wide.Width {
		t.Fatalf("expected 'A' width < 'WWWWW' width: %d vs %d", narrow.Width, wide.Width)
	}
}

// findTestFont returns a font path that works with the current FFmpeg build.
// It tries the bundled Noto fonts first, then falls back to system fonts.
func findTestFont(t *testing.T, ffmpeg string) string {
	t.Helper()
	// Try Noto SemiBold first
	fonts, err := VisualizerFonts()
	if err == nil {
		// Quick smoke test: can FFmpeg drawtext render with this font?
		ctx := context.Background()
		_, err := MeasureTextWithFFmpeg(ctx, ffmpeg, fonts.SemiBold600, "x", 12)
		if err == nil {
			return fonts.SemiBold600
		}
	}
	// Fallback to Windows Arial
	arial := "C:/Windows/Fonts/arial.ttf"
	if _, err := os.Stat(arial); err == nil {
		return arial
	}
	// Last resort: skip the test
	t.Skip("no suitable font found for measure test")
	return ""
}
