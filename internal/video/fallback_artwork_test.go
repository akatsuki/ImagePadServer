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

// updateGolden is set when the RTK_UPDATE_GOLDEN environment variable is
// present; it causes TestRenderFallbackArtworkGolden to rewrite the golden
// PNG file instead of comparing against it.
func updateGolden() bool {
	v, _ := os.LookupEnv("RTK_UPDATE_GOLDEN")
	return v == "1" || v == "true"
}

func TestPaletteForFeaturesHighEnergy(t *testing.T) {
	// BPM >= 130 triggers high energy.
	p := PaletteForFeatures(AudioFeatures{BPM: 130})
	want := Palette{Start: color.RGBA{122, 29, 79, 255}, End: color.RGBA{255, 107, 53, 255}}
	if p != want {
		t.Fatalf("high energy via BPM: got %+v, want %+v", p, want)
	}
	// IntegratedLUFS >= -11 also triggers high energy (OR condition).
	p = PaletteForFeatures(AudioFeatures{BPM: 0, IntegratedLUFS: -11})
	if p != want {
		t.Fatalf("high energy via loudness: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesBassFocused(t *testing.T) {
	// LowFrequencyRatio >= 0.45; must not match high energy first.
	p := PaletteForFeatures(AudioFeatures{
		BPM:               100,
		LowFrequencyRatio: 0.45,
		IntegratedLUFS:    -12,
	})
	want := Palette{Start: color.RGBA{36, 16, 63, 255}, End: color.RGBA{124, 58, 237, 255}}
	if p != want {
		t.Fatalf("bass focused: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesBright(t *testing.T) {
	// SpectralCentroid >= 3500; must not match high energy or bass first.
	p := PaletteForFeatures(AudioFeatures{
		BPM:               100,
		LowFrequencyRatio: 0.3,
		SpectralCentroid:  3500,
		IntegratedLUFS:    -12,
	})
	want := Palette{Start: color.RGBA{11, 85, 99, 255}, End: color.RGBA{32, 199, 201, 255}}
	if p != want {
		t.Fatalf("bright: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesCalm(t *testing.T) {
	// BPM < 95 AND IntegratedLUFS <= -16; must not match earlier rules.
	p := PaletteForFeatures(AudioFeatures{
		BPM:               94,
		LowFrequencyRatio: 0.3,
		SpectralCentroid:  1000,
		IntegratedLUFS:    -16,
	})
	want := Palette{Start: color.RGBA{31, 42, 68, 255}, End: color.RGBA{94, 92, 230, 255}}
	if p != want {
		t.Fatalf("calm: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesDefault(t *testing.T) {
	// No rule matches: BPM < 130, LFR < 0.45, SC < 3500, not calm (IL > -16).
	p := PaletteForFeatures(AudioFeatures{
		BPM:               94,
		LowFrequencyRatio: 0.3,
		SpectralCentroid:  1000,
		IntegratedLUFS:    -15,
	})
	want := Palette{Start: color.RGBA{23, 59, 87, 255}, End: color.RGBA{58, 134, 255, 255}}
	if p != want {
		t.Fatalf("default: got %+v, want %+v", p, want)
	}
}

func TestPaletteForFeaturesBoundaryExclusive(t *testing.T) {
	// Values just below each threshold fall through to default.
	p := PaletteForFeatures(AudioFeatures{
		BPM:               129, // < 130
		LowFrequencyRatio: 0.44, // < 0.45
		SpectralCentroid:  3499, // < 3500
		IntegratedLUFS:    -12, // < -11 but > -16, and BPM 129 is not < 95
	})
	want := Palette{Start: color.RGBA{23, 59, 87, 255}, End: color.RGBA{58, 134, 255, 255}}
	if p != want {
		t.Fatalf("boundary exclusive: got %+v, want %+v", p, want)
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

	if updateGolden() {
		if err := os.WriteFile(goldenPath, raw, 0644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Log("wrote golden file", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatal("rendered output differs from golden file")
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
