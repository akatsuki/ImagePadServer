package video

import (
	"math"
	"testing"
)

func TestMonotoneHermite(t *testing.T) {
	t.Run("returns correct output count", func(t *testing.T) {
		input := []float64{0, 0.5, 1}
		got := monotoneHermite(input, 5)
		if len(got) != 5 {
			t.Fatalf("expected length 5, got %d", len(got))
		}
	})

	t.Run("output count equals input length returns copy", func(t *testing.T) {
		input := []float64{0.1, 0.3, 0.7, 0.9}
		got := monotoneHermite(input, 4)
		if len(got) != 4 {
			t.Fatalf("expected length 4, got %d", len(got))
		}
		for i := range input {
			if got[i] != input[i] {
				t.Fatalf("at index %d: expected %v, got %v", i, input[i], got[i])
			}
		}
	})

	t.Run("monotone ascending preserved", func(t *testing.T) {
		input := []float64{0, 0.25, 0.5, 0.75, 1}
		got := monotoneHermite(input, 17)
		if len(got) != 17 {
			t.Fatalf("expected length 17, got %d", len(got))
		}
		// Output must be non-decreasing
		for i := 1; i < len(got); i++ {
			if got[i] < got[i-1]-1e-10 {
				t.Fatalf("non-monotone at index %d: %v < %v", i, got[i], got[i-1])
			}
		}
		// Output must stay within input range
		if got[0] < 0 || got[len(got)-1] > 1 {
			t.Fatalf("output out of input range: [%v, %v]", got[0], got[len(got)-1])
		}
	})

	t.Run("no segment overshoot for steep transitions", func(t *testing.T) {
		// Abrupt step: values jump from 0.1 to 0.9 in one segment.
		// A plain cubic would overshoot below 0.1 or above 0.9.
		input := []float64{0.1, 0.1, 0.1, 0.9, 0.9, 0.9}
		got := monotoneHermite(input, 25)
		if len(got) != 25 {
			t.Fatalf("expected length 25, got %d", len(got))
		}
		for i, v := range got {
			if v < 0.1-1e-10 || v > 0.9+1e-10 {
				t.Fatalf("overshoot at index %d: value %v outside [0.1, 0.9]", i, v)
			}
		}
	})

	t.Run("constant input produces constant output", func(t *testing.T) {
		input := []float64{0.5, 0.5, 0.5}
		got := monotoneHermite(input, 11)
		if len(got) != 11 {
			t.Fatalf("expected length 11, got %d", len(got))
		}
		for i, v := range got {
			if math.Abs(v-0.5) > 1e-10 {
				t.Fatalf("at index %d: expected 0.5, got %v", i, v)
			}
		}
	})

	t.Run("single element returns single point", func(t *testing.T) {
		input := []float64{0.42}
		got := monotoneHermite(input, 1)
		if len(got) != 1 || got[0] != 0.42 {
			t.Fatalf("expected [0.42], got %v", got)
		}
	})

	t.Run("two elements linear interpolation", func(t *testing.T) {
		input := []float64{0, 1}
		got := monotoneHermite(input, 5)
		expected := []float64{0, 0.25, 0.5, 0.75, 1}
		if len(got) != 5 {
			t.Fatalf("expected length 5, got %d", len(got))
		}
		for i := range expected {
			if math.Abs(got[i]-expected[i]) > 1e-10 {
				t.Fatalf("at index %d: expected %v, got %v", i, expected[i], got[i])
			}
		}
	})

	t.Run("non-monotone input does not exceed bounds", func(t *testing.T) {
		input := []float64{0.2, 0.8, 0.3, 0.9}
		got := monotoneHermite(input, 13)
		if len(got) != 13 {
			t.Fatalf("expected length 13, got %d", len(got))
		}
		for i, v := range got {
			if v < 0.2-1e-10 || v > 0.9+1e-10 {
				t.Fatalf("value at index %d (%v) outside input range [0.2, 0.9]", i, v)
			}
		}
	})

	t.Run("edge flattening preserves shape", func(t *testing.T) {
		// Flat segment followed by rise; Fritsch-Carlson should
		// set tangent to zero at flat-to-rise boundary.
		input := []float64{0.2, 0.2, 0.2, 0.6, 0.8, 1.0}
		got := monotoneHermite(input, 25)
		if len(got) != 25 {
			t.Fatalf("expected length 25, got %d", len(got))
		}
		// Must be non-decreasing
		for i := 1; i < len(got); i++ {
			if got[i] < got[i-1]-1e-10 {
				t.Fatalf("non-monotone at index %d: %v < %v", i, got[i], got[i-1])
			}
		}
		// Must stay within input bounds
		for _, v := range got {
			if v < 0.2-1e-10 || v > 1.0+1e-10 {
				t.Fatalf("value %v outside input range [0.2, 1.0]", v)
			}
		}
	})
}

