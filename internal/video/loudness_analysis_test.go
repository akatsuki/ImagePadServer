package video

import (
	"math"
	"testing"
)

func TestRelativeLoudnessPeakToUnity(t *testing.T) {
	// A single peak at RMS 1.0 must map to exactly 1.0.
	var rms [1000]float64
	rms[500] = 1.0 // peak
	for i := 0; i < 1000; i++ {
		if i != 500 {
			rms[i] = 0.0158489 // ~peak-36dB
		}
	}
	got := normalizeRelativeLoudness(rms)
	if got[500] != 1.0 {
		t.Fatalf("peak sample: got %v, want 1.0", got[500])
	}
}

func TestRelativeLoudnessFloorBoundary(t *testing.T) {
	// Values exactly 36 dB below peak must map to 0.
	// peak = 1.0 → peakDB = 0
	// 36 dB below = -36 dBFS → RMS = 10^(-36/20) ≈ 0.0158489
	var rms [1000]float64
	rms[0] = 1.0            // peak
	rms[1] = 0.0158489319   // exactly -36 dB from peak
	rms[2] = 0.01           // below -36 dB
	rms[3] = 0.001          // far below
	got := normalizeRelativeLoudness(rms)
	if got[0] != 1.0 {
		t.Fatalf("peak: got %v, want 1.0", got[0])
	}
	if got[1] != 0.0 {
		t.Fatalf("36dB-below-peak: got %v, want 0.0", got[1])
	}
	if got[2] != 0.0 {
		t.Fatalf("below threshold: got %v, want 0.0", got[2])
	}
	if got[3] != 0.0 {
		t.Fatalf("far below: got %v, want 0.0", got[3])
	}
}

func TestRelativeLoudnessSilence(t *testing.T) {
	// All-zero RMS must produce all-zero output.
	var rms [1000]float64
	got := normalizeRelativeLoudness(rms)
	for i, v := range got {
		if v != 0.0 {
			t.Fatalf("silence sample %d: got %v, want 0.0", i, v)
		}
	}
}

func TestRelativeLoudnessNonFinite(t *testing.T) {
	// Non-finite RMS values map to 0 at their positions.
	var rms [1000]float64
	rms[0] = math.NaN()
	rms[1] = math.Inf(1)
	rms[2] = math.Inf(-1)
	rms[3] = 1.0 // valid peak
	got := normalizeRelativeLoudness(rms)
	if got[0] != 0.0 {
		t.Fatalf("NaN position: got %v, want 0.0", got[0])
	}
	if got[1] != 0.0 {
		t.Fatalf("+Inf position: got %v, want 0.0", got[1])
	}
	if got[2] != 0.0 {
		t.Fatalf("-Inf position: got %v, want 0.0", got[2])
	}
	if got[3] != 1.0 {
		t.Fatalf("valid peak position: got %v, want 1.0", got[3])
	}
}

func TestRelativeLoudnessConstantGainInvariant(t *testing.T) {
	// Multiplying all RMS values by a constant <=1 must produce identical
	// normalized output within floating-point tolerance.
	var base [1000]float64
	for i := 0; i < 1000; i++ {
		base[i] = 0.1 + 0.9*float64(i)/999 // ramp from 0.1 to 1.0
	}
	base[500] = 1.0

	var scaled [1000]float64
	for i, v := range base {
		scaled[i] = v * 0.5
	}

	gotBase := normalizeRelativeLoudness(base)
	gotScaled := normalizeRelativeLoudness(scaled)

	for i := 0; i < 1000; i++ {
		if math.Abs(gotBase[i]-gotScaled[i]) > 1e-12 {
			t.Fatalf("sample %d: base=%v scaled=%v diff=%v", i, gotBase[i], gotScaled[i], math.Abs(gotBase[i]-gotScaled[i]))
		}
	}
}

