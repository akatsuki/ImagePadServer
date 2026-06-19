package video

import "math"

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
