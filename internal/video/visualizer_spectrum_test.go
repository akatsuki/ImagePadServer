package video

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// SpectrumFadeHeight
// ---------------------------------------------------------------------------

func TestSpectrumFadePixels(t *testing.T) {
	cases := []struct {
		width int
		want  int
	}{
		{640, 5},   // 360p:  640/1280 * 10 = 5
		{1280, 10}, // 720p: 1280/1280 * 10 = 10
		{1920, 15}, // 1080p: 1920/1280 * 10 = 15
	}
	for _, c := range cases {
		got := SpectrumFadeHeight(c.width)
		if got != c.want {
			t.Errorf("SpectrumFadeHeight(%d) = %d, want %d", c.width, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Fixed bottom fade rendering
// ---------------------------------------------------------------------------

// TestSpectrumFixedBottomFade verifies that:
//  1. The bottom pixel of every bar (alpha=0) shows the background colour
//     unchanged (the fade reaches full transparency at the common bottom row).
//  2. Pixels above the fixed fade region have the normal bar opacity.
//  3. Bars shorter than the fade height use the complete bar for the gradient
//     without dividing by zero.
func TestSpectrumFixedBottomFade(t *testing.T) {
	width, height := 1280, 720

	// Mid-grey background makes fade detection easy.
	bgColor := color.RGBA{100, 100, 100, 255}
	base := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(base, base.Bounds(), &image.Uniform{bgColor}, image.Point{}, draw.Src)

	mode := ForegroundMode{
		Color:   color.RGBA{255, 255, 255, 255},
		Overlay: color.RGBA{0, 0, 0, 0}, // transparent overlay
	}
	layout, _ := LayoutForSize(width, height)

	// Tall bars (value = 1.0) test the normal case; the first bar uses a
	// very low value to create a short bar (shorter than fade height).
	frame := AudioFrame{}
	for b := 0; b < 24; b++ {
		frame.Spectrum24[b] = 1.0
	}
	frame.Spectrum24[0] = 0.01 // very short bar (shorter than 10px fade)

	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:     30,
			Duration: 1.0 / 30,
			Frames:   []AudioFrame{frame},
		},
	}

	var buf bytes.Buffer
	if err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, width, height); err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}

	data := buf.Bytes()

	barBottom := layout.Spectrum.Y + layout.Spectrum.H // 488
	firstBarX := layout.Spectrum.X + 11                // 443
	barW := 18
	barGap := 13
	fadePx := 10 // at 720p

	// 1. Bottom pixel of every tall bar must show the background colour
	//    (alpha ≈ 0 at the bottom edge).
	for b := 1; b < 24; b++ {
		x := firstBarX + b*(barW+barGap)
		for dx := 0; dx < barW; dx++ {
			cx := x + dx
			if cx < 0 || cx >= width {
				continue
			}
			idx := ((barBottom-1)*width + cx) * 4
			r, g, bl := data[idx], data[idx+1], data[idx+2]
			dr := absDiff(int(r), 100)
			dg := absDiff(int(g), 100)
			db := absDiff(int(bl), 100)
			if dr > 5 || dg > 5 || db > 5 {
				t.Errorf("bar %d bottom pixel (%d,%d): RGB(%d,%d,%d) far from bg (100,100,100)",
					b, cx, barBottom-1, r, g, bl)
			}
		}
	}

	// 2. Pixels well above the fade region (e.g. 5+ pixels above fade top)
	//    should be visually distinct from the background (= full bar opacity).
	for b := 1; b < 24; b++ {
		x := firstBarX + b*(barW+barGap)
		for dx := 0; dx < barW; dx++ {
			cx := x + dx
			if cx < 0 || cx >= width {
				continue
			}
			// y = barBottom - 1 - fadePx - 5 (15 pixels above bottom)
			checkY := barBottom - 1 - fadePx - 5
			if checkY < 0 {
				continue
			}
			idx := (checkY*width + cx) * 4
			r, g, bl := data[idx], data[idx+1], data[idx+2]
			// Should be far from grey background (white bar at high alpha).
			dr := absDiff(int(r), 100)
			dg := absDiff(int(g), 100)
			db := absDiff(int(bl), 100)
			if dr <= 10 && dg <= 10 && db <= 10 {
				t.Errorf("bar %d above-fade pixel (%d,%d): RGB(%d,%d,%d) too close to bg",
					b, cx, checkY, r, g, bl)
			}
		}
	}

	// 3. The short bar (b=0, value=0.01) must still have a visible bottom
	//    fade. Its bar height is minBarH (4px at 720p). The fade covers the
	//    entire bar without divide-by-zero.
	x0 := firstBarX + 0*(barW+barGap)
	for dx := 0; dx < barW; dx++ {
		cx := x0 + dx
		if cx < 0 || cx >= width {
			continue
		}
		// The very bottom pixel of the short bar.
		idx := ((barBottom-1)*width + cx) * 4
		r, g, bl := data[idx], data[idx+1], data[idx+2]
		dr := absDiff(int(r), 100)
		dg := absDiff(int(g), 100)
		db := absDiff(int(bl), 100)
		if dr > 5 || dg > 5 || db > 5 {
			t.Errorf("short bar bottom pixel (%d,%d): RGB(%d,%d,%d) far from bg, short bar fade broken",
				cx, barBottom-1, r, g, bl)
		}

		// The top pixel of the short bar should be partially visible
		// (some alpha > 0 but not necessarily full).
		topY := barBottom - 4 // 4px bar at 720p (minBarH)
		if topY >= 0 {
			idx2 := (topY*width + cx) * 4
			r, g, bl := data[idx2], data[idx2+1], data[idx2+2]
			dr2 := absDiff(int(r), 100)
			dg2 := absDiff(int(g), 100)
			db2 := absDiff(int(bl), 100)
			if dr2 <= 2 && dg2 <= 2 && db2 <= 2 {
				t.Errorf("short bar top pixel (%d,%d): RGB(%d,%d,%d) invisible, short bar should have gradient",
					cx, topY, r, g, bl)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// drawSpectrumFixedFade direct render (AV-861)
// ---------------------------------------------------------------------------

// pixelRed returns the red channel value at (x, y). With a black background
// and white bar colour the red value equals the source alpha used in the
// blend, which lets us verify the gradient precisely.
func pixelRed(img *image.RGBA, x, y int) uint8 {
	return img.Pix[y*img.Stride+x*4]
}

// TestSpectrumDirectRender exercises drawSpectrumFixedFade directly to verify
// exact alpha values in the fixed fade gradient, short-bar behaviour, and the
// 1-pixel fade edge case (AV-861).
func TestSpectrumDirectRender(t *testing.T) {
	t.Run("720p_alpha_gradient", func(t *testing.T) {
		width, height := 1280, 720
		canvas := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.RGBA{0, 0, 0, 255}}, image.Point{}, draw.Src)

		mode := ForegroundMode{
			Color:   color.RGBA{255, 255, 255, 255},
			Overlay: color.RGBA{0, 0, 0, 0},
		}
		layout, err := LayoutForSize(width, height)
		if err != nil {
			t.Fatal(err)
		}

		var spectrum [24]float64
		for i := range spectrum {
			spectrum[i] = 1.0
		}
		drawSpectrumFixedFade(canvas, spectrum, mode, layout)

		// Derived values at 720p (s = 1.0).
		barBottom := layout.Spectrum.Y + layout.Spectrum.H // 488
		firstBarX := layout.Spectrum.X + 11                // 443
		barW := 18
		barGap := 13
		fadePx := SpectrumFadeHeight(width) // 10
		maxAlpha := uint8(math.Round(0.82 * 255.0)) // 209

		// Use bar index 1 (first tall bar).
		x := firstBarX + 1*(barW+barGap)

		// Bottom pixel (y = barBottom-1 = 487): bottomDist = 0, bar alpha = 0.
		// blendPixel returns early (c.A == 0), pixel stays black → R = 0.
		if r := pixelRed(canvas, x, barBottom-1); r != 0 {
			t.Errorf("bottom pixel R = %d, want 0", r)
		}

		// Top of fade gradient (y = barBottom-fadePx = 478):
		// bottomDist = effFade-1 = 9, bar alpha = round(209*9/9) = 209.
		// Black bg + white bar at alpha 209 → R = 209.
		if r := pixelRed(canvas, x, barBottom-fadePx); r != maxAlpha {
			t.Errorf("top-of-fade pixel R = %d, want %d", r, maxAlpha)
		}

		// Above fade (y = barBottom-fadePx-1 = 477):
		// bottomDist >= effFade, bar alpha = maxAlpha.
		if r := pixelRed(canvas, x, barBottom-fadePx-1); r != maxAlpha {
			t.Errorf("above-fade pixel R = %d, want %d", r, maxAlpha)
		}
	})

	t.Run("short_bar", func(t *testing.T) {
		width, height := 1280, 720
		canvas := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.RGBA{0, 0, 0, 255}}, image.Point{}, draw.Src)

		mode := ForegroundMode{
			Color:   color.RGBA{255, 255, 255, 255},
			Overlay: color.RGBA{0, 0, 0, 0},
		}
		layout, err := LayoutForSize(width, height)
		if err != nil {
			t.Fatal(err)
		}

		var spectrum [24]float64
		for i := range spectrum {
			spectrum[i] = 0
		}
		spectrum[0] = 0.01 // produces a bar shorter than fadePx=10

		drawSpectrumFixedFade(canvas, spectrum, mode, layout)

		barBottom := layout.Spectrum.Y + layout.Spectrum.H // 488
		firstBarX := layout.Spectrum.X + 11                // 443
		barW := 18
		barGap := 13
		maxAlpha := uint8(math.Round(0.82 * 255.0)) // 209

		// barH = minBarH + int(0.01*148) = 4 + 1 = 5 < fadePx (10)
		// so effFade = barH = 5.
		barH := 5
		x := firstBarX + 0*(barW+barGap) // bar index 0

		// Bottom pixel: bottomDist=0, bar alpha=0 → blendPixel no-op → R=0.
		if r := pixelRed(canvas, x, barBottom-1); r != 0 {
			t.Errorf("short bar bottom pixel R = %d, want 0", r)
		}

		// Top of bar (y = barBottom-barH = 483): bottomDist = barH-1 = 4,
		// bar alpha = round(209*4/(effFade-1=4)) = 209.
		if r := pixelRed(canvas, x, barBottom-barH); r != maxAlpha {
			t.Errorf("short bar top pixel R = %d, want %d", r, maxAlpha)
		}
	})

	t.Run("one_px_fade", func(t *testing.T) {
		// Use a narrow canvas so SpectrumFadeHeight returns 1, triggering
		// the effFade==1 special case.
		width := 100
		height := 72
		canvas := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.RGBA{0, 0, 0, 255}}, image.Point{}, draw.Src)

		layout, err := LayoutForSize(width, height)
		if err != nil {
			t.Fatal(err)
		}

		var spectrum [24]float64
		for i := range spectrum {
			spectrum[i] = 1.0
		}

		// Must not panic.
		drawSpectrumFixedFade(canvas, spectrum, ForegroundMode{
			Color:   color.RGBA{255, 255, 255, 255},
			Overlay: color.RGBA{0, 0, 0, 0},
		}, layout)

		// Bottom bar pixel must have R = 0 (bar alpha = 0 → bg unchanged).
		barBottom := layout.Spectrum.Y + layout.Spectrum.H
		s := float64(width) / 1280.0
		firstBarX := layout.Spectrum.X + int(math.Round(11*s))
		barW := int(math.Round(18 * s))
		barGap := int(math.Round(13 * s))
		x := firstBarX + 1*(barW+barGap)

		if x >= 0 && x < width && (barBottom-1) < height {
			if r := pixelRed(canvas, x, barBottom-1); r != 0 {
				t.Errorf("1px fade bottom pixel R = %d, want 0", r)
			}
		}
		// No panic above means the 1-pixel fade path is safe.
	})
}
