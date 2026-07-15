package video

import "testing"

// TestUnifiedMusicSceneCPUInventory freezes pure CPU-reference inputs that
// the canonical GPU scene producer must reproduce. It deliberately avoids
// FFmpeg/font rasterization and encoded bytes.
func TestUnifiedMusicSceneCPUInventory(t *testing.T) {
	fixtures := []struct {
		name   string
		width  int
		height int
		art    bool
		unicode bool
	}{
		{name: "embedded-cover-latin", width: 1280, height: 720, art: true},
		{name: "no-artwork-fallback", width: 640, height: 360},
		{name: "unicode-japanese-long-scroll", width: 1920, height: 1080, art: true, unicode: true},
		{name: "quiet-envelope", width: 1280, height: 720},
		{name: "loud-envelope", width: 1280, height: 720, art: true},
		{name: "fade-start-mid-end", width: 640, height: 360, art: true},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			layout, err := LayoutForSize(fixture.width, fixture.height)
			if err != nil {
				t.Fatal(err)
			}
			if layout.Artwork.W <= 0 || layout.Artwork.H <= 0 || layout.Title.W <= 0 || layout.Spectrum.W <= 0 || layout.Loudness.W <= 0 || layout.Progress.W <= 0 || layout.Time.W <= 0 {
				t.Fatalf("incomplete canonical layout: %+v", layout)
			}
			if got := ScrollOffset(0, float64(layout.Title.W+200), float64(layout.Title.W)); got != 0 {
				t.Fatalf("scroll must pause at start: %v", got)
			}
			overflow := 200.0
			if got := ScrollOffset(3+overflow/80, float64(layout.Title.W)+overflow, float64(layout.Title.W)); got >= 0 || got < -overflow {
				t.Fatalf("scroll must move within clipped range: %v", got)
			}
			if got := ScrollOffset(0, float64(layout.Title.W-1), float64(layout.Title.W)); got != 0 {
				t.Fatalf("short text must not scroll: %v", got)
			}
			if got := SpectrumFadeHeight(fixture.width); got < 1 {
				t.Fatalf("invalid spectrum fade height: %d", got)
			}
			if got := FormatMediaTime(0); got != "0:00" {
				t.Fatalf("elapsed time format: %q", got)
			}
			if fixture.unicode && fixture.name != "unicode-japanese-long-scroll" {
				t.Fatal("unicode fixture ownership changed")
			}
		})
	}
}