func TestTrendWindow(t *testing.T) {
	t.Run("longer than 16 seconds uses 8s window", func(t *testing.T) {
		duration := 240.0
		// 8 * 1000 / 240 = 33.333 → 33 (odd)
		got := trendWindowSize(duration)
		if got != 33 {
			t.Fatalf("expected 33 for 240s track, got %d", got)
		}
	})

	t.Run("exactly 16 seconds uses 8s window", func(t *testing.T) {
		duration := 16.0
		// 8 * 1000 / 16 = 500 → 501 (odd)
		got := trendWindowSize(duration)
		if got != 501 {
			t.Fatalf("expected 501 for 16s track, got %d", got)
		}
	})

	t.Run("10 second track uses half duration 5s", func(t *testing.T) {
		duration := 10.0
		// 5 * 1000 / 10 = 500 → 501 (odd)
		got := trendWindowSize(duration)
		if got != 501 {
			t.Fatalf("expected 501 for 10s track, got %d", got)
		}
	})

	t.Run("5 second track uses half duration 2.5s", func(t *testing.T) {
		duration := 5.0
		// 2.5 * 1000 / 5 = 500 → 501 (odd)
		got := trendWindowSize(duration)
		if got != 501 {
			t.Fatalf("expected 501 for 5s track, got %d", got)
		}
	})

	t.Run("very short track returns odd minimum window", func(t *testing.T) {
		duration := 0.5
		// 0.25 * 1000 / 0.5 = 500 → 501 (odd)
		got := trendWindowSize(duration)
		if got < 1 || got%2 == 0 {
			t.Fatalf("expected odd positive window for short track, got %d", got)
		}
	})

	t.Run("window is always odd", func(t *testing.T) {
		durations := []float64{0.5, 1, 3, 7, 10, 15, 16, 20, 30, 60, 120, 240, 600}
		for _, d := range durations {
			got := trendWindowSize(d)
			if got < 1 {
				t.Fatalf("window must be at least 1 for duration %v, got %d", d, got)
			}
			if got%2 == 0 {
				t.Fatalf("window must be odd for duration %v, got %d", d, got)
			}
		}
	})
}

