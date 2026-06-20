package video

import (
	"image"
	"image/color"
	"testing"
)

// quietPeakedEnvelope is a low-absolute-level RMS envelope with a single louder
// peak. In absolute terms every value is tiny, so an un-normalized render pins
// the curve to the bottom of the panel.
func quietPeakedEnvelope() [1000]float64 {
	var env [1000]float64
	for i := range env {
		env[i] = 0.002
	}
	env[500] = 0.02 // the track's own peak, still quiet in absolute terms
	return env
}

// topmostPaintedY returns the smallest y (highest on screen) that has any
// painted pixel inside the loudness region, or -1 if none.
func topmostPaintedY(layer *image.RGBA, lr Rect) int {
	for y := lr.Y; y < lr.Y+lr.H; y++ {
		for x := lr.X; x < lr.X+lr.W; x++ {
			if _, _, _, a := layer.At(x, y).RGBA(); a > 0 {
				return y
			}
		}
	}
	return -1
}

// TestLoudnessLayerUsesRelativeNormalization locks the fix for quiet tracks
// being pinned to the bottom of the loudness panel. buildLoudnessLayer must
// rescale the absolute envelope against the track peak so the peak reaches the
// upper portion of the graph, unlike a direct absolute render.
func TestLoudnessLayerUsesRelativeNormalization(t *testing.T) {
	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	mode := ForegroundMode{Color: color.RGBA{R: 255, G: 255, B: 255, A: 255}}
	env := quietPeakedEnvelope()
	lr := layout.Loudness

	// Absolute render (what the bug produced): peak ~0.02 sits near the bottom.
	absTrend := SmoothLoudnessTrend(env, 60)
	absLayer := renderLoudnessLayer(env, absTrend, mode, layout, 1280, 720)
	absTop := topmostPaintedY(absLayer, lr)

	// Wired render through buildLoudnessLayer applies relative normalization.
	relLayer := buildLoudnessLayer(AudioFeatures{LoudnessEnvelope: env}, 60, mode, layout, 1280, 720)
	relTop := topmostPaintedY(relLayer, lr)

	if absTop < 0 || relTop < 0 {
		t.Fatalf("expected painted pixels in both layers, got absTop=%d relTop=%d", absTop, relTop)
	}

	// The relative render must reach meaningfully higher (smaller y) than the
	// absolute one, and the peak should land in the top half of the panel.
	if relTop >= absTop {
		t.Errorf("relative render did not rise above absolute render: relTop=%d absTop=%d", relTop, absTop)
	}
	if relTop > lr.Y+lr.H/2 {
		t.Errorf("normalized peak did not reach the top half: relTop=%d region [%d,%d]", relTop, lr.Y, lr.Y+lr.H)
	}
}
