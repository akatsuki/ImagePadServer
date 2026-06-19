package video

import "math"

// fractionalLogBandEnergies maps FFT coefficients into 24 logarithmic bands
// spanning 20 Hz to 20 kHz using fractional bin overlap.
//
// coeff is the output of an n-point real FFT (n even), so len(coeff) == n/2 + 1.
// coeff[0] is the DC component and is excluded from all bands.
// Partially intersecting FFT bins are weighted by the fraction of their
// bandwidth that falls within each band boundary.
//
// sampleRate must match the sample rate used to produce coeff.
// For best results at 48 kHz, use an 8192-point FFT (coeff length 4097),
// giving ~5.86 Hz bin spacing.
func fractionalLogBandEnergies(coeff []complex128, sampleRate int) [24]float64 {
	// Guard: reject any NaN or Inf coefficient — they would corrupt all bands.
	for _, c := range coeff {
		r, im := real(c), imag(c)
		if math.IsNaN(r) || math.IsNaN(im) || math.IsInf(r, 0) || math.IsInf(im, 0) {
			return [24]float64{}
		}
	}

	var bands [24]float64

	nFFT := (len(coeff) - 1) * 2 // original FFT size
	binWidth := float64(sampleRate) / float64(nFFT)

	for b := 0; b < 24; b++ {
		loFreq := 20.0 * math.Pow(1000.0, float64(b)/24.0)
		hiFreq := 20.0 * math.Pow(1000.0, float64(b+1)/24.0)

		var sum, totalWeight float64

		// Start at bin 1 to exclude DC (bin 0).
		for i := 1; i < len(coeff); i++ {
			binLo := (float64(i) - 0.5) * binWidth
			binHi := (float64(i) + 0.5) * binWidth

			overlapLo := math.Max(binLo, loFreq)
			overlapHi := math.Min(binHi, hiFreq)

			if overlapHi > overlapLo {
				weight := (overlapHi - overlapLo) / binWidth
				mag := cmag(coeff[i])
				sum += mag * weight
				totalWeight += weight
			}
		}

		if totalWeight > 0 {
			bands[b] = sum / totalWeight
		}
	}

	return bands
}
