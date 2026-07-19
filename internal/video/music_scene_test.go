package video

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"reflect"
	"testing"
)

func TestRenderCanonicalTextOverlayGolden(t *testing.T) {
	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	o := RenderCanonicalTextOverlay(AudioMetadata{Title: "Fixture Title", Artist: "Fixture Artist", Album: "Fixture Album"}, layout, 1280, 720)
	if o == nil || len(o.Payload) == 0 {
		t.Fatal("missing overlay raster")
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	if o.AssetHash != "" {
		t.Logf("overlay golden hash=%s", o.AssetHash)
	}
}

func TestCanonicalMusicSceneNormalizesAssetsAndMetadata(t *testing.T) {
	p := t.TempDir() + "/cover.png"
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	cover := image.NewRGBA(image.Rect(0, 0, 1, 1))
	cover.SetRGBA(0, 0, color.RGBA{20, 40, 80, 255})
	if err := png.Encode(f, cover); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	in := AudioRenderInput{ArtworkPath: p, Metadata: AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"}, Analysis: AudioAnalysis{Frames: []AudioFrame{{Spectrum24: [24]float64{.5}}}}}
	s := CanonicalMusicScene(in, 0, 123)
	if s.Artwork == nil || len(s.Artwork.Payload) == 0 || s.Artwork.AssetHash == "" {
		t.Fatal("artwork was not normalized")
	}
	if s.GlyphAtlas == nil || len(s.GlyphAtlas.Glyphs) == 0 || len(s.GlyphAtlas.TextRuns) != 4 {
		t.Fatal("text assets were not normalized")
	}
	if s.GlyphAtlas.TextRuns[0].X != float32(s.Layout.Title.X) || s.GlyphAtlas.TextRuns[1].Y <= float32(s.Layout.Artist.Y) || s.GlyphAtlas.TextRuns[2].SizePx != 24 || s.GlyphAtlas.TextRuns[3].Text != "0:00 / 0:00" {
		t.Fatalf("text runs did not preserve canonical field rectangles: %+v", s.GlyphAtlas.TextRuns)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var round MusicScenePayload
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, round) {
		t.Fatal("scene JSON roundtrip changed canonical assets")
	}
}

func TestCanonicalMusicSceneUsesPreparedArtworkInput(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{7, 8, 9, 255})
	prepared, err := artworkMetadataFromRGBA("prepared", img)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepared artwork violates GPU contract: %v", err)
	}
	scene := CanonicalMusicScene(AudioRenderInput{ArtworkTexture: &prepared}, 0, 0)
	if scene.Artwork == nil || scene.Artwork.TextureID != "prepared" || scene.Artwork.Payload[0] != 7 {
		t.Fatalf("prepared artwork was not preserved: %#v", scene.Artwork)
	}
	prepared.Payload[0] = 99
	if scene.Artwork.Payload[0] != 7 {
		t.Fatal("scene artwork aliases mutable input payload")
	}
}

func TestCanonicalMusicSceneUsesPreparedForegroundMode(t *testing.T) {
	mode := ForegroundMode{
		PrimaryColor: color.RGBA{1, 2, 3, 255},
		AccentColor:  color.RGBA{4, 5, 6, 255},
		Overlay:      color.RGBA{7, 8, 9, 64},
	}
	scene := CanonicalMusicScene(AudioRenderInput{PreparedForeground: &mode}, 0, 0)
	if scene.Palette.Primary != ([4]uint8{1, 2, 3, 255}) ||
		scene.Palette.Accent != ([4]uint8{4, 5, 6, 255}) ||
		scene.Palette.Overlay != ([4]uint8{7, 8, 9, 64}) {
		t.Fatalf("prepared foreground was not applied: %+v", scene.Palette)
	}
	if scene.GlyphAtlas != nil && len(scene.GlyphAtlas.TextRuns) > 0 &&
		scene.GlyphAtlas.TextRuns[0].RGBA != scene.Palette.Primary {
		t.Fatalf("prepared primary was not propagated to glyph runs: %+v", scene.GlyphAtlas.TextRuns[0])
	}
}

func TestCanonicalMusicSceneIsStableAcrossRenderRoutes(t *testing.T) {
	in := AudioRenderInput{Analysis: AudioAnalysis{Frames: []AudioFrame{{Spectrum24: [24]float64{0, .5, 1}}}}}
	a := CanonicalMusicScene(in, 0, 0)
	b := CanonicalMusicScene(in, 0, 0)
	if a.Schema != MusicSceneSchema || a.Feature.Schema != GPUContractVersion {
		t.Fatalf("invalid scene schema: %#v", a)
	}
	if len(a.Feature.SpectrumQ16) != 24 || a.Feature.SpectrumQ16[1] != 32768 || a.Feature.SpectrumQ16[2] != 65535 {
		t.Fatalf("unexpected canonical quantization: %#v", a.Feature.SpectrumQ16[:3])
	}
	if !reflect.DeepEqual(a.Feature, b.Feature) {
		t.Fatalf("single and playlist scene inputs diverged: %#v != %#v", a.Feature, b.Feature)
	}
}

