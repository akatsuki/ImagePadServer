package video

import (
	"math"
	"testing"

	"gonum.org/v1/gonum/dsp/fourier"
)

func TestFractionalLogBands(t *testing.T) {
	fftSize := 8192
	sampleRate := 48000

	for b := 0; b < 24; b++ {
		loFreq := 20.0 * math.Pow(1000.0, float64(b)/24.0)
		hiFreq := 20.0 * math.Pow(1000.0, float64(b+1)/24.0)
		freq := math.Sqrt(loFreq * hiFreq)

		window := make([]float64, fftSize)
		for i := 0; i < fftSize; i++ {
			sine := math.Sin(2 * math.Pi * freq * float64(i) / float64(sampleRate))
			hann := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(fftSize-1)))
			window[i] = sine * hann
		}

		fft := fourier.NewFFT(fftSize)
		coeff := fft.Coefficients(nil, window)

		energies := fractionalLogBandEnergies(coeff, sampleRate)

		if math.IsNaN(energies[b]) || math.IsInf(energies[b], 0) || energies[b] <= 0 {
			t.Errorf("band %d (%.1f-%.1f Hz, test freq %.1f Hz): expected finite positive energy, got %e",
				b, loFreq, hiFreq, freq, energies[b])
		}
	}
}

func TestNormalizeSpectrumTrackPreservesShape(t *testing.T) {
	// Basic normalization: a ramp across bands and frames should produce
	// output with the same shape and values in [0, 1].
	raw := make([][24]float64, 3)
	for i := 0; i < 3; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = 0.1 + 0.9*float64(j)/23
		}
	}

	got := normalizeSpectrumTrack(raw)
	if len(got) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(got))
	}
	for i, frame := range got {
		if len(frame) != 24 {
			t.Fatalf("frame %d: expected 24 bands, got %d", i, len(frame))
		}
		for j, v := range frame {
			if v < 0 || v > 1 {
				t.Errorf("frame[%d][%d]=%v out of [0,1]", i, j, v)
			}
		}
	}
	// Highest band (most energy) must be near 1.0.
	if got[0][23] < 0.9 {
		t.Errorf("expected highest band near 1.0, got %f", got[0][23])
	}
}

func TestNormalizeSpectrumTrackConstantGainInvariant(t *testing.T) {
	// Multiplying all raw values by a constant < 1 that does not clip
	// must produce equivalent bar heights within floating-point tolerance.
	base := make([][24]float64, 5)
	for i := 0; i < 5; i++ {
		for j := 0; j < 24; j++ {
			base[i][j] = 0.05 + 0.95*float64(j)/23
		}
	}

	scaled := make([][24]float64, 5)
	for i, frame := range base {
		for j, v := range frame {
			scaled[i][j] = v * 0.25
		}
	}

	gotBase := normalizeSpectrumTrack(base)
	gotScaled := normalizeSpectrumTrack(scaled)

	for i := 0; i < 5; i++ {
		for j := 0; j < 24; j++ {
			if math.Abs(gotBase[i][j]-gotScaled[i][j]) > 1e-12 {
				t.Fatalf("[%d][%d]: base=%v scaled=%v diff=%v",
					i, j, gotBase[i][j], gotScaled[i][j], math.Abs(gotBase[i][j]-gotScaled[i][j]))
			}
		}
	}
}

func TestNormalizeSpectrumTrackAllZero(t *testing.T) {
	// All-zero input must produce all-zero output.
	raw := make([][24]float64, 3)
	got := normalizeSpectrumTrack(raw)
	for i, frame := range got {
		for j, v := range frame {
			if v != 0 {
				t.Errorf("[%d][%d]: expected 0, got %v", i, j, v)
			}
		}
	}
}

func TestNormalizeSpectrumTrackEmpty(t *testing.T) {
	// Nil input returns nil.
	if got := normalizeSpectrumTrack(nil); got != nil {
		t.Error("expected nil for nil input")
	}
	// Empty input returns empty.
	if got := normalizeSpectrumTrack([][24]float64{}); len(got) != 0 {
		t.Error("expected empty for empty input")
	}
}

