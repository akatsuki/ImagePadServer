package video

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
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
	if s.GlyphAtlas == nil || s.GlyphAtlas.FontFamily != "Go Regular" {
		t.Fatalf("expected embedded raster font, got %#v", s.GlyphAtlas)
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
