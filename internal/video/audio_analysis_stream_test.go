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

func frameEnergy(frame AudioFrame) float64 {
	var total float64
	for _, value := range frame.Spectrum24 {
		total += value
	}
	return total
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
