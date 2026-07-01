package video

import (
	"image/color"
	"math"
	"testing"
)

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---------------------------------------------------------------------------
// Round-trip: sRGB → OKLCH → sRGB
// ---------------------------------------------------------------------------

func TestOKLCHRoundTrip(t *testing.T) {
	tests := []color.RGBA{
		{R: 0, G: 0, B: 0, A: 255},
		{R: 255, G: 255, B: 255, A: 255},
		{R: 255, G: 0, B: 0, A: 255},
		{R: 0, G: 255, B: 0, A: 255},
		{R: 0, G: 0, B: 255, A: 255},
		{R: 128, G: 128, B: 128, A: 255},
		{R: 255, G: 128, B: 0, A: 255},
		{R: 128, G: 0, B: 255, A: 255},
		{R: 0, G: 255, B: 128, A: 255},
		{R: 200, G: 50, B: 50, A: 255},
		{R: 50, G: 200, B: 50, A: 255},
		{R: 50, G: 50, B: 200, A: 255},
		{R: 255, G: 255, B: 0, A: 255},
		{R: 0, G: 255, B: 255, A: 255},
		{R: 255, G: 0, B: 255, A: 255},
		{R: 100, G: 100, B: 100, A: 255},
		{R: 200, G: 200, B: 200, A: 255},
	}
	for _, c := range tests {
		oklch := SRGBToOKLCH(c)
		got := OKLCHToSRGB(oklch)
		if d := absInt(int(got.R) - int(c.R)); d > 1 {
			t.Errorf("R channel error %d for %+v, got %+v", d, c, got)
		}
		if d := absInt(int(got.G) - int(c.G)); d > 1 {
			t.Errorf("G channel error %d for %+v, got %+v", d, c, got)
		}
		if d := absInt(int(got.B) - int(c.B)); d > 1 {
			t.Errorf("B channel error %d for %+v, got %+v", d, c, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Hue rotation: exactly 180 degrees modulo 360
// ---------------------------------------------------------------------------

func TestOKLCHHueRotation(t *testing.T) {
	hues := []float64{0, 30, 90, 180, 270, 45, 135, 225, 315, 360}
	for _, h := range hues {
		oklch := OKLCH{L: 0.5, C: 0.1, H: h}
		rotated := oklch.RotateHue(180)
		got := rotated.H
		want := math.Mod(h+180, 360)
		if got < 0 {
			got += 360
		}
		if want < 0 {
			want += 360
		}
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("RotateHue(180) on %.1f: got %.10f, want %.10f", h, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Chroma clamp: 0.05 .. 0.18
// ---------------------------------------------------------------------------

func TestOKLCHChromaClamp(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{input: 0.01, want: 0.05}, // below min
		{input: 0.30, want: 0.18}, // above max
		{input: 0.10, want: 0.10}, // within range
		{input: 0.05, want: 0.05}, // at min edge
		{input: 0.18, want: 0.18}, // at max edge
		{input: 0.00, want: 0.05}, // zero
		{input: -0.1, want: 0.05}, // negative
		{input: 1.00, want: 0.18}, // extreme high
	}
	for _, tt := range tests {
		oklch := OKLCH{L: 0.5, C: tt.input, H: 180}
		clamped := oklch.ClampChroma(0.05, 0.18)
		if math.Abs(clamped.C-tt.want) > 1e-9 {
			t.Errorf("ClampChroma(%.4f): got C=%.10f, want %.4f", tt.input, clamped.C, tt.want)
		}
	}
}
