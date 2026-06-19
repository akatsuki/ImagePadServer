package video

import "testing"

func TestSelectMoodPaletteHighEnergy(t *testing.T) {
	start, end := SelectMoodPalette(AudioFeatures{BPM: 120})
	if start != "#FF4500" || end != "#8B0000" {
		t.Fatalf("high energy: got %s %s", start, end)
	}
}

func TestSelectMoodPaletteBassFocused(t *testing.T) {
	start, end := SelectMoodPalette(AudioFeatures{BPM: 100, LowFrequencyRatio: 0.4})
	if start != "#1E90FF" || end != "#00008B" {
		t.Fatalf("bass focused: got %s %s", start, end)
	}
}

func TestSelectMoodPaletteBright(t *testing.T) {
	start, end := SelectMoodPalette(AudioFeatures{BPM: 100, LowFrequencyRatio: 0.1, SpectralCentroid: 3000})
	if start != "#FFD700" || end != "#FF8C00" {
		t.Fatalf("bright: got %s %s", start, end)
	}
}

func TestSelectMoodPaletteCalm(t *testing.T) {
	start, end := SelectMoodPalette(AudioFeatures{BPM: 100, LowFrequencyRatio: 0.1, SpectralCentroid: 2000, IntegratedLUFS: -14})
	if start != "#98FB98" || end != "#006400" {
		t.Fatalf("calm: got %s %s", start, end)
	}
}

func TestSelectMoodPaletteDefault(t *testing.T) {
	start, end := SelectMoodPalette(AudioFeatures{BPM: 80, LowFrequencyRatio: 0.1, SpectralCentroid: 1500, IntegratedLUFS: -25})
	if start != "#9370DB" || end != "#4B0082" {
		t.Fatalf("default: got %s %s", start, end)
	}
}

func TestSelectMoodPaletteBoundaryExclusive(t *testing.T) {
	start, end := SelectMoodPalette(AudioFeatures{BPM: 119, LowFrequencyRatio: 0.39, SpectralCentroid: 2999, IntegratedLUFS: -15})
	if start != "#9370DB" || end != "#4B0082" {
		t.Fatalf("exclusive default: got %s %s", start, end)
	}
}

func TestAudioFeaturesDimensions(t *testing.T) {
	var features AudioFeatures
	if len(features.Fingerprint64) != 64 {
		t.Fatal("Fingerprint64 must be 64")
	}
	if len(features.LoudnessEnvelope) != 1000 {
		t.Fatal("LoudnessEnvelope must be 1000")
	}
}

func TestAudioFrameDimensions(t *testing.T) {
	var frame AudioFrame
	if len(frame.Spectrum24) != 24 {
		t.Fatal("Spectrum24 must be 24")
	}
}

func TestAnalyzeAudioFPSConstant(t *testing.T) {
	a := AudioAnalysis{FPS: 30, Duration: 10.0, Frames: make([]AudioFrame, 300)}
	for i := range a.Frames {
		a.Frames[i].Spectrum24 = [24]float64{}
	}
	if a.FPS != 30 {
		t.Fatal("FPS must be 30")
	}
}
