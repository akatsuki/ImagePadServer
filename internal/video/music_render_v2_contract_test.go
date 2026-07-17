package video

import "testing"

func TestMusicRenderV2ProductionRejectsCPUFinalRaster(t *testing.T) {
	job := MusicRenderV2Job{Width: 1280, Height: 720, FPS: 30, ArtworkMode: MusicRenderV2ArtworkFallback, Layers: []MusicRenderV2LayerReceipt{{Name: "background", Provider: MusicRenderV2NativeGPU}, {Name: "screen_rgba", Provider: MusicRenderV2GoldenUpload}}}
	if err := job.ValidateProduction(); err == nil {
		t.Fatal("expected CPU final raster contract to be rejected")
	}
}

func TestMusicRenderV2ProductionAcceptsNativeLayers(t *testing.T) {
	job := MusicRenderV2Job{Width: 1280, Height: 720, FPS: 30, ArtworkMode: MusicRenderV2ArtworkSource, Layers: []MusicRenderV2LayerReceipt{{Name: "background", Provider: MusicRenderV2NativeGPU}, {Name: "text", Provider: MusicRenderV2NativeGPU}}}
	if err := job.ValidateProduction(); err != nil {
		t.Fatalf("native GPU job rejected: %v", err)
	}
}
