package video

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestShowwavesTextureGPUReadback(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	fixture := filepath.Join("..", "..", ".tmp", "music-fixtures", "embedded-cover-latin.wav")
	if exe == "" {
		t.Skip("GPU sidecar not configured")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Skipf("GPU sidecar unavailable: %v", err)
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("waveform fixture unavailable: %v", err)
	}
	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	const waveW, waveH, frames = 752, 168, 1
	var source []byte
	err = StreamAudioWaveFrames(context.Background(), ffmpeg, fixture, waveW, waveH, frames, "#00FFFF@0.55", "", func(_ int, rgba []byte) error {
		source = append([]byte(nil), rgba...)
		return nil
	})
	if err != nil {
		t.Fatalf("generate showwaves: %v", err)
	}
	stride := ((waveW*4 + int(GPURowAlignment) - 1) / int(GPURowAlignment)) * int(GPURowAlignment)
	payload := make([]byte, stride*waveH)
	for y := 0; y < waveH; y++ {
		copy(payload[y*stride:y*stride+waveW*4], source[y*waveW*4:(y+1)*waveW*4])
	}
	scene := MusicScenePayload{
		Schema:          MusicSceneSchema,
		Feature:         AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, SpectrumQ16: make([]uint16, 24)},
		WaveformTexture: &BaseTextureMetadata{TextureID: "wave-test", Width: waveW, Height: waveH, RowStride: uint32(stride), Format: PixelRGBA8, ColorSpace: ColorSRGB, Payload: payload},
	}
	frame, err := ProbeGPUSceneWaveform(context.Background(), exe, waveW, waveH, &scene)
	if err != nil {
		t.Fatalf("GPU waveform probe: %v", err)
	}
	got, err := GPUFrameToPackedRGBA(frame)
	if err != nil {
		t.Fatalf("pack GPU waveform: %v", err)
	}
	if len(got) != len(source) {
		t.Fatalf("waveform size = %d, want %d", len(got), len(source))
	}
	for i := range source {
		if got[i] != source[i] {
			t.Fatalf("waveform byte mismatch at %d: got %d want %d", i, got[i], source[i])
		}
	}
}