func TestCanonicalMusicSceneUsesRasterizedGlyphAtlas(t *testing.T) {
	in := AudioRenderInput{Metadata: AudioMetadata{Title: "WiFi", Artist: "A", Album: "B"}, Analysis: AudioAnalysis{Frames: []AudioFrame{{}}}}
	s := CanonicalMusicScene(in, 0, 0)
	if s.GlyphAtlas == nil || s.GlyphAtlas.FontFamily != "Noto Sans JP" {
		t.Fatalf("expected the same embedded Noto family as the CPU renderer, got %#v", s.GlyphAtlas)
	}
	var alpha int
	for i := 3; i < len(s.GlyphAtlas.Payload); i += 4 {
		if s.GlyphAtlas.Payload[i] != 0 {
			alpha++
		}
	}
	if alpha < 100 {
		t.Fatalf("rasterized atlas has too few covered pixels: %d", alpha)
	}
	if len(s.GlyphAtlas.Glyphs) < 4 || s.GlyphAtlas.Glyphs[0].Advance == s.GlyphAtlas.Glyphs[1].Advance {
		t.Fatalf("glyph advances do not reflect font metrics: %#v", s.GlyphAtlas.Glyphs[:min(4, len(s.GlyphAtlas.Glyphs))])
	}
}

func TestNormalizeGlyphsPreservesWeightAndUsesMetricTimeCentering(t *testing.T) {
	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	atlas := normalizeGlyphs(AudioMetadata{Title: "A", Artist: "A", Album: "A"}, layout, [4]uint8{255, 255, 255, 255}, 0, 5)
	if atlas == nil {
		t.Fatal("missing glyph atlas")
	}
	byID := make(map[string]GlyphEntry, len(atlas.Glyphs))
	for _, glyph := range atlas.Glyphs {
		byID[glyph.ID] = glyph
	}
	for _, id := range []string{"600:A", "500:A", "400:A"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing weight-specific glyph %q", id)
		}
	}
	timeRun := atlas.TextRuns[len(atlas.TextRuns)-1]
	var advance float32
	for _, r := range timeRun.Text {
		advance += byID[musicGlyphID(timeRun.FontWeight, r)].Advance
	}
	wantX := float32(layout.Time.X) + (float32(layout.Time.W)-advance*timeRun.SizePx/musicAtlasLayoutEm)/2
	if math.Abs(float64(timeRun.X-wantX)) > 0.01 {
		t.Fatalf("time run x=%f, metric-centered x=%f", timeRun.X, wantX)
	}
}

func TestNormalizeGlyphsAllocatesMultipleAtlasRows(t *testing.T) {
	meta := AudioMetadata{Title: "ABCDEFGHIJKLMNOPQRSTUVWX"}
	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	atlas := normalizeGlyphs(meta, layout, [4]uint8{255, 255, 255, 255}, 0, 5)
	if atlas == nil || len(atlas.Glyphs) < 17 {
		t.Fatalf("atlas glyphs = %v", atlas)
	}
	seen := map[[2]uint32]bool{}
	for _, glyph := range atlas.Glyphs {
		key := [2]uint32{glyph.X, glyph.Y}
		if seen[key] {
			t.Fatalf("duplicate atlas cell for glyph %q at %v", glyph.ID, key)
		}
		seen[key] = true
	}
	if atlas.Height < 128 || atlas.Glyphs[16].Y < 64 {
		t.Fatalf("atlas did not allocate a second row: height=%d glyph16=%+v", atlas.Height, atlas.Glyphs[16])
	}
}

func TestMusicSceneDynamicsUsesCanonicalRelativeLoudness(t *testing.T) {
	var features AudioFeatures
	for i := range features.LoudnessEnvelope {
		features.LoudnessEnvelope[i] = 0.5
	}
	features.LoudnessEnvelope[999] = 1

	dynamics := musicSceneDynamics(features, 0, 1, 0)
	want := normalizeRelativeLoudness(features.LoudnessEnvelope)
	wantQ := uint16(math.Round(want[0] * 65535))
	if dynamics.LoudnessEnvelope[0] != wantQ {
		t.Fatalf("GPU loudness Q16 = %d, want CPU canonical %d", dynamics.LoudnessEnvelope[0], wantQ)
	}
}
