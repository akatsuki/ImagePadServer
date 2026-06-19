package video

import (
	"math"
	"sort"
)

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

// normalizeSpectrumTrack converts raw per-frame 24-band magnitudes into
// display-ready normalized values using global track statistics.
//
// Each raw value is a positive magnitude (|FFT coefficient|). The function:
//
//  1. Converts positive magnitudes to dBFS: 20 * log10(v).
//  2. Computes the global 95th percentile (nearest-rank) across all finite
//     dB values as the display reference.
//  3. Maps referenceDB to 1.0 and referenceDB-60 to 0.0, with linear
//     interpolation in dB and clamping to [0, 1].
//  4. Computes a silence floor as the median of the lowest 10% of finite
//     dB values. Values whose dB is at most floor+6 become exact zero.
//  5. Constant-gain invariant: multiplying all raw values by the same
//     positive constant produces identical output within floating-point
//     tolerance.
func normalizeSpectrumTrack(raw [][24]float64) [][24]float64 {
	if len(raw) == 0 {
		return raw
	}

	// Step 1: collect finite dB values for global statistics.
	var allDB []float64
	for _, frame := range raw {
		for _, v := range frame {
			if v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
				allDB = append(allDB, 20.0*math.Log10(v))
			}
		}
	}

	// No finite energy anywhere → all output frames are zero.
	if len(allDB) == 0 {
		result := make([][24]float64, len(raw))
		return result
	}

	sort.Float64s(allDB)
	n := len(allDB)

	// Step 2: global 95th percentile (nearest-rank).
	p95Idx := int(math.Ceil(float64(n)*0.95)) - 1
	if p95Idx < 0 {
		p95Idx = 0
	}
	refDB := allDB[p95Idx]

	// Step 3: silence floor = median of the lowest 10 % of dB values.
	lowCount := int(math.Ceil(float64(n) * 0.1))
	if lowCount < 1 {
		lowCount = 1
	}
	if lowCount > n {
		lowCount = n
	}
	lowest := allDB[:lowCount]
	var floor float64
	if m := len(lowest); m%2 == 0 {
		floor = (lowest[m/2-1] + lowest[m/2]) / 2.0
	} else {
		floor = lowest[m/2]
	}

	silenceThreshold := floor + 6.0
	lo := refDB - 60.0 // point that maps to 0.0
	span := 60.0

	// Step 4: normalize each frame/band.
	result := make([][24]float64, len(raw))
	for fi, frame := range raw {
		for bi, v := range frame {
			if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				result[fi][bi] = 0
				continue
			}
			db := 20.0 * math.Log10(v)

			// Silence floor: values within 6 dB of the floor become zero,
			// but only when the floor is meaningfully below the reference
			// level.  When floor == refDB (uniform nonzero signal) we must
			// not zero everything out.
			if floor+6 < refDB && db <= silenceThreshold {
				result[fi][bi] = 0
				continue
			}

			// Map reference-60..reference to 0..1.
			norm := (db - lo) / span
			if norm < 0 {
				norm = 0
			} else if norm > 1 {
				norm = 1
			}
			result[fi][bi] = norm
		}
	}

	return result
}
