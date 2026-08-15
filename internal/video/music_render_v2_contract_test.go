package video

import "testing"

func TestMusicRenderV2ProductionRejectsCPUFinalRaster(t *testing.T) {
	job := MusicRenderV2Job{Width: 1280, Height: 720, FPS: 30, ArtworkMode: MusicRenderV2ArtworkFallback, Layers: []MusicRenderV2LayerReceipt{{Name: "background", Provider: MusicRenderV2NativeGPU}, {Name: "screen_rgba", Provider: MusicRenderV2GoldenUpload}}}
	if err := job.ValidateProduction(); err == nil {
		t.Fatal("expected CPU final raster contract to be rejected")
	}
}

func TestMusicRenderV2ProductionAcceptsNativeLayers(t *testing.T) {
	layers := make([]MusicRenderV2LayerReceipt, 0, len(requiredMusicRenderV2Layers))
	for _, name := range requiredMusicRenderV2Layers {
		layers = append(layers, MusicRenderV2LayerReceipt{Name: name, Provider: MusicRenderV2NativeGPU})
	}
	job := MusicRenderV2Job{Width: 1280, Height: 720, FPS: 30, ArtworkMode: MusicRenderV2ArtworkSource, Layers: layers}
	if err := job.ValidateProduction(); err != nil {
		t.Fatalf("native GPU job rejected: %v", err)
	}
}

func TestMusicRenderV2ProductionRejectsPartialNativeLayerManifest(t *testing.T) {
	job := MusicRenderV2Job{
		Width:       1280,
		Height:      720,
		FPS:         30,
		ArtworkMode: MusicRenderV2ArtworkSource,
		Layers:      []MusicRenderV2LayerReceipt{{Name: "background", Provider: MusicRenderV2NativeGPU}},
	}
	if err := job.ValidateProduction(); err == nil {
		t.Fatal("expected partial NativeGPU layer manifest to be rejected")
	}
}

func TestNewMusicRenderV2JobUsesLogicalNativeLayers(t *testing.T) {
	job := NewMusicRenderV2Job(AudioRenderInput{}, 1280, 720, 30, MusicRenderV2ArtworkFallback)
	if err := job.ValidateProduction(); err != nil {
		t.Fatalf("constructed job rejected: %v", err)
	}
	if len(job.Layers) != 8 || job.Layers[0].Name != "background" {
		t.Fatalf("unexpected logical layer manifest: %+v", job.Layers)
	}
}
