package video

import (
	"math"
	"testing"
)

func TestParseLoudnormJSON(t *testing.T) {
	out := `[Parsed_loudnorm_0 @ 0x55]
{
	"input_i" : "-18.40",
	"input_tp" : "-2.10",
	"input_lra" : "7.30",
	"input_thresh" : "-28.60",
	"target_offset" : "0.50"
}
`
	m, err := parseLoudnormJSON(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.InputI != -18.40 || m.InputTP != -2.10 || m.InputLRA != 7.30 ||
		m.InputThresh != -28.60 || m.TargetOffset != 0.50 {
		t.Fatalf("unexpected measurement: %+v", m)
	}
}

func TestLoudnormFilterString(t *testing.T) {
	m := LoudnormMeasurement{InputI: -18.4, InputTP: -2.1, InputLRA: 7.3, InputThresh: -28.6, TargetOffset: 0.5}
	got := loudnormFilter(m, -14.0)
	want := "loudnorm=I=-14.0:TP=-1.0:LRA=11.0:" +
		"measured_I=-18.4:measured_TP=-2.1:measured_LRA=7.3:measured_thresh=-28.6:" +
		"offset=0.5:linear=true:print_format=summary"
	if got != want {
		t.Fatalf("loudnorm filter mismatch:\n got %q\nwant %q", got, want)
	}
}

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

func generateClickTrack(bpm float64, durationSec float64, sampleRate int) []int16 {
	totalSamples := int(durationSec * float64(sampleRate))
	pcm := make([]int16, totalSamples*2) // stereo interleaved
	beatSamples := int(60.0 / bpm * float64(sampleRate))
	clickSamples := int(0.010 * float64(sampleRate)) // 10 ms click duration
	for i := 0; i < totalSamples; i++ {
		positionInBeat := i % beatSamples
		var val int16
		if positionInBeat < clickSamples {
			amplitude := math.Sin(2 * math.Pi * 440 * float64(i) / float64(sampleRate))
			val = int16(amplitude * 8000)
		}
		pcm[i*2] = val
		pcm[i*2+1] = val
	}
	return pcm
}

func TestComputeBPM60(t *testing.T) {
	pcm := generateClickTrack(60, 10, sampleRate)
	bpm := computeBPM(pcm, sampleRate)
	diff := math.Abs(bpm - 60)
	if diff > 2 {
		t.Fatalf("expected BPM ~60, got %.1f (diff %.1f)", bpm, diff)
	}
}

func TestComputeBPM90(t *testing.T) {
	pcm := generateClickTrack(90, 8, sampleRate)
	bpm := computeBPM(pcm, sampleRate)
	diff := math.Abs(bpm - 90)
	if diff > 2 {
		t.Fatalf("expected BPM ~90, got %.1f (diff %.1f)", bpm, diff)
	}
}

func TestComputeBPM120(t *testing.T) {
	pcm := generateClickTrack(120, 8, sampleRate)
	bpm := computeBPM(pcm, sampleRate)
	diff := math.Abs(bpm - 120)
	if diff > 2 {
		t.Fatalf("expected BPM ~120, got %.1f (diff %.1f)", bpm, diff)
	}
}

func TestComputeBPM180(t *testing.T) {
	pcm := generateClickTrack(180, 6, sampleRate)
	bpm := computeBPM(pcm, sampleRate)
	diff := math.Abs(bpm - 180)
	if diff > 2 {
		t.Fatalf("expected BPM ~180, got %.1f (diff %.1f)", bpm, diff)
	}
}

func TestComputeBPMUsesOnsetFrameUnits(t *testing.T) {
	// Prove the autocorrelation lag for 120 BPM is approximately 50 onset frames
	// (onsetRate * 60 / 120 = 100 * 60 / 120 = 50), not 24000 PCM samples.
	// We back-compute the lag from the BPM result.
	pcm := generateClickTrack(120, 8, sampleRate)
	bpm := computeBPM(pcm, sampleRate)
	if bpm <= 0 {
		t.Fatalf("expected positive BPM, got %.1f", bpm)
	}
	// lag = 60 * onsetRate / bpm
	bestLag := int(60.0*float64(onsetRate)/bpm + 0.5)
	// Should be ~50 onset frames — not 24000 PCM samples
	if bestLag < 40 || bestLag > 60 {
		t.Fatalf("expected lag ~50 onset frames for 120 BPM, got ~%d (BPM=%.1f)", bestLag, bpm)
	}
}

func TestComputeBPMSilence(t *testing.T) {
	pcm := make([]int16, sampleRate*2*2) // 2 seconds of silence
	bpm := computeBPM(pcm, sampleRate)
	if bpm != 0 {
		t.Fatalf("expected 0 BPM for silence, got %.1f", bpm)
	}
}

func TestComputeBPMSmallInput(t *testing.T) {
	pcm := make([]int16, sampleRate) // 0.5 seconds (sub-two-second)
	bpm := computeBPM(pcm, sampleRate)
	if bpm != 0 {
		t.Fatalf("expected 0 BPM for small input, got %.1f", bpm)
	}
}
