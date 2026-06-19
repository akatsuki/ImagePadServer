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

func TestReleaseFraction(t *testing.T) {
	t.Run("at zero returns one", func(t *testing.T) {
		got := releaseFraction(0)
		if got != 1.0 {
			t.Errorf("releaseFraction(0) = %v, want 1.0", got)
		}
	})

	t.Run("at one quarter is approximately 39 percent", func(t *testing.T) {
		got := releaseFraction(0.25)
		// Expected: (exp(-0.875) - exp(-3.5)) / (1 - exp(-3.5)) ≈ 0.3987
		if got < 0.38 || got > 0.41 {
			t.Errorf("releaseFraction(0.25) = %v, want ~0.3987", got)
		}
	})

	t.Run("at one half is approximately 15 percent", func(t *testing.T) {
		got := releaseFraction(0.5)
		// Expected: (exp(-1.75) - exp(-3.5)) / (1 - exp(-3.5)) ≈ 0.1481
		if got < 0.13 || got > 0.17 {
			t.Errorf("releaseFraction(0.5) = %v, want ~0.1481", got)
		}
	})

	t.Run("at three quarters is approximately 4.5 percent", func(t *testing.T) {
		got := releaseFraction(0.75)
		// Expected: (exp(-2.625) - exp(-3.5)) / (1 - exp(-3.5)) ≈ 0.0436
		if got < 0.03 || got > 0.06 {
			t.Errorf("releaseFraction(0.75) = %v, want ~0.0436", got)
		}
	})

	t.Run("at one second is exactly zero", func(t *testing.T) {
		got := releaseFraction(1.0)
		if got != 0.0 {
			t.Errorf("releaseFraction(1.0) = %v, want 0.0", got)
		}
	})

	t.Run("beyond one second returns zero", func(t *testing.T) {
		if got := releaseFraction(1.5); got != 0.0 {
			t.Errorf("releaseFraction(1.5) = %v, want 0.0", got)
		}
		if got := releaseFraction(2.0); got != 0.0 {
			t.Errorf("releaseFraction(2.0) = %v, want 0.0", got)
		}
		if got := releaseFraction(10.0); got != 0.0 {
			t.Errorf("releaseFraction(10.0) = %v, want 0.0", got)
		}
	})

	t.Run("negative seconds returns one", func(t *testing.T) {
		got := releaseFraction(-0.5)
		if got != 1.0 {
			t.Errorf("releaseFraction(-0.5) = %v, want 1.0", got)
		}
	})

	t.Run("monotonically decreasing", func(t *testing.T) {
		prev := 1.0
		for step := 0; step <= 100; step++ {
			tSec := float64(step) / 100.0
			got := releaseFraction(tSec)
			if got > prev+1e-15 {
				t.Errorf("releaseFraction(%v) = %v > prev=%v (not monotonic)", tSec, got, prev)
			}
			prev = got
		}
	})
}

