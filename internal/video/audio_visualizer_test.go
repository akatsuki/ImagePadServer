package video

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestAudioVisualizerFFmpegArgsBasic(t *testing.T) {
	args := AudioVisualizerFFmpegArgs("audio.m4a", "subtitles.ass", "C:\\fonts", "media-1", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	if len(args) == 0 {
		t.Fatal("empty args")
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "audio.m4a") {
		t.Error("missing audio path")
	}
	if !strings.Contains(joined, "subtitles.ass") {
		t.Error("missing ass path")
	}
	if !strings.Contains(joined, "fontsdir") {
		t.Error("missing fontsdir")
	}
}

func TestAudioVisualizerFFmpegArgsContainsShowwaves(t *testing.T) {
	args := AudioVisualizerFFmpegArgs("a.m4a", "b.ass", "/fonts", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "showwaves") {
		t.Error("args MUST contain showwaves")
	}
}

func TestAudioVisualizerFFmpegArgsPipeInput(t *testing.T) {
	args := AudioVisualizerFFmpegArgs("audio.m4a", "sub.ass", "/f", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "pipe:0") {
		t.Error("args MUST read rawvideo from pipe:0")
	}
}

func TestAudioVisualizerFFmpegArgsHLS(t *testing.T) {
	args := AudioVisualizerFFmpegArgs("a.m4a", "s.ass", "/f", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "hls") {
		t.Error("args MUST contain hls")
	}
}

func TestWriteVisualizerRGBAFramesBasic(t *testing.T) {
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 1.0,
			Frames:   make([]AudioFrame, 30),
		},
	}
	for i := range input.Analysis.Frames {
		input.Analysis.Frames[i].Spectrum24 = [24]float64{}
	}
	var buf bytes.Buffer
	err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, 128, 72)
	if err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}
	expected := 30 * 128 * 72 * 4
	if buf.Len() != expected {
		t.Fatalf("output size: got %d, want %d", buf.Len(), expected)
	}
}

func TestWriteVisualizerRGBAFramesSpectrumBars(t *testing.T) {
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 1.0 / 30,
			Frames:   make([]AudioFrame, 1),
		},
	}
	input.Analysis.Frames[0].Spectrum24 = [24]float64{}
	for i := 0; i < 24; i++ {
		input.Analysis.Frames[0].Spectrum24[i] = float64(i) / 24.0
	}
	var buf bytes.Buffer
	err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, 128, 72)
	if err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("empty output")
	}
}

func TestWriteVisualizerRGBAFramesZeroFrames(t *testing.T) {
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 0,
			Frames:   []AudioFrame{},
		},
	}
	var buf bytes.Buffer
	err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, 128, 72)
	if err == nil {
		t.Fatal("expected error for zero frames")
	}
}

func TestWriteVisualizerRGBAFramesFPS30(t *testing.T) {
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 2.0,
			Frames:   make([]AudioFrame, 60),
		},
	}
	for i := range input.Analysis.Frames {
		input.Analysis.Frames[i].Spectrum24 = [24]float64{}
	}
	var buf bytes.Buffer
	err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, 128, 72)
	if err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}
	expected := 60 * 128 * 72 * 4
	if buf.Len() != expected {
		t.Fatalf("output size: got %d, want %d", buf.Len(), expected)
	}
}
