package video

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestPaletteForFeaturesHighEnergy(t *testing.T) {
	p := PaletteForFeatures(AudioFeatures{BPM: 120})
	want := Palette{Start: color.RGBA{255, 69, 0, 255}, End: color.RGBA{139, 0, 0, 255}}
	if p != want {
		t.Fatalf("high energy: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesBassFocused(t *testing.T) {
	p := PaletteForFeatures(AudioFeatures{BPM: 100, LowFrequencyRatio: 0.4})
	want := Palette{Start: color.RGBA{30, 144, 255, 255}, End: color.RGBA{0, 0, 139, 255}}
	if p != want {
		t.Fatalf("bass focused: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesBright(t *testing.T) {
	p := PaletteForFeatures(AudioFeatures{BPM: 100, LowFrequencyRatio: 0.1, SpectralCentroid: 3000})
	want := Palette{Start: color.RGBA{255, 215, 0, 255}, End: color.RGBA{255, 140, 0, 255}}
	if p != want {
		t.Fatalf("bright: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesCalm(t *testing.T) {
	p := PaletteForFeatures(AudioFeatures{BPM: 100, LowFrequencyRatio: 0.1, SpectralCentroid: 2000, IntegratedLUFS: -14})
	want := Palette{Start: color.RGBA{152, 251, 152, 255}, End: color.RGBA{0, 100, 0, 255}}
	if p != want {
		t.Fatalf("calm: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesDefault(t *testing.T) {
	p := PaletteForFeatures(AudioFeatures{BPM: 80, LowFrequencyRatio: 0.1, SpectralCentroid: 1500, IntegratedLUFS: -25})
	want := Palette{Start: color.RGBA{147, 112, 219, 255}, End: color.RGBA{76, 0, 130, 255}}
	if p != want {
		t.Fatalf("default: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesBoundaryExclusive(t *testing.T) {
	p := PaletteForFeatures(AudioFeatures{BPM: 119, LowFrequencyRatio: 0.39, SpectralCentroid: 2999, IntegratedLUFS: -15})
	want := Palette{Start: color.RGBA{147, 112, 219, 255}, End: color.RGBA{76, 0, 130, 255}}
	if p != want {
		t.Fatalf("exclusive default: got %+v, want %+v", p, want)
	}
}

func TestRenderFallbackArtworkIsDeterministic(t *testing.T) {
	testFFmpeg := func(t *testing.T) string {
		t.Helper()
		path, err := ffmpegPath()
		if err != nil {
			t.Skipf("ffmpeg unavailable: %v", err)
		}
		return path
	}
	testFonts := func(t *testing.T) FontSet {
		t.Helper()
		fonts, err := VisualizerFonts()
		if err != nil {
			t.Skipf("fonts unavailable: %v", err)
		}
		return fonts
	}

	f := AudioFeatures{BPM: 132, IntegratedLUFS: -10}
	a, err := RenderFallbackArtwork(context.Background(), testFFmpeg(t), testFonts(t), f, color.RGBA{255, 255, 255, 224}, 288)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderFallbackArtwork(context.Background(), testFFmpeg(t), testFonts(t), f, color.RGBA{255, 255, 255, 224}, 288)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pngBytes(t, a), pngBytes(t, b)) {
		t.Fatal("render changed between invocations")
	}
}

func TestRenderFallbackArtworkGolden(t *testing.T) {
	testFFmpeg := func(t *testing.T) string {
		t.Helper()
		path, err := ffmpegPath()
		if err != nil {
			t.Skipf("ffmpeg unavailable: %v", err)
		}
		return path
	}
	testFonts := func(t *testing.T) FontSet {
		t.Helper()
		fonts, err := VisualizerFonts()
		if err != nil {
			t.Skipf("fonts unavailable: %v", err)
		}
		return fonts
	}

	f := AudioFeatures{BPM: 132, IntegratedLUFS: -10}
	img, err := RenderFallbackArtwork(context.Background(), testFFmpeg(t), testFonts(t), f, color.RGBA{255, 255, 255, 224}, 288)
	if err != nil {
		t.Fatal(err)
	}

	goldenPath := filepath.Join("testdata", "golden", "fallback-720.png")
	raw := pngBytes(t, img)

	if _, err := os.Stat(goldenPath); os.IsNotExist(err) {
		if err := os.WriteFile(goldenPath, raw, 0644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Log("wrote golden file", goldenPath)
	} else if err != nil {
		t.Fatalf("stat golden: %v", err)
	} else {
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden: %v", err)
		}
		if !bytes.Equal(raw, want) {
			t.Fatal("rendered output differs from golden file")
		}
	}
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
