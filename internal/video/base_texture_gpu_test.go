package video

import (
	"context"
	"image"
	"image/color"
	"os"
	"testing"
	"time"
)

func TestGPUBaseTextureDiagnosticRoundTrip(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if exe == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not set")
	}
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			src.SetRGBA(x, y, color.RGBA{uint8(x * 3), uint8(y * 3), uint8(x + y), 255})
		}
	}
	base, err := NewBaseTextureMetadata("gpu-test-base", src, ColorLinear)
	if err != nil {
		t.Fatal(err)
	}
	scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, SpectrumQ16: make([]uint16, 24)}, BaseTexture: &base}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	frame, err := ProbeGPUSceneBaseTexture(ctx, exe, 64, 64, &scene)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			srcOff := y*int(base.RowStride) + x*4
			gotOff := y*int(frame.RowStride) + x*4
			for c := 0; c < 4; c++ {
				if frame.Payload[gotOff+c] != base.Payload[srcOff+c] {
					t.Fatalf("pixel mismatch at %d,%d channel %d: got=%d want=%d", x, y, c, frame.Payload[gotOff+c], base.Payload[srcOff+c])
				}
			}
		}
	}
}