func TestNormalizeSpectrumTrackNearSilence(t *testing.T) {
	// A track with deep-silence frames that are far below the active
	// signal.  The silence floor must be zeroed out, while the active
	// frames must remain positive.
	raw := make([][24]float64, 100)
	for i := 0; i < 10; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = 1e-12 // deep silence, ~-240 dB
		}
	}
	for i := 10; i < 100; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = 0.1 // active signal, ~-20 dB
		}
	}

	got := normalizeSpectrumTrack(raw)
	// Deep-silence frames must be exactly zero.
	for i := 0; i < 10; i++ {
		for j := 0; j < 24; j++ {
			if got[i][j] != 0 {
				t.Errorf("silent frame [%d][%d]: expected 0, got %v", i, j, got[i][j])
				break
			}
		}
	}
	// Active frames must be non-zero.
	for i := 10; i < 100; i++ {
		if got[i][0] <= 0 {
			t.Errorf("active frame [%d]: expected >0, got %v", i, got[i][0])
			break
		}
	}
}

func TestNormalizeSpectrumTrackUses95thPercentile(t *testing.T) {
	// Create a track with a clear floor (deep-silence frames), moderate
	// frames, and a few very loud frames. The 95th percentile should
	// exclude the top ~5% (the loudest frames) and reflect the moderate level.
	//
	// Layout: 3 floor + 35 moderate + 2 loud = 40 frames × 24 bands = 960 values.
	// Lowest 10% = 96 values. Floor frames contribute 3×24=72 values,
	// all at ~-240 dB. The median of the lowest 96 is therefore -240.
	// Moderate dB = 20*log10(0.1) ≈ -20. Silence threshold ≈ -240+6 = -234.
	// Therefore moderate values survive the floor check.
	raw := make([][24]float64, 40)
	// 3 floor frames — deep silence, ~7.5% of frames.
	for i := 0; i < 3; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = 1e-12
		}
	}
	// 35 moderate frames (~87.5%).
	for i := 3; i < 38; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = 0.1
		}
	}
	// 2 loud frames (5%) — these should be above the 95th percentile.
	for i := 38; i < 40; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = 100.0
		}
	}

	got := normalizeSpectrumTrack(raw)
	// The moderate frames should map to values near 1.0 since the
	// 95th percentile reference is ~0.1, not 100.0.
	if got[3][0] < 0.5 {
		t.Errorf("moderate frame expected >0.5, got %v (95th percentile may be using max instead)", got[3][0])
	}
	// The very loud frames should also map to 1.0 (clamped).
	if got[38][0] != 1.0 {
		t.Errorf("loud frame expected 1.0, got %v", got[38][0])
	}
}

func TestNormalizeSpectrumTrackNonFinite(t *testing.T) {
	// Non-finite raw values should be treated as zero.
	raw := make([][24]float64, 2)
	raw[0][0] = math.NaN()
	raw[0][1] = math.Inf(1)
	raw[0][2] = math.Inf(-1)
	raw[0][3] = 1.0 // valid peak
	raw[1][0] = 0.1 // valid lower value

	got := normalizeSpectrumTrack(raw)
	if got[0][0] != 0 {
		t.Errorf("NaN position: expected 0, got %v", got[0][0])
	}
	if got[0][1] != 0 {
		t.Errorf("+Inf position: expected 0, got %v", got[0][1])
	}
	if got[0][2] != 0 {
		t.Errorf("-Inf position: expected 0, got %v", got[0][2])
	}
	// Valid peak must be non-zero.
	if got[0][3] <= 0 {
		t.Errorf("valid peak: expected >0, got %v", got[0][3])
	}
}

func TestNormalizeSpectrumTrackFloorZerosNearSilence(t *testing.T) {
	// When a band is consistently near the noise floor (within 6 dB),
	// it should become exactly zero.
	raw := make([][24]float64, 20)
	for i := 0; i < 20; i++ {
		for j := 0; j < 24; j++ {
			if j < 4 {
				// Bands 0-3: just above the floor
				raw[i][j] = 1e-8
			} else {
				// Bands 4-23: strong signal
				raw[i][j] = 0.5 + 0.5*float64(j-4)/19
			}
		}
	}

	got := normalizeSpectrumTrack(raw)
	// Bands 0-3 should be zero (near silence floor).
	for j := 0; j < 4; j++ {
		if got[0][j] != 0 {
			t.Errorf("near-silence band %d: expected 0, got %v", j, got[0][j])
		}
	}
	// Bands 4+ should be non-zero.
	for j := 4; j < 24; j++ {
		if got[0][j] <= 0 {
			t.Errorf("active band %d: expected >0, got %v", j, got[0][j])
			break
		}
	}
}