func TestRelativeLoudnessQuietToLoud(t *testing.T) {
	// Verify a ramp from quiet to loud: the peak at the end maps to 1.0,
	// values early in the ramp are below 1.0.
	var rms [1000]float64
	for i := 0; i < 1000; i++ {
		rms[i] = 0.0001 + 0.9999*float64(i)/999
	}
	got := normalizeRelativeLoudness(rms)

	// Last sample is the peak
	if got[999] != 1.0 {
		t.Fatalf("peak at end: got %v, want 1.0", got[999])
	}
	// First sample must be well below 1.0 (it's ~ -80 dB below peak)
	if got[0] >= 1.0 {
		t.Fatalf("first sample should be <1.0, got %v", got[0])
	}
	// Should be monotonic non-decreasing
	for i := 1; i < 1000; i++ {
		if got[i] < got[i-1]-1e-15 {
			t.Fatalf("not monotonic at %d: %v < %v", i, got[i], got[i-1])
		}
	}
}

func TestRelativeLoudnessSinglePeak(t *testing.T) {
	// A single transient peak in otherwise quiet signal.
	var rms [1000]float64
	for i := 0; i < 1000; i++ {
		rms[i] = 0.001
	}
	rms[500] = 1.0

	got := normalizeRelativeLoudness(rms)
	if got[500] != 1.0 {
		t.Fatalf("transient peak: got %v, want 1.0", got[500])
	}
	// Values far from peak that are below peak-36dB should be 0
	if got[0] != 0.0 {
		t.Fatalf("quiet sample: got %v, want 0.0", got[0])
	}
}

func TestRelativeLoudnessAllNonFinite(t *testing.T) {
	// All-NaN envelope must produce all zeros.
	var rms [1000]float64
	for i := 0; i < 1000; i++ {
		rms[i] = math.NaN()
	}
	got := normalizeRelativeLoudness(rms)
	for i, v := range got {
		if v != 0.0 {
			t.Fatalf("all-NaN sample %d: got %v, want 0.0", i, v)
		}
	}
}

func TestRelativeLoudnessAllInf(t *testing.T) {
	// All-Inf envelope must produce all zeros.
	var rms [1000]float64
	for i := 0; i < 1000; i++ {
		rms[i] = math.Inf(1)
	}
	got := normalizeRelativeLoudness(rms)
	for i, v := range got {
		if v != 0.0 {
			t.Fatalf("all-Inf sample %d: got %v, want 0.0", i, v)
		}
	}
}

func TestRelativeLoudnessNegative(t *testing.T) {
	// Negative values must be treated as invalid and mapped to 0.
	var rms [1000]float64
	rms[0] = 1.0
	rms[1] = -0.5
	got := normalizeRelativeLoudness(rms)
	if got[0] != 1.0 {
		t.Fatalf("valid sample: got %v, want 1.0", got[0])
	}
	if got[1] != 0.0 {
		t.Fatalf("negative sample: got %v, want 0.0", got[1])
	}
}

func TestRelativeLoudnessUniform(t *testing.T) {
	// Uniformly loud (all values equal) — every point is the peak, so all 1.0.
	var rms [1000]float64
	for i := 0; i < 1000; i++ {
		rms[i] = 0.5
	}
	got := normalizeRelativeLoudness(rms)
	for i, v := range got {
		if v != 1.0 {
			t.Fatalf("uniform sample %d: got %v, want 1.0", i, v)
		}
	}
}

func TestRelativeLoudnessMixed(t *testing.T) {
	// Realistic mixed envelope with varying dynamics.
	var rms [1000]float64
	for i := 0; i < 1000; i++ {
		// Simulate: quiet intro, louder verse, peak chorus, quiet outro
		switch {
		case i < 200:
			rms[i] = 0.01
		case i < 400:
			rms[i] = 0.1
		case i < 600:
			rms[i] = 0.03 + 0.97*float64(i-400)/200
		case i < 800:
			rms[i] = 1.0
		default:
			rms[i] = 0.5 * (1.0 - float64(i-800)/200)
		}
	}
	got := normalizeRelativeLoudness(rms)
	// Peak region must be 1.0
	if got[700] != 1.0 {
		t.Fatalf("chorus peak: got %v, want 1.0", got[700])
	}
	// Intro (0.01 RMS) is 20*log10(0.01/1.0) = -40 dB from peak, which is below -36 dB
	if got[0] != 0.0 {
		t.Fatalf("quiet intro: got %v, want 0.0", got[0])
	}
	// All values must be in [0, 1]
	for i, v := range got {
		if v < 0 || v > 1 {
			t.Fatalf("sample %d out of range [0,1]: %v", i, v)
		}
	}
}