func TestApplySpectrumMotion(t *testing.T) {
	t.Run("empty input returns empty", func(t *testing.T) {
		got := applySpectrumMotion(nil, 30.0)
		if got != nil {
			t.Error("expected nil for nil input")
		}
		got = applySpectrumMotion([][24]float64{}, 30.0)
		if len(got) != 0 {
			t.Error("expected empty for empty input")
		}
	})

	t.Run("zero input stays zero", func(t *testing.T) {
		raw := make([][24]float64, 5)
		got := applySpectrumMotion(raw, 30.0)
		if len(got) != 5 {
			t.Fatalf("expected 5 frames, got %d", len(got))
		}
		for i, frame := range got {
			for j, v := range frame {
				if v != 0 {
					t.Errorf("[%d][%d] = %v, want 0", i, j, v)
				}
			}
		}
	})

	t.Run("attack approaches new value with 15ms time constant at 30fps", func(t *testing.T) {
		raw := make([][24]float64, 3)
		// Frame 0: silence
		// Frame 1: sudden peak on band 0
		raw[1][0] = 1.0
		// Frame 2: sustained
		raw[2][0] = 1.0

		fps := 30.0
		alpha := 1.0 - math.Exp(-1.0/fps/0.015)
		got := applySpectrumMotion(raw, fps)
		// Frame 0 should be 0 (no signal yet)
		if got[0][0] != 0 {
			t.Errorf("frame 0 band 0: expected 0, got %v", got[0][0])
		}
		// Frame 1: attack from 0 reaches ~89% at 30fps
		expect1 := alpha // ≈ 0.892
		if math.Abs(got[1][0]-expect1) > 0.01 {
			t.Errorf("frame 1 band 0: expected ~%v (attack), got %v", expect1, got[1][0])
		}
		// Frame 2: continued attack reaches ~99%
		expect2 := expect1 + (1.0-expect1)*alpha // ≈ 0.988
		if math.Abs(got[2][0]-expect2) > 0.01 {
			t.Errorf("frame 2 band 0: expected ~%v, got %v", expect2, got[2][0])
		}
	})

	t.Run("release decays over one second", func(t *testing.T) {
		fps := 30.0
		frames := int(fps) + 2 // one second plus margin
		raw := make([][24]float64, frames)
		// First frame: peak
		raw[0][0] = 1.0
		// Subsequent frames: silence (decay)
		// (all other frames remain zero)

		alpha := 1.0 - math.Exp(-1.0/fps/0.015)
		got := applySpectrumMotion(raw, fps)
		// Frame 0: attack from 0 reaches ~89% at 30fps
		if math.Abs(got[0][0]-alpha) > 0.01 {
			t.Errorf("frame 0 band 0: expected ~%v (attack), got %v", alpha, got[0][0])
		}
		// At frame 30 (1 second later), value should be ~0
		last := got[frames-1][0]
		if last != 0.0 {
			t.Errorf("final frame band 0: expected 0.0 after 1s release, got %v", last)
		}
		// Values should be monotonically decreasing after the peak
		for i := 1; i < len(got)-1; i++ {
			if got[i][0] > got[i-1][0] {
				t.Errorf("frame %d band 0 = %v > frame %d = %v (not decreasing during release)", i, got[i][0], i-1, got[i-1][0])
			}
		}
	})

	t.Run("new peak during release snaps up", func(t *testing.T) {
		raw := make([][24]float64, 10)
		raw[0][0] = 1.0 // initial peak
		raw[2][0] = 0.5 // intermediate drop
		raw[5][0] = 1.0 // new peak

		got := applySpectrumMotion(raw, 30.0)
		// Frame 0: attack from 0 reaches ~89% at 30fps
		alpha := 1.0 - math.Exp(-1.0/30.0/0.015)
		if math.Abs(got[0][0]-alpha) > 0.01 {
			t.Errorf("frame 0: expected ~%v (attack), got %v", alpha, got[0][0])
		}
		// Frame 1: releasing from 1.0, so < 1.0
		if got[1][0] >= 1.0 {
			t.Errorf("frame 1: expected < 1.0 (releasing), got %v", got[1][0])
		}
		// Frame 2: raw 0.5 which is > decayed value → snap to 0.5
		// Frame 5: raw 1.0 > displayed → attack to ~0.958 (15ms time constant)
		if got[5][0] < 0.95 || got[5][0] > 0.97 {
			t.Errorf("frame 5: expected ~0.958 (attack), got %v", got[5][0])
		}
		// After new peak, release again
		if got[6][0] >= 1.0 {
			t.Errorf("frame 6: expected < 1.0 (releasing from second peak), got %v", got[6][0])
		}
	})

	t.Run("never releases below raw value", func(t *testing.T) {
		fps := 30.0
		raw := make([][24]float64, 30) // exactly 1 second
		raw[0][0] = 1.0
		for i := 1; i < 30; i++ {
			raw[i][0] = 0.05 // low but non-zero floor
		}

		got := applySpectrumMotion(raw, fps)
		// During release, decayed value should never be below the raw floor
		for i := 1; i < 30; i++ {
			if got[i][0] < 0.05-1e-12 {
				t.Errorf("frame %d: %v < raw floor 0.05", i, got[i][0])
			}
		}
	})

	t.Run("multiple bands are independent", func(t *testing.T) {
		raw := make([][24]float64, 5)
		raw[0][0] = 1.0  // band 0 peaks then decays
		raw[2][1] = 1.0  // band 1 peaks later
		raw[4][2] = 1.0  // band 2 peaks even later

		got := applySpectrumMotion(raw, 30.0)
		// Band 0 decays after frame 0
		if got[4][0] >= 1.0 {
			t.Errorf("band 0 frame 4: expected < 1.0 (releasing), got %v", got[4][0])
		}
		// Band 1 frame 2: attack from 0 reaches ~0.892 at 30fps
		alpha := 1.0 - math.Exp(-1.0/30.0/0.015)
		if math.Abs(got[2][1]-alpha) > 0.01 {
			t.Errorf("band 1 frame 2: expected ~%v (attack), got %v", alpha, got[2][1])
		}
		// Band 2 frame 4: attack from 0 reaches ~0.892 at 30fps
		if math.Abs(got[4][2]-alpha) > 0.01 {
			t.Errorf("band 2 frame 4: expected ~%v (attack), got %v", alpha, got[4][2])
		}
	})

	t.Run("no upward jump during release at high framerates", func(t *testing.T) {
		// A single frame of signal at 60fps or 120fps followed by silence
		// must never cause the displayed value to rise during release.
		// This verifies the fix: peak must track displayed (newVal), not the
		// raw target, so release starts from the current displayed position.
		for _, fps := range []float64{60, 120} {
			raw := make([][24]float64, 10)
			raw[0][0] = 1.0 // single frame of signal, rest is silence

			got := applySpectrumMotion(raw, fps)
			prev := got[0][0]
			for i := 1; i < len(got); i++ {
				if got[i][0] > prev {
					t.Errorf("fps=%.0f frame %d: value %v > previous %v (upward jump during release)",
						fps, i, got[i][0], prev)
				}
				prev = got[i][0]
			}
		}
	})

	t.Run("exact zero after one second of trailing silence", func(t *testing.T) {
		// Spec section 5.3: "When the source remains below the silence floor,
		// every bar must be exactly zero after one second."
		fps := 60.0
		totalFrames := int(fps * 3) // 3 seconds
		raw := make([][24]float64, totalFrames)
		// First second: some activity
		for i := 0; i < int(fps); i++ {
			for j := 0; j < 24; j++ {
				raw[i][j] = 0.5 + 0.5*float64(j)/23
			}
		}
		// Last two seconds: trailing digital silence (all zero)
		// (frames after fps are already zero)

		got := applySpectrumMotion(raw, fps)
		// After one second of release from the last peak (frame fps-1 = 59),
		// release ends at frame fps+59 (= 119). Verify the final 60 frames (120-179).
		checkStart := int(fps * 2) // frame 120 — guaranteed 1s after last activity
		for i := checkStart; i < totalFrames; i++ {
			for j := 0; j < 24; j++ {
				if got[i][j] != 0.0 {
					t.Errorf("frame %d band %d: expected 0.0 after trailing silence, got %v", i, j, got[i][j])
				}
			}
		}
	})
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
