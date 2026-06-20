package video

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"testing"
)

// barFadeHeight renders one frame through the live render path and measures the
// bottom-fade height (rows from the bottom where alpha climbs from 0 to the
// full bar opacity) for the spectrum bar with the given index and value.
func barFadeHeight(t *testing.T, barIndex int, val float64) int {
	t.Helper()
	const w, h = 1280, 720
	layout, err := LayoutForSize(w, h)
	if err != nil {
		t.Fatal(err)
	}
	mode := ForegroundMode{Color: color.RGBA{255, 255, 255, 255}}

	var frame AudioFrame
	frame.Spectrum24[barIndex] = val
	input := AudioRenderInput{Analysis: AudioAnalysis{FPS: 30, Duration: 1.0 / 30, Frames: []AudioFrame{frame}}}

	base := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, w, h); err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}
	data := buf.Bytes()

	// Bar geometry mirrors the renderer at s=1 (width 1280).
	barW := 18
	barGap := 13
	firstBarX := layout.Spectrum.X + 11
	x := firstBarX + barIndex*(barW+barGap) + barW/2
	barBottom := layout.Spectrum.Y + layout.Spectrum.H

	maxAlpha := byte(209) // round(0.82*255)
	fade := 0
	for dy := 1; dy <= layout.Spectrum.H; dy++ {
		y := barBottom - dy
		if y < 0 {
			break
		}
		a := data[(y*w+x)*4+3]
		if a == 0 {
			continue // below/above the painted bar
		}
		if a >= maxAlpha {
			break // reached full opacity → top of fade region
		}
		fade++
	}
	return fade
}

// TestSpectrumFadeIsFixedHeight locks the wiring of the fixed-height bottom fade
// (spec §5.5). A tall bar and a shorter bar must fade over the same pixel
// height; the superseded proportional fade (20% of bar height, spec §2.6) made
// them differ.
func TestSpectrumFadeIsFixedHeight(t *testing.T) {
	tall := barFadeHeight(t, 10, 1.0)
	short := barFadeHeight(t, 12, 0.45)
	if tall == 0 || short == 0 {
		t.Fatalf("no fade measured: tall=%d short=%d", tall, short)
	}
	if d := tall - short; d < -1 || d > 1 {
		t.Errorf("fade height varies with bar height (tall=%d short=%d); "+
			"live render still uses proportional drawSpectrum instead of fixed drawSpectrumFixedFade", tall, short)
	}
}