func TestGaussianTrend(t *testing.T) {
	t.Run("constant input produces same constant", func(t *testing.T) {
		var env [1000]float64
		for i := range env {
			env[i] = 0.5
		}
		got := SmoothLoudnessTrend(env, 240)
		for i := range got {
			if math.Abs(got[i]-0.5) > 0.01 {
				t.Fatalf("at index %d: expected ~0.5, got %v", i, got[i])
			}
		}
	})

	t.Run("all zeros stays zeros", func(t *testing.T) {
		var env [1000]float64
		got := SmoothLoudnessTrend(env, 240)
		for i := range got {
			if got[i] != 0 {
				t.Fatalf("at index %d: expected 0, got %v", i, got[i])
			}
		}
	})

	t.Run("all ones stays ones", func(t *testing.T) {
		var env [1000]float64
		for i := range env {
			env[i] = 1
		}
		got := SmoothLoudnessTrend(env, 240)
		for i := range got {
			if math.Abs(got[i]-1) > 0.01 {
				t.Fatalf("at index %d: expected ~1, got %v", i, got[i])
			}
		}
	})

	t.Run("output clamped to 0..1", func(t *testing.T) {
		var env [1000]float64
		env[0] = 2
		env[1] = -1
		got := SmoothLoudnessTrend(env, 240)
		for i := range got {
			if got[i] < 0 || got[i] > 1 {
				t.Fatalf("at index %d: value %v outside [0,1]", i, got[i])
			}
		}
	})

	t.Run("smoothing spreads energy into adjacent bins", func(t *testing.T) {
		var env [1000]float64
		for i := 480; i < 520; i++ {
			env[i] = 1.0
		}
		got := SmoothLoudnessTrend(env, 240)
		// Energy should spread: bins near the impulse rise above zero
		spreadAbove := 0
		for _, v := range got {
			if v > 0.001 {
				spreadAbove++
			}
		}
		if spreadAbove <= 40 {
			t.Fatalf("expected smoothing to spread energy beyond 40 bins, got %d", spreadAbove)
		}
		// Peak should be reduced from the original 1.0
		var peak float64
		for _, v := range got {
			if v > peak {
				peak = v
			}
		}
		if peak >= 1.0 {
			t.Fatalf("expected smoothed peak < 1.0, got %v", peak)
		}
		if peak <= 0 {
			t.Fatalf("expected positive smoothed peak, got %v", peak)
		}
	})

	t.Run("reflected endpoints avoid edge drop", func(t *testing.T) {
		var env [1000]float64
		for i := range env {
			env[i] = 0.5
		}
		got := SmoothLoudnessTrend(env, 240)
		// Both ends should be ~0.5 with reflected boundary
		if math.Abs(got[0]-0.5) > 0.02 {
			t.Fatalf("expected ~0.5 at start with reflection, got %v", got[0])
		}
		if math.Abs(got[999]-0.5) > 0.02 {
			t.Fatalf("expected ~0.5 at end with reflection, got %v", got[999])
		}
	})

	t.Run("kernel is normalized Gaussian", func(t *testing.T) {
		var env [1000]float64
		env[500] = 1.0
		got := SmoothLoudnessTrend(env, 240)
		// With a normalized kernel, energy should be proportional
		sum := 0.0
		for _, v := range got {
			sum += v
		}
		if sum <= 0 || sum > 1.1 {
			t.Fatalf("expected sum ~1 from a single impulse, got %v", sum)
		}
	})

	t.Run("duration-based window narrows for longer tracks", func(t *testing.T) {
		// Shorter tracks get a wider kernel in samples → more smoothing
		shortWin := trendWindowSize(10)   // half-duration = 5s → 501 samples
		longWin := trendWindowSize(240)    // 8s window → 33 samples
		if shortWin <= longWin {
			t.Fatalf("expected shorter track to have larger window, short=%d long=%d",
				shortWin, longWin)
		}
	})

	t.Run("endpoint impulse preserves reflection symmetry", func(t *testing.T) {
		// A non-constant signal proves the reflection formulas work correctly
		// (constant signals hide reflection errors because there is no edge).
		//
		// Left reflection: x[-(i+1)] = x[i+1]
		// Right reflection: x[n+i] = x[n-2-i]  (n = 1000)
		//
		// For an impulse at env[0] = 1, the output near index 0 uses
		// kernel[center-i] * 1.  For the same impulse at env[999] = 1,
		// the output near index 999 uses kernel[center+i] * 1.
		// With a symmetric Gaussian kernel these are equal, so:
		//   leftSmoothed[i] ≈ rightSmoothed[999-i]
		var leftEnv, rightEnv [1000]float64
		leftEnv[0] = 1
		rightEnv[999] = 1

		leftGot := SmoothLoudnessTrend(leftEnv, 240)  // window=33
		rightGot := SmoothLoudnessTrend(rightEnv, 240)

		// Check symmetry for the first 33 indices (one full kernel half-width)
		for i := 0; i < 33; i++ {
			l := leftGot[i]
			r := rightGot[999-i]
			diff := math.Abs(l - r)
			if diff > 1e-12 {
				t.Errorf("left-right asymmetry at offset %d: leftImpulse[%d]=%g, rightImpulse[%d]=%g, diff=%g",
					i, i, l, 999-i, r, diff)
			}
		}
	})

	t.Run("non-finite input produces finite output", func(t *testing.T) {
		var env [1000]float64
		env[100] = math.NaN()
		env[200] = math.Inf(1)
		env[300] = math.Inf(-1)
		got := SmoothLoudnessTrend(env, 240)
		for i := range got {
			if math.IsNaN(got[i]) || math.IsInf(got[i], 0) {
				t.Fatalf("at index %d: non-finite value %v", i, got[i])
			}
		}
	})
}
