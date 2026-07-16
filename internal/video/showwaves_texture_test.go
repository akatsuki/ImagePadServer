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
	const waveW, waveH, frames = 752, 168, 3
	sources := make([][]byte, frames)
	err = StreamAudioWaveFrames(context.Background(), ffmpeg, fixture, waveW, waveH, frames, "#00FFFF@0.55", "", func(_ int, rgba []byte) error {
		// The stream callback index is not needed for this fixed-size probe;
		// retain frames in arrival order.
		for i := range sources {
			if sources[i] == nil {
				sources[i] = append([]byte(nil), rgba...)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("generate showwaves: %v", err)
	}
	stride := ((waveW*4 + int(GPURowAlignment) - 1) / int(GPURowAlignment)) * int(GPURowAlignment)
	for fi, source := range sources {
		payload := make([]byte, stride*waveH)
		for y := 0; y < waveH; y++ {
			copy(payload[y*stride:y*stride+waveW*4], source[y*waveW*4:(y+1)*waveW*4])
		}
		scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, SpectrumQ16: make([]uint16, 24)}, WaveformTexture: &BaseTextureMetadata{TextureID: "wave-test", Width: waveW, Height: waveH, RowStride: uint32(stride), Format: PixelRGBA8, ColorSpace: ColorSRGB, Payload: payload}}
		frame, err := ProbeGPUSceneWaveform(context.Background(), exe, waveW, waveH, &scene)
		if err != nil {
			t.Fatalf("GPU waveform probe frame %d: %v", fi, err)
		}
		got, err := GPUFrameToPackedRGBA(frame)
		if err != nil {
			t.Fatalf("pack GPU waveform frame %d: %v", fi, err)
		}
		if len(got) != len(source) {
			t.Fatalf("waveform frame %d size = %d, want %d", fi, len(got), len(source))
		}
		for i := range source {
			if got[i] != source[i] {
				t.Fatalf("waveform frame %d byte mismatch at %d: got %d want %d", fi, i, got[i], source[i])
			}
		}
	}
}
