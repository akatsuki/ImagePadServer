package video

import (
	"math"
	"testing"
)

func stereoTone(seconds int, amplitude float64) []int16 {
	monoSamples := seconds * sampleRate
	out := make([]int16, monoSamples*2)
	for i := 0; i < monoSamples; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/sampleRate) * amplitude * 32767)
		out[i*2], out[i*2+1] = v, v
	}
	return out
}

func feedAnalyzerChunks(t *testing.T, a *streamAnalyzer, pcm []int16) {
	t.Helper()
	const chunk = 8192
	for len(pcm) > 0 {
		n := chunk
		if n > len(pcm) {
			n = len(pcm)
		}
		if err := a.ConsumeStereo(pcm[:n]); err != nil {
			t.Fatal(err)
		}
		pcm = pcm[n:]
	}
}

// TestParallelAnalysisDeterministic verifies the parallel spectrum pipeline
// produces identical, correctly-ordered output across runs (catches ordering
// or data-race nondeterminism in the worker pool).
func TestParallelAnalysisDeterministic(t *testing.T) {
	pcm := stereoTone(3, 0.8)

	run := func() AudioAnalysis {
		a := newStreamAnalyzer()
		feedAnalyzerChunks(t, a, pcm)
		res, err := a.Finish()
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}
		return res
	}

	a1 := run()
	a2 := run()

	if len(a1.Frames) == 0 {
		t.Fatal("no frames produced")
	}
	if len(a1.Frames) != len(a2.Frames) {
		t.Fatalf("frame count differs: %d vs %d", len(a1.Frames), len(a2.Frames))
	}
	for i := range a1.Frames {
		if a1.Frames[i].Spectrum24 != a2.Frames[i].Spectrum24 {
			t.Fatalf("frame %d differs between runs (nondeterministic ordering/race)", i)
		}
	}
	if a1.Features.SpectralCentroid != a2.Features.SpectralCentroid ||
		a1.Features.LowFrequencyRatio != a2.Features.LowFrequencyRatio ||
		a1.Features.Fingerprint64 != a2.Features.Fingerprint64 {
		t.Fatal("track-level features differ between runs")
	}
}

func frameEnergy(frame AudioFrame) float64 {
	var total float64
	for _, value := range frame.Spectrum24 {
		total += value
	}
	return total
}

// stereoLowMultiTone sums three low-frequency sines that fall into spectrum
// bands 0, 1 and 3 (the boxes reported as permanently static). A correct
// fractional log-band mapping must give each of those bands energy; the old
// integer-truncating mapBands collapsed their bin range and left them at 0.
func stereoLowMultiTone(seconds int, amplitude float64) []int16 {
	monoSamples := seconds * sampleRate
	out := make([]int16, monoSamples*2)
	freqs := []float64{24, 30, 55} // ~band 0, band 1, band 3
	for i := 0; i < monoSamples; i++ {
		var s float64
		for _, f := range freqs {
			s += math.Sin(2 * math.Pi * f * float64(i) / float64(sampleRate))
		}
		s /= float64(len(freqs))
		v := int16(s * amplitude * 32767)
		out[i*2], out[i*2+1] = v, v
	}
	return out
}

func TestStreamAnalyzerLowBandsAreNotStatic(t *testing.T) {
	a := newStreamAnalyzer()
	feedAnalyzerChunks(t, a, stereoLowMultiTone(5, 0.6))
	analysis, err := a.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Frames) == 0 {
		t.Fatal("no frames produced")
	}
	mid := analysis.Frames[len(analysis.Frames)/2]
	for _, b := range []int{0, 1, 3} {
		if mid.Spectrum24[b] <= 0 {
			t.Errorf("band %d (box %d from left) is static 0 despite low-frequency content; "+
				"low bands collapsed (mapBands integer-bin truncation regression)", b, b+1)
		}
	}
}

func TestStreamAnalyzerKeepsBoundedWorkingBuffers(t *testing.T) {
	a := newStreamAnalyzer()
	chunk := stereoTone(1, 0.5)
	for i := 0; i < 60; i++ {
		feedAnalyzerChunks(t, a, chunk)
	}
	if len(a.pcm) > fftWindowSize*4 {
		t.Fatalf("retained pcm = %d samples, want bounded FFT overlap", len(a.pcm))
	}
	if len(a.monoBuf) > onsetHopSamples {
		t.Fatalf("retained mono = %d samples, want at most one onset hop", len(a.monoBuf))
	}
}

func TestStreamAnalyzerPreservesSpectrumAcrossWholeTrack(t *testing.T) {
	a := newStreamAnalyzer()
	feedAnalyzerChunks(t, a, stereoTone(30, 0.5))
	analysis, err := a.Finish()
	if err != nil {
		t.Fatal(err)
	}
	for _, fraction := range []float64{0.1, 0.5, 0.9} {
		idx := int(float64(len(analysis.Frames)-1) * fraction)
		if energy := frameEnergy(analysis.Frames[idx]); energy <= 0.01 {
			t.Fatalf("frame at %.0f%% has energy %.4f; whole-track spectrum was lost", fraction*100, energy)
		}
	}
}

func TestStreamAnalyzerLoudnessEnvelopeCoversWholeTrack(t *testing.T) {
	a := newStreamAnalyzer()
	feedAnalyzerChunks(t, a, stereoTone(10, 0.05))
	feedAnalyzerChunks(t, a, stereoTone(10, 0.8))
	analysis, err := a.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Features.LoudnessEnvelope[100] >= analysis.Features.LoudnessEnvelope[900] {
		t.Fatalf("envelope did not preserve quiet-to-loud change: early=%f late=%f",
			analysis.Features.LoudnessEnvelope[100], analysis.Features.LoudnessEnvelope[900])
	}
}
