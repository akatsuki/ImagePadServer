package video

import "math"

// monotoneHermite interpolates the input slice to outputCount samples using
// Fritsch-Carlson monotone cubic Hermite interpolation.
//
// The input points are treated as equally spaced on [0, n-1] where n =
// len(input). The output covers the same interval with outputCount samples.
// Fritsch-Carlson tangents ensure monotonicity — the interpolated curve never
// overshoots the range of adjacent input points within any segment.
//
// An empty input or outputCount <= 0 returns nil. A single-element input
// returns a constant array of the single value.
func monotoneHermite(input []float64, outputCount int) []float64 {
	n := len(input)
	if n == 0 || outputCount <= 0 {
		return nil
	}
	if outputCount == 1 {
		return []float64{input[0]}
	}
	if n == 1 {
		out := make([]float64, outputCount)
		for i := range out {
			out[i] = input[0]
		}
		return out
	}

	// Secant slopes between adjacent input points (implicit h=1).
	secants := make([]float64, n-1)
	for i := 0; i < n-1; i++ {
		secants[i] = input[i+1] - input[i]
	}

	// Fritsch-Carlson tangents.
	tangents := make([]float64, n)

	// Interior points: weighted harmonic mean when secants share a sign.
	for i := 1; i < n-1; i++ {
		if secants[i-1]*secants[i] > 0 {
			// With h=1 for all segments, the weighted harmonic mean
			// simplifies to the ordinary arithmetic mean.
			tangents[i] = (secants[i-1] + secants[i]) / 2
		} else {
			tangents[i] = 0
		}
	}

	// Endpoint tangents.
	tangents[0] = secants[0]
	tangents[n-1] = secants[n-2]

	// Sufficient monotonicity constraint per segment (Fritsch-Carlson).
	for i := 0; i < n-1; i++ {
		d := secants[i]
		if d == 0 {
			tangents[i] = 0
			tangents[i+1] = 0
			continue
		}
		alpha := tangents[i] / d
		beta := tangents[i+1] / d

		// Clamp negative tangents to zero (non-monotone → flat).
		if alpha < 0 {
			alpha = 0
			tangents[i] = 0
		}
		if beta < 0 {
			beta = 0
			tangents[i+1] = 0
		}

		// Scale when alpha² + beta² exceeds 9 (sufficient condition).
		sq := alpha*alpha + beta*beta
		if sq > 9 {
			tau := 3.0 / math.Sqrt(sq)
			tangents[i] = tau * d * alpha
			tangents[i+1] = tau * d * beta
		}
	}

	// Interpolate to outputCount points using cubic Hermite basis.
	out := make([]float64, outputCount)
	maxPos := float64(n - 1)
	maxOut := float64(outputCount - 1)

	for j := 0; j < outputCount; j++ {
		x := float64(j) * maxPos / maxOut

		seg := int(math.Floor(x))
		if seg < 0 {
			seg = 0
		}
		if seg >= n-1 {
			seg = n - 2
		}
		t := x - float64(seg)

		// Hermite basis functions.
		t2 := t * t
		t3 := t2 * t
		h00 := 2*t3 - 3*t2 + 1
		h10 := t3 - 2*t2 + t
		h01 := -2*t3 + 3*t2
		h11 := t3 - t2

		out[j] = h00*input[seg] + h10*tangents[seg] + h01*input[seg+1] + h11*tangents[seg+1]
	}

	return out
}

// trendWindowSize computes the Gaussian kernel size in envelope samples (0..999)
// for a track of the given duration in seconds.
//
// For tracks >= 16 seconds, the effective window is 8 seconds of media time.
// For shorter tracks, the window is reduced to half the track duration so the
// trend does not collapse into one average value.
//
// The result is always odd and at least 1.
func trendWindowSize(duration float64) int {
	effective := 8.0
	if duration < 16 {
		effective = duration / 2
	}
	// Convert seconds to envelope samples (1000 total points).
	n := int(math.Round(effective * 1000 / duration))
	if n < 1 {
		n = 1
	}
	// Ensure odd so the kernel has a well-defined center.
	if n%2 == 0 {
		n++
	}
	return n
}

// SmoothLoudnessTrend applies a normalized Gaussian convolution to the
// 1000-point loudness envelope.  The kernel size is derived from the track
// duration via trendWindowSize.  Boundaries use reflection to avoid false
// downward slopes at the start and end of the track.  Results are clamped
// to [0, 1].
func SmoothLoudnessTrend(envelope [1000]float64, duration float64) [1000]float64 {
	windowSize := trendWindowSize(duration)
	return gaussianSmooth(envelope, windowSize)
}

// gaussianSmooth convolves the 1000-point input with a normalized Gaussian
// kernel of the given odd size.  Boundaries are reflected.
func gaussianSmooth(input [1000]float64, windowSize int) [1000]float64 {
	if windowSize <= 1 {
		// No smoothing needed.
		var out [1000]float64
		for i := range out {
			v := input[i]
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			if v < 0 {
				v = 0
			}
			if v > 1 {
				v = 1
			}
			out[i] = v
		}
		return out
	}

	// Build the normalized Gaussian kernel.
	// Full effective window maps to ±3σ, covering ~99.7% of the Gaussian mass.
	sigma := float64(windowSize) / 6.0
	kernel := make([]float64, windowSize)
	var kernelSum float64
	center := windowSize / 2
	for i := 0; i < windowSize; i++ {
		x := float64(i - center)
		kernel[i] = math.Exp(-x*x / (2 * sigma * sigma))
		kernelSum += kernel[i]
	}
	for i := range kernel {
		kernel[i] /= kernelSum
	}

	// Convolve with reflected boundaries.
	var output [1000]float64
	for i := 0; i < 1000; i++ {
		var sum float64
		for j := 0; j < windowSize; j++ {
			srcIdx := i + (j - center)
			// Reflect at boundaries.
			if srcIdx < 0 {
				srcIdx = -srcIdx
			}
			if srcIdx >= 1000 {
				srcIdx = 1998 - srcIdx
			}
			v := input[srcIdx]
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			sum += kernel[j] * v
		}
		if sum < 0 {
			sum = 0
		}
		if sum > 1 {
			sum = 1
		}
		output[i] = sum
	}
	return output
}
