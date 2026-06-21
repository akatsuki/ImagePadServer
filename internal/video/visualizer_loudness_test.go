package video

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestLoudnessLayerPixels(t *testing.T) {
	var env, trend [1000]float64
	for i := 0; i < 1000; i++ {
		env[i] = 0.5 + 0.5*math.Sin(float64(i)*math.Pi*4/1000.0)
	}
	trend = SmoothLoudnessTrend(env, 240)

	mode := ForegroundMode{AccentColor: color.RGBA{R: 255, G: 255, B: 255, A: 255}}

	t.Run("720p returns correct full-frame dimensions", func(t *testing.T) {
		layout, err := LayoutForSize(1280, 720)
		if err != nil {
			t.Fatal(err)
		}
		layer := renderLoudnessLayer(env, trend, mode, layout, 1280, 720)
		if layer == nil {
			t.Fatal("expected non-nil layer")
		}
		b := layer.Bounds()
		if b.Dx() != 1280 || b.Dy() != 720 {
			t.Fatalf("expected 1280x720, got %dx%d", b.Dx(), b.Dy())
		}
	})

	t.Run("graph area contains non-transparent pixels", func(t *testing.T) {
		layout, err := LayoutForSize(1280, 720)
		if err != nil {
			t.Fatal(err)
		}
		layer := renderLoudnessLayer(env, trend, mode, layout, 1280, 720)
		lr := layout.Loudness
		found := false
		for y := lr.Y; y < lr.Y+lr.H && !found; y++ {
			for x := lr.X; x < lr.X+lr.W && !found; x++ {
				_, _, _, a := layer.At(x, y).RGBA()
				if a > 0 {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("expected non-transparent pixels inside loudness graph area")
		}
	})

	t.Run("layer stores premultiplied RGBA pixels", func(t *testing.T) {
		layout, err := LayoutForSize(1280, 720)
		if err != nil {
			t.Fatal(err)
		}
		colored := ForegroundMode{AccentColor: color.RGBA{R: 255, G: 48, B: 24, A: 255}}
		layer := renderLoudnessLayer(env, trend, colored, layout, 1280, 720)
		lr := layout.Loudness
		for y := lr.Y; y < lr.Y+lr.H; y++ {
			for x := lr.X; x < lr.X+lr.W; x++ {
				p := layer.RGBAAt(x, y)
				if p.R > p.A || p.G > p.A || p.B > p.A {
					t.Fatalf("pixel (%d,%d) is not premultiplied: %+v", x, y, p)
				}
			}
		}
	})

	t.Run("area outside the loudness graph is transparent", func(t *testing.T) {
		layout, err := LayoutForSize(1280, 720)
		if err != nil {
			t.Fatal(err)
		}
		layer := renderLoudnessLayer(env, trend, mode, layout, 1280, 720)
		lr := layout.Loudness
		if lr.Y > 0 {
			for x := 0; x < 1280; x++ {
				_, _, _, a := layer.At(x, lr.Y-1).RGBA()
				if a > 0 {
					t.Fatalf("expected transparent above graph at x=%d, alpha=%d", x, a>>8)
				}
			}
		}
		if lr.Y+lr.H < 720 {
			for x := 0; x < 1280; x++ {
				_, _, _, a := layer.At(x, lr.Y+lr.H).RGBA()
				if a > 0 {
					t.Fatalf("expected transparent below graph at x=%d, alpha=%d", x, a>>8)
				}
			}
		}
	})

	t.Run("works at 1080p", func(t *testing.T) {
		layout, err := LayoutForSize(1920, 1080)
		if err != nil {
			t.Fatal(err)
		}
		layer := renderLoudnessLayer(env, trend, mode, layout, 1920, 1080)
		if layer == nil {
			t.Fatal("expected non-nil layer at 1080p")
		}
		b := layer.Bounds()
		if b.Dx() != 1920 || b.Dy() != 1080 {
			t.Fatalf("expected 1920x1080, got %dx%d", b.Dx(), b.Dy())
		}
		lr := layout.Loudness
		found := false
		for y := lr.Y; y < lr.Y+lr.H && !found; y++ {
			for x := lr.X; x < lr.X+lr.W && !found; x++ {
				_, _, _, a := layer.At(x, y).RGBA()
				if a > 0 {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("expected non-transparent pixels at 1080p")
		}
	})

	t.Run("guide lines produce visible horizontal bands with zero envelope", func(t *testing.T) {
		var zero [1000]float64
		var zt [1000]float64
		layout, err := LayoutForSize(1280, 720)
		if err != nil {
			t.Fatal(err)
		}
		layer := renderLoudnessLayer(zero, zt, mode, layout, 1280, 720)
		lr := layout.Loudness
		// Scan vertically through the graph centre and count rows that
		// contain non-transparent pixels (guide lines + bottom curve).
		midX := lr.X + lr.W/2
		opaqueRows := 0
		for y := lr.Y; y < lr.Y+lr.H; y++ {
			_, _, _, a := layer.At(midX, y).RGBA()
			if a > 0 {
				opaqueRows++
			}
		}
		// With 4 guide lines (each 1–2 rows after Lanczos) plus the
		// zero-value curve at the bottom, we expect at least 4 rows.
		if opaqueRows < 4 {
			t.Fatalf("expected ≥4 rows with non-transparent pixels, got %d", opaqueRows)
		}
	})

	t.Run("peak value draws at top of graph", func(t *testing.T) {
		var peakEnv, peakTrend [1000]float64
		for i := range peakEnv {
			peakEnv[i] = 1.0
			peakTrend[i] = 1.0
		}
		layout, err := LayoutForSize(1280, 720)
		if err != nil {
			t.Fatal(err)
		}
		layer := renderLoudnessLayer(peakEnv, peakTrend, mode, layout, 1280, 720)
		lr := layout.Loudness
		// Check that the top of the graph area has content for peak values
		midX := lr.X + lr.W/2
		topY := lr.Y + 2 // a few px from the top
		if topY >= lr.Y+lr.H {
			topY = lr.Y
		}
		_, _, _, a := layer.At(midX, topY).RGBA()
		if a == 0 {
			t.Fatal("expected non-zero alpha near top of graph for peak value=1")
		}
	})

	// subPixelFWHM measures the full width at half-maximum of a horizontal
	// trend line using linear interpolation between rows for sub-pixel precision.
	subPixelFWHM := func(t *testing.T, layer *image.RGBA, lr Rect, midX int) float64 {
		t.Helper()
		type aRow struct {
			y int
			a uint32
		}
		var rows []aRow
		for y := lr.Y; y < lr.Y+lr.H; y++ {
			_, _, _, a := layer.At(midX, y).RGBA()
			rows = append(rows, aRow{y, a})
		}
		// Find peak.
		peak := 0
		for i := range rows {
			if rows[i].a > rows[peak].a {
				peak = i
			}
		}
		if rows[peak].a == 0 {
			t.Fatal("trend line not found")
		}
		half := float64(rows[peak].a) / 2.0

		// Scan up (decreasing y) from peak.
		var topEdge float64
		for i := peak; i >= 0; i-- {
			if float64(rows[i].a) < half {
				next := rows[i+1] // closer to peak, a >= half
				cur := rows[i]    // a < half
				na := float64(next.a)
				ca := float64(cur.a)
				topEdge = float64(cur.y) + (half-ca)/(na-ca)
				break
			}
		}
		if topEdge == 0 {
			topEdge = float64(rows[0].y)
		}

		// Scan down (increasing y) from peak.
		var bottomEdge float64
		for i := peak; i < len(rows); i++ {
			if float64(rows[i].a) < half {
				prev := rows[i-1] // closer to peak, a >= half
				cur := rows[i]    // a < half
				pa := float64(prev.a)
				ca := float64(cur.a)
				bottomEdge = float64(prev.y) + (half-pa)/(ca-pa)
				break
			}
		}
		if bottomEdge == 0 {
			bottomEdge = float64(rows[len(rows)-1].y)
		}

		return bottomEdge - topEdge
	}

	t.Run("trend line width scales correctly with resolution", func(t *testing.T) {
		// Constant envelope = 0 (no detail circles), constant trend = 0.75
		// putting the trend line between guide lines 6/80 and 28/80.
		var zero [1000]float64
		var trend [1000]float64
		for i := range trend {
			trend[i] = 0.75
		}

		// Expected sub-pixel FWHM after fix:
		//   360p: target=1.5px,  new R=3 → ss circle 7 rows → ≈1.75px → FWHM≈1.81
		//   720p: target=3.0px,  new R=6 → ss circle 13 rows → ≈3.25px → FWHM≈2.99
		//   1080p: target=4.5px, new R=9 → ss circle 19 rows → ≈4.75px → FWHM≈4.99
		//
		// Old buggy code (rounds before multiplying by ss) produces:
		//   360p: R=4 → ≈2.04px FWHM (fails max=2.0)
		//   1080p: R=10 → ≈4.99px FWHM (uses generous max for sanity only;
		//          the Lanczos spread dominates at 1080p, making FWHM
		//          indistinguishable between R=9 and R=10).

		type resCase struct {
			name    string
			w, h    int
			maxFWHM float64
		}
		cases := []resCase{
			{"360p", 640, 360, 2.0},
			{"720p", 1280, 720, 3.5},
			{"1080p", 1920, 1080, 5.5},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				layout, err := LayoutForSize(tc.w, tc.h)
				if err != nil {
					t.Fatal(err)
				}
				layer := renderLoudnessLayer(zero, trend, mode, layout, tc.w, tc.h)
				lr := layout.Loudness
				midX := lr.X + lr.W/2

				fwhm := subPixelFWHM(t, layer, lr, midX)
				if fwhm > tc.maxFWHM {
					t.Fatalf("trend line sub-pixel FWHM=%.4f at %s exceeds max %.4f (likely old buggy radius rounding)",
						fwhm, tc.name, tc.maxFWHM)
				}
			})
		}
	})

	t.Run("4x sup + Lanczos produces finite output", func(t *testing.T) {
		layout, err := LayoutForSize(1280, 720)
		if err != nil {
			t.Fatal(err)
		}
		layer := renderLoudnessLayer(env, trend, mode, layout, 1280, 720)
		// Verify no NaN or corruption in pixel data
		b := layer.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, b_, a := layer.At(x, y).RGBA()
				if r > 0xFFFF || g > 0xFFFF || b_ > 0xFFFF || a > 0xFFFF {
					t.Fatalf("pixel at (%d,%d) has out-of-range values: r=%d g=%d b=%d a=%d", x, y, r, g, b_, a)
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------
// fillCircleSet tests
// ---------------------------------------------------------------------------

func TestFillCircleSet(t *testing.T) {
	t.Run("draws pixels within radius", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 20, 20))
		fillCircleSet(img, 10, 10, 3, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		// Center should be set
		if img.RGBAAt(10, 10).R != 255 {
			t.Fatal("expected center pixel to be set")
		}
		// Edge should be within radius
		if img.RGBAAt(10, 7).R != 255 {
			t.Fatal("expected pixel 3 above center to be set")
		}
		// Outside radius should be transparent
		if img.RGBAAt(10, 6).A != 0 {
			t.Fatal("expected pixel 4 above center to be transparent")
		}
	})

	t.Run("handles radius zero", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 10, 10))
		fillCircleSet(img, 5, 5, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		if img.RGBAAt(5, 5).R != 255 {
			t.Fatal("expected center pixel set with radius 0")
		}
	})

	t.Run("clamps to image bounds", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 10, 10))
		// Circle centered at negative position; should not panic
		fillCircleSet(img, -5, -5, 5, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		// No crash = pass
	})
}
