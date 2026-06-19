package video

import "math"

// normalizeRelativeLoudness converts a 1000-point RMS envelope into
// within-track relative loudness values in [0, 1].
//
// The peak RMS point maps to exactly 1.0; points at least 36 dB below the
// peak map to exactly 0.0. An all-silent or non-finite envelope returns
// 1000 zero values. Multiplying all RMS values by a constant that does not
// clip produces identical normalized output within floating-point tolerance.
func normalizeRelativeLoudness(rms [1000]float64) [1000]float64 {
	var result [1000]float64

	hasFinitePeak := false
	peakDB := math.Inf(-1)

	// First pass: find the peak dB value among finite inputs.
	for _, v := range rms {
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		db := 20.0 * math.Log10(v)
		if math.IsInf(db, 0) || math.IsNaN(db) {
			continue
		}
		hasFinitePeak = true
		if db > peakDB {
			peakDB = db
		}
	}

	// If all values are silent or non-finite, return zeros.
	if !hasFinitePeak {
		return result
	}

	threshold := peakDB - 36.0

	// Second pass: normalize each point.
	for i, v := range rms {
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			result[i] = 0
			continue
		}
		db := 20.0 * math.Log10(v)
		if math.IsInf(db, 0) || math.IsNaN(db) {
			result[i] = 0
			continue
		}
		normalized := (db - threshold) / 36.0
		if normalized < 0 {
			normalized = 0
		} else if normalized > 1 {
			normalized = 1
		}
		result[i] = normalized
	}

	return result
}
