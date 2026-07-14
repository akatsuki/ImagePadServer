package obsrtmp

import "testing"

func TestGPUSceneValidationAndSingleTextOwner(t *testing.T) {
	scene := GPUScene{Kind: GPUSceneFallback, TextOwner: GPUTextOwnerGlyph, Progress: 0.5}
	if err := scene.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := FFmpegASSArgs(scene.TextOwner, "track.ass", "fonts"); got != nil {
		t.Fatalf("glyph scene must disable FFmpeg ASS, got %v", got)
	}
	scene.TextOwner = GPUTextOwnerASS
	if got := FFmpegASSArgs(scene.TextOwner, "track.ass", "fonts"); len(got) != 1 {
		t.Fatalf("ASS scene should retain exactly one ASS filter, got %v", got)
	}
}

func TestStaticTextureCacheCopiesValues(t *testing.T) {
	cache := NewStaticTextureCache()
	src := []byte{1, 2, 3}
	cache.Put("cover", src)
	src[0] = 9
	got, ok := cache.Get("cover")
	if !ok || got[0] != 1 {
		t.Fatalf("cache did not isolate source bytes: %v", got)
	}
	got[1] = 8
	again, _ := cache.Get("cover")
	if again[1] != 2 || cache.Len() != 1 {
		t.Fatalf("cache returned mutable storage: %v", again)
	}
}

