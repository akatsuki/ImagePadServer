package video

import (
	"encoding/json"
	"testing"
)

func TestMusicSceneDocumentCarriesCanonicalFramePayload(t *testing.T) {
	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "odd", Artist: "赤月", Album: "album"},
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 1,
			Frames: []AudioFrame{{Spectrum24: [24]float64{0.25, 0.5, 1.0}}},
			WaveformFrames: [][]uint16{{100, 200, 300, 400}},
		},
	}

	doc, err := NewMusicSceneDocument(input, 640, 360, 30, 1, 4096, 44100)
	if err != nil {
		t.Fatalf("NewMusicSceneDocument: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("document validation: %v", err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	var roundTrip MusicSceneDocument
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal document: %v", err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatalf("round-trip validation: %v", err)
	}
	frame := roundTrip.FramesPayload[0]
	if frame.FrameIndex != 0 || len(frame.SpectrumQ16) != 24 || len(frame.WaveformQ16) != 4 {
		t.Fatalf("unexpected frame payload: %+v", frame)
	}
	if frame.TextLines[0] != "odd" || frame.TextLines[1] != "赤月" {
		t.Fatalf("metadata text was not carried: %#v", frame.TextLines)
	}
}

func TestApplyMusicSceneDocumentUsesQuantizedPayload(t *testing.T) {
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 1,
			Frames:   []AudioFrame{{}},
		},
	}
	doc, err := NewMusicSceneDocument(input, 640, 360, 30, 1, 4096, 44100)
	if err != nil {
		t.Fatalf("NewMusicSceneDocument: %v", err)
	}
	doc.FramesPayload[0].SpectrumQ16[0] = 32768
	doc.FramesPayload[0].LoudnessQ16[0] = 49152
	applied, err := ApplyMusicSceneDocument(input, doc)
	if err != nil {
		t.Fatalf("ApplyMusicSceneDocument: %v", err)
	}
	if got := applied.Analysis.Frames[0].Spectrum24[0]; got < 0.49 || got > 0.51 {
		t.Fatalf("spectrum did not round-trip through Q16: %f", got)
	}
	if got := applied.Analysis.Features.LoudnessEnvelope[0]; got < 0.74 || got > 0.76 {
		t.Fatalf("loudness did not round-trip through Q16: %f", got)
	}
}

func TestMusicSceneDocumentPadsOddWaveformPair(t *testing.T) {
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 1,
			Frames:   []AudioFrame{{}},
			WaveformFrames: [][]uint16{{100, 200, 300}},
		},
	}
	doc, err := NewMusicSceneDocument(input, 640, 360, 30, 1, 4096, 44100)
	if err != nil {
		t.Fatalf("NewMusicSceneDocument: %v", err)
	}
	waveform := doc.FramesPayload[0].WaveformQ16
	if len(waveform) != 4 || waveform[3] != 300 {
		t.Fatalf("odd waveform was not padded: %#v", waveform)
	}
}
