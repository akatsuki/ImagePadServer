package video

import (
	"math"
)

// spectrumFMin / spectrumFMax bound the 24 log-spaced visualizer bands.
// 30 Hz–16 kHz keeps every bar inside the range where real music has energy:
// below 30 Hz is sub-bass rumble, above 16 kHz is largely inaudible air.
const (
	spectrumFMin = 30.0
	spectrumFMax = 16000.0
)

// fractionalLogBandEnergies maps FFT coefficients into 24 logarithmic bands
// spanning spectrumFMin..spectrumFMax (30 Hz–16 kHz) using fractional bin
// overlap.
//
// coeff is the output of an n-point real FFT (n even), so len(coeff) == n/2 + 1.
// coeff[0] is the DC component and is excluded from all bands.
// Partially intersecting FFT bins are weighted by the fraction of their
// bandwidth that falls within each band boundary.
//
// sampleRate must match the sample rate used to produce coeff.
// The analyzer uses an 8192-point FFT at 48 kHz (coeff length 4097),
// giving ~5.86 Hz bin spacing so the lowest bands are resolved.
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
		loFreq := spectrumFMin * math.Pow(spectrumFMax/spectrumFMin, float64(b)/24.0)
		hiFreq := spectrumFMin * math.Pow(spectrumFMax/spectrumFMin, float64(b+1)/24.0)

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

// releaseFraction returns the normalized release-curve multiplier at time t
// seconds into the release phase.
//
// The curve follows:
//
//	releaseFraction(t) = (exp(-3.5*t) - exp(-3.5)) / (1 - exp(-3.5))
//
// At t=0 the result is 1.0; at t=1 it is exactly 0. For t>1 it returns 0.
// Expected values: ~0.3987 at 0.25s, ~0.1481 at 0.50s, ~0.0436 at 0.75s.
func releaseFraction(seconds float64) float64 {
	if seconds <= 0 {
		return 1.0
	}
	if seconds >= 1.0 {
		return 0.0
	}
	const k = 3.5
	eNeg35 := math.Exp(-k)
	return (math.Exp(-k*seconds) - eNeg35) / (1.0 - eNeg35)
}

// applySpectrumMotion applies attack and release smoothing to a sequence of
// normalized spectrum frames.
//
// Attack uses a 15ms time constant: display approaches raw value exponentially
// with alpha = 1 - exp(-dt/0.015). At 30fps this reaches ~89% in one frame
// and ~99% in two frames. Release follows the exponential curve defined by
// releaseFraction over exactly 1.0 second.
//
// Each band is smoothed independently. The function never releases below the
// current raw input value. If a new peak arrives, the release is cancelled
// and attack applies immediately.
//
// The output has the same dimensions as the input.
func applySpectrumMotion(raw [][24]float64, fps float64) [][24]float64 {
	if len(raw) == 0 {
		return raw
	}

	frameDuration := 1.0 / fps
	result := make([][24]float64, len(raw))

	var displayed [24]float64
	var peak [24]float64
	var releaseTimer [24]float64

	for fi, frame := range raw {
		for bi, v := range frame {
			if v > displayed[bi] {
				// Attack: exponential approach with 15ms time constant.
				alpha := 1.0 - math.Exp(-frameDuration/0.015)
				newVal := displayed[bi] + (v-displayed[bi])*alpha
				result[fi][bi] = newVal
				displayed[bi] = newVal
				peak[bi] = newVal
				releaseTimer[bi] = 0
			} else {
				// Release: advance timer and apply decay.
				releaseTimer[bi] += frameDuration
				decayed := peak[bi] * releaseFraction(releaseTimer[bi])
				if decayed < v {
					// Never release below the raw value.
					result[fi][bi] = v
					displayed[bi] = v
					peak[bi] = v
					releaseTimer[bi] = 0
				} else {
					result[fi][bi] = decayed
					displayed[bi] = decayed
				}
			}
		}
	}

	return result
}

// fftFullScaleMag is the band-magnitude reference for a full-scale int16
// signal. Dividing band magnitudes by it expresses them in dBFS, keeping the
// absolute reference independent of the FFT size (0.5 is the Hann coherent
// gain).
const fftFullScaleMag = 32768.0 * fftWindowSize * 0.5

// spectrumRefDB / spectrumRangeDB define the fixed absolute window that maps
// band dBFS to bar height. Because music-mode audio is loudness-normalized to a
// known anchor (-14 LUFS), this fixed window replaces the old per-track 95th
// percentile so bar height reflects absolute energy and bars no longer all
// swing to the top. Calibrated against -14 LUFS pink noise (per-band p99
// ~ -33 dBFS, see TestSpectrumCalibrationProbe) with headroom for the higher
// crest factor of real music: a band at spectrumRefDB fills the bar, and
// spectrumRangeDB below it maps to empty.
const (
	spectrumRefDB   = -24.0
	spectrumRangeDB = 54.0
)

// normalizeSpectrumTrack maps raw per-frame band magnitudes to [0,1] using the
// fixed absolute dBFS window (spectrumRefDB / spectrumRangeDB). Non-finite and
// non-positive values map to zero. Unlike the previous per-track percentile
// normalization, this is intentionally NOT gain-invariant: a quieter track
// produces lower bars.
func normalizeSpectrumTrack(raw [][24]float64) [][24]float64 {
	if len(raw) == 0 {
		return raw
	}
	lo := spectrumRefDB - spectrumRangeDB
	result := make([][24]float64, len(raw))
	for fi, frame := range raw {
		for bi, v := range frame {
			if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				result[fi][bi] = 0
				continue
			}
			db := 20.0 * math.Log10(v/fftFullScaleMag)
			norm := (db - lo) / spectrumRangeDB
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