func TestNormalizeSpectrumTrackAllFinite(t *testing.T) {
	// All values must be finite in the output (no NaN, no Inf).
	raw := make([][24]float64, 4)
	for i := 0; i < 4; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = 0.01 + 0.99*float64(i*24+j)/95
		}
	}
	got := normalizeSpectrumTrack(raw)
	for i, frame := range got {
		for j, v := range frame {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("[%d][%d]: non-finite value %v", i, j, v)
			}
		}
	}
}

func TestNormalizeSpectrumTrackUniform(t *testing.T) {
	// Bands at similar but not identical levels. Include a few deep-silence
	// bands to set the floor below the main signal.
	raw := make([][24]float64, 3)
	for i := 0; i < 3; i++ {
		for j := 0; j < 24; j++ {
			if j < 2 {
				raw[i][j] = 1e-10 // deep silence to set floor
			} else {
				raw[i][j] = 0.5 // uniform signal
			}
		}
	}
	got := normalizeSpectrumTrack(raw)
	// Signal bands must be ~1.0 (all at the same level = the 95th percentile).
	for i := 0; i < 3; i++ {
		for j := 2; j < 24; j++ {
			if got[i][j] < 0.9 {
				t.Errorf("[%d][%d]: expected ~1.0 for uniform signal, got %v", i, j, got[i][j])
			}
		}
	}
	// Silence bands must be 0.
	for i := 0; i < 3; i++ {
		for j := 0; j < 2; j++ {
			if got[i][j] != 0 {
				t.Errorf("[%d][%d]: expected 0 for silence, got %v", i, j, got[i][j])
			}
		}
	}
}

func TestNormalizeSpectrumTrackSingleFrame(t *testing.T) {
	// Single frame with a clear peak-to-floor structure.
	raw := make([][24]float64, 1)
	for j := 0; j < 24; j++ {
		raw[0][j] = 0.001 + 0.999*float64(j)/23
	}
	got := normalizeSpectrumTrack(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(got))
	}
	// Highest band near 1.0.
	if got[0][23] < 0.9 {
		t.Errorf("expected highest band near 1.0, got %v", got[0][23])
	}
	// Lowest band near 0.0 (or zero).
	if got[0][0] > 0.1 {
		t.Errorf("expected lowest band near 0, got %v", got[0][0])
	}
}

func TestNormalizeSpectrumTrackNegativeValues(t *testing.T) {
	// Negative values are invalid and must be treated as zero.
	raw := make([][24]float64, 2)
	raw[0][0] = -1.0
	raw[0][1] = -0.5
	raw[0][2] = 1.0 // valid peak
	raw[1][0] = 0.1

	got := normalizeSpectrumTrack(raw)
	if got[0][0] != 0 {
		t.Errorf("negative value: expected 0, got %v", got[0][0])
	}
	if got[0][1] != 0 {
		t.Errorf("negative value: expected 0, got %v", got[0][1])
	}
	if got[0][2] <= 0 {
		t.Errorf("valid positive: expected >0, got %v", got[0][2])
	}
}

func TestNormalizeSpectrumTrackUniformNonzero(t *testing.T) {
	// All bands at the same uniform level with no deep-silence bands.
	// Prior to the fix, floor==refDB caused silenceThreshold to be
	// refDB+6, zeroing every value — producing silent output for a
	// uniform nonzero signal.
	raw := make([][24]float64, 3)
	for i := 0; i < 3; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = 0.5
		}
	}
	got := normalizeSpectrumTrack(raw)
	for i, frame := range got {
		for j, v := range frame {
			if v <= 0 {
				t.Errorf("[%d][%d]: expected positive value for uniform nonzero input, got %v", i, j, v)
			}
		}
	}
}

func TestNormalizeSpectrumTrackAllNonFiniteReturnsZeros(t *testing.T) {
	// All frames contain only NaN/Inf — must return all zeros.
	raw := make([][24]float64, 3)
	for i := 0; i < 3; i++ {
		for j := 0; j < 24; j++ {
			raw[i][j] = math.NaN()
		}
	}
	got := normalizeSpectrumTrack(raw)
	for i, frame := range got {
		for j, v := range frame {
			if v != 0 {
				t.Errorf("[%d][%d]: expected 0 for all-NaN input, got %v", i, j, v)
			}
		}
	}
}
