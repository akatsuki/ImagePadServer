package video

import (
	"context"
	"image/color"
	"math"
	"testing"
)

func TestPrepareGPUMusicSceneInputLeavesFullFrameFallbackGenerationToGPU(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	in := AudioRenderInput{Analysis: AudioAnalysis{Frames: []AudioFrame{{}}}}
	prepared, err := PrepareGPUMusicSceneInput(context.Background(), ffmpeg, in, 1280, 720)
	if err != nil {
		t.Fatalf("fallback preparation failed: %v", err)
	}
	if prepared.ArtworkTexture != nil || prepared.BaseTexture != nil {
		t.Fatalf("fallback preparation produced CPU pixel payloads: %+v", prepared)
	}
	if prepared.BackgroundArtworkTexture == nil || prepared.BackgroundArtworkTexture.TextureID != "gpu-fallback-note-mask" {
		t.Fatalf("fallback preparation did not provide the bounded note mask: %+v", prepared.BackgroundArtworkTexture)
	}
	scene := CanonicalMusicScene(prepared, 0, 0)
	if scene.Artwork != nil || scene.BackgroundArtwork == nil || scene.BaseTexture != nil {
		t.Fatalf("canonical fallback scene contains CPU pixel payloads: %+v", scene)
	}
}

func TestPrepareGPUMusicSceneInputBuildsBoundedLibassRuns(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	in := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Fixture Title", Artist: "Fixture Artist", Album: "Fixture Album"},
		Analysis: AudioAnalysis{Duration: 1, FPS: 30, Frames: []AudioFrame{{}}},
	}
	prepared, err := PrepareGPUMusicSceneInput(context.Background(), ffmpeg, in, 1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.PreparedGlyphAtlas == nil || len(prepared.PreparedGlyphAtlas.Atlas.Payload) == 0 {
		t.Fatal("missing bounded libass atlas")
	}
	if len(prepared.PreparedGlyphAtlas.TimeEvents) != 1 {
		t.Fatalf("time events are not directly indexed: %d", len(prepared.PreparedGlyphAtlas.TimeEvents))
	}
	scene := CanonicalMusicScene(prepared, 0, 0)
	if scene.GlyphAtlas == nil || len(scene.GlyphAtlas.BitmapRuns) != 4 {
		t.Fatalf("expected title/artist/album/time bitmap runs, got %#v", scene.GlyphAtlas)
	}
	if scene.TextOverlay != nil || scene.GlyphAtlas.Width >= 1280 || scene.GlyphAtlas.Height >= 720 {
		t.Fatalf("text preparation regressed to a full-screen raster: %#v", scene.GlyphAtlas)
	}
	layout, _ := LayoutForSize(1280, 720)
	overlay, err := RenderCanonicalASSOverlay(context.Background(), ffmpeg, in.Metadata, 1, layout, ForegroundMode{PrimaryColor: color.RGBA{255, 255, 255, 255}}, 1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	var sum, square float64
	var maxWant, maxRaw uint8
	const assColorAlpha = uint32(224)
	const ffDrawMaskScale = uint32(0x10307)
	alphaScale := (ffDrawMaskScale*assColorAlpha + 3) >> 8
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			want := overlay.Payload[y*int(overlay.RowStride)+x*4+3]
			if want > maxWant {
				maxWant = want
			}
			var got uint8
			for _, run := range scene.GlyphAtlas.BitmapRuns {
				if x >= run.ScreenRect.X && x < run.ScreenRect.X+run.ScreenRect.W && y >= run.ScreenRect.Y && y < run.ScreenRect.Y+run.ScreenRect.H {
					sx := run.AtlasRect.X + x - run.ScreenRect.X
					sy := run.AtlasRect.Y + y - run.ScreenRect.Y
					if a := scene.GlyphAtlas.Payload[sy*int(scene.GlyphAtlas.RowStride)+sx*4+3]; a > got {
						got = a
					}
				}
			}
			if got > maxRaw {
				maxRaw = got
			}
			got = uint8((uint32(got) * alphaScale * 255) >> 24)
			d := float64(int(want) - int(got))
			sum += math.Abs(d)
			square += d * d
		}
	}
	mae := sum / (1280 * 720)
	rmse := math.Sqrt(square / (1280 * 720))
	// The RGBA alpha-plane diagnostic takes a slightly different FFmpeg draw
	// path than the production YUV blend, but the reconstructed plane must stay
	// within sub-quantization error while preserving all 256 raw mask levels.
	if mae > 0.002 || rmse > 0.04 || maxRaw != 255 || maxWant != 224 {
		t.Fatalf("opaque bounded libass mask does not reconstruct canonical alpha: MAE=%.6f RMSE=%.6f maxRaw=%d maxCanonical=%d", mae, rmse, maxRaw, maxWant)
	}
}
