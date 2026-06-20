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
		loFreq := spectrumFMin * math.Pow(spectrumFMax/spectrumFMin, float64(b)/24.0)
		hiFreq := spectrumFMin * math.Pow(spectrumFMax/spectrumFMin, float64(b+1)/24.0)
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

func TestLowFrequencyResolution(t *testing.T) {
	// A 45 Hz tone must concentrate in a single band. The 2048-point FFT
	// (~23 Hz bins) smears it across several low bands; 8192 (~5.86 Hz)
	// resolves it so the dominant band dwarfs its neighbours.
	n := fftWindowSize
	pcm := make([]float64, n)
	for i := range pcm {
		w := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n-1)))
		pcm[i] = math.Sin(2*math.Pi*45*float64(i)/float64(sampleRate)) * w
	}
	coeff := fourier.NewFFT(n).Coefficients(nil, pcm)
	bands := fractionalLogBandEnergies(coeff, sampleRate)

	maxIdx := 0
	for b := 1; b < 24; b++ {
		if bands[b] > bands[maxIdx] {
			maxIdx = b
		}
	}
	if maxIdx == 0 || maxIdx == 23 {
		t.Fatalf("45 Hz tone landed in edge band %d; unexpected layout", maxIdx)
	}
	if bands[maxIdx] <= 4*bands[maxIdx-1] || bands[maxIdx] <= 4*bands[maxIdx+1] {
		t.Fatalf("45 Hz not concentrated: dominant[%d]=%.4g lo[%d]=%.4g hi[%d]=%.4g",
			maxIdx, bands[maxIdx], maxIdx-1, bands[maxIdx-1], maxIdx+1, bands[maxIdx+1])
	}
}

// rawFromDBFS converts a target band level in dBFS to a raw magnitude in the
// domain normalizeSpectrumTrack expects (matching fftFullScaleMag).
func rawFromDBFS(db float64) float64 {
	return fftFullScaleMag * math.Pow(10, db/20)
}

func TestNormalizeSpectrumTrackAbsoluteWindow(t *testing.T) {
	// A band at the reference fills the bar; range below maps to empty; the
	// midpoint maps to ~0.5; above the reference clamps to 1.0.
	raw := [][24]float64{{}}
	raw[0][0] = rawFromDBFS(spectrumRefDB)
	raw[0][1] = rawFromDBFS(spectrumRefDB - spectrumRangeDB)
	raw[0][2] = rawFromDBFS(spectrumRefDB - spectrumRangeDB/2)
	raw[0][3] = rawFromDBFS(spectrumRefDB + 6)

	got := normalizeSpectrumTrack(raw)
	if math.Abs(got[0][0]-1.0) > 1e-9 {
		t.Errorf("ref band: got %v, want 1.0", got[0][0])
	}
	if math.Abs(got[0][1]-0.0) > 1e-9 {
		t.Errorf("floor band: got %v, want 0.0", got[0][1])
	}
	if math.Abs(got[0][2]-0.5) > 1e-9 {
		t.Errorf("midpoint band: got %v, want 0.5", got[0][2])
	}
	if got[0][3] != 1.0 {
		t.Errorf("above-ref band: got %v, want clamp 1.0", got[0][3])
	}
}

func TestNormalizeSpectrumTrackNotGainInvariant(t *testing.T) {
	// The absolute window must rank a louder frame above a quieter one, unlike
	// the previous per-track percentile normalization.
	quiet := [][24]float64{{}}
	loud := [][24]float64{{}}
	for b := 0; b < 24; b++ {
		quiet[0][b] = rawFromDBFS(spectrumRefDB - 30)
		loud[0][b] = rawFromDBFS(spectrumRefDB - 5)
	}
	q := normalizeSpectrumTrack(quiet)
	l := normalizeSpectrumTrack(loud)
	if !(l[0][0] > q[0][0]+0.3) {
		t.Fatalf("absolute mapping must rank loud above quiet: quiet=%.3f loud=%.3f", q[0][0], l[0][0])
	}
}

func TestNormalizeSpectrumTrackShapeMonotonic(t *testing.T) {
	// A dB ramp across bands produces a non-decreasing bar height in [0,1].
	raw := [][24]float64{{}}
	for j := 0; j < 24; j++ {
		db := (spectrumRefDB - spectrumRangeDB) + float64(j)/23*spectrumRangeDB
		raw[0][j] = rawFromDBFS(db)
	}
	got := normalizeSpectrumTrack(raw)
	for j := 0; j < 24; j++ {
		if got[0][j] < 0 || got[0][j] > 1 {
			t.Errorf("band %d out of [0,1]: %v", j, got[0][j])
		}
		if j > 0 && got[0][j] < got[0][j-1]-1e-9 {
			t.Errorf("band %d (%v) < band %d (%v): not monotonic", j, got[0][j], j-1, got[0][j-1])
		}
	}
	if got[0][23] < 0.99 {
		t.Errorf("top of ramp should fill the bar, got %v", got[0][23])
	}
	if got[0][0] != 0 {
		t.Errorf("bottom of ramp should be empty, got %v", got[0][0])
	}
}

func TestNormalizeSpectrumTrackAllZero(t *testing.T) {
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
	if got := normalizeSpectrumTrack(nil); got != nil {
		t.Error("expected nil for nil input")
	}
	if got := normalizeSpectrumTrack([][24]float64{}); len(got) != 0 {
		t.Error("expected empty for empty input")
	}
}

func TestNormalizeSpectrumTrackInvalidValuesAreZero(t *testing.T) {
	// NaN, +/-Inf and negative values map to zero; a valid loud band survives.
	raw := make([][24]float64, 2)
	raw[0][0] = math.NaN()
	raw[0][1] = math.Inf(1)
	raw[0][2] = math.Inf(-1)
	raw[0][3] = -1.0
	raw[0][4] = rawFromDBFS(spectrumRefDB)
	got := normalizeSpectrumTrack(raw)
	for _, j := range []int{0, 1, 2, 3} {
		if got[0][j] != 0 {
			t.Errorf("invalid band %d: expected 0, got %v", j, got[0][j])
		}
	}
	if got[0][4] <= 0 {
		t.Errorf("valid band: expected >0, got %v", got[0][4])
	}
}

func TestNormalizeSpectrumTrackBelowFloorIsZero(t *testing.T) {
	// Energy more than spectrumRangeDB below the reference maps to exactly 0.
	raw := [][24]float64{{}}
	raw[0][0] = rawFromDBFS(spectrumRefDB - spectrumRangeDB - 10)
	got := normalizeSpectrumTrack(raw)
	if got[0][0] != 0 {
		t.Errorf("below-floor band: expected 0, got %v", got[0][0])
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
		raw[0][0] = 1.0 // band 0 peaks then decays
		raw[2][1] = 1.0 // band 1 peaks later
		raw[4][2] = 1.0 // band 2 peaks even later

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
