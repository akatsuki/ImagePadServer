package video

import (
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/png"
	"math"
	"os"
	"strings"
)

// CanonicalMusicScene is the sole normalization boundary for both single HLS
// and playlist rendering. It intentionally does not mutate AudioRenderInput.
func CanonicalMusicScene(input AudioRenderInput, frameIndex uint64, ptsNS int64) MusicScenePayload {
	var spectrum []uint16
	var rms, peak float64
	if int(frameIndex) < len(input.Analysis.Frames) {
		f := input.Analysis.Frames[frameIndex]
		spectrum = make([]uint16, len(f.Spectrum24))
		for i, v := range f.Spectrum24 {
			if v < 0 || math.IsNaN(v) {
				v = 0
			}
			if v > 1 {
				v = 1
			}
			if v > peak {
				peak = v
			}
			spectrum[i] = uint16(math.Round(v * 65535))
		}
		rms = peak * 0.707
	}
	scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, FrameIndex: frameIndex, PTSNs: ptsNS, SpectrumQ16: spectrum, RMSQ15: uint16(math.Round(sceneClamp01(rms) * 32767)), PeakQ15: uint16(math.Round(sceneClamp01(peak) * 32767))}}
	if a, ok := normalizeArtwork(input.ArtworkPath); ok {
		scene.Artwork = &a
	}
	scene.GlyphAtlas = normalizeGlyphs(input.Metadata)
	return scene
}

func sceneClamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func normalizeArtwork(path string) (ArtworkMetadata, bool) {
	f, err := os.Open(path)
	if err != nil || path == "" {
		return ArtworkMetadata{}, false
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return ArtworkMetadata{}, false
	}
	const max = 512
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > max || h > max {
		s := float64(max) / float64(w)
		if float64(h)*s > max {
			s = float64(max) / float64(h)
		}
		w, h = int(float64(w)*s), int(float64(h)*s)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			sx := b.Min.X + xx*b.Dx()/w
			sy := b.Min.Y + yy*b.Dy()/h
			dst.Set(xx, yy, src.At(sx, sy))
		}
	}
	stride := (w*4 + 255) &^ 255
	payload := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		copy(payload[y*stride:y*stride+w*4], dst.Pix[y*dst.Stride:y*dst.Stride+w*4])
	}
	sum := sha256.Sum256(payload)
	return ArtworkMetadata{TextureID: "artwork-" + hex.EncodeToString(sum[:8]), Width: uint32(w), Height: uint32(h), RowStride: uint32(stride), Format: PixelRGBA8, ColorSpace: ColorSRGB, Alpha: true, Payload: payload, AssetHash: hex.EncodeToString(sum[:])}, true
}

func normalizeGlyphs(meta AudioMetadata) *GlyphAtlasMetadata {
	text := strings.TrimSpace(strings.Join([]string{meta.Title, meta.Artist, meta.Album}, " · "))
	if text == "" {
		return nil
	}
	const gw, gh, cols = 24, 40, 32
	runes := []rune(text)
	if len(runes) > 256 {
		runes = runes[:256]
	}
	w := cols * gw
	h := gh
	stride := (w*4 + 255) &^ 255
	payload := make([]byte, stride*h)
	glyphs := make([]GlyphEntry, 0, len(runes))
	seen := map[rune]bool{}
	for i, r := range runes {
		if seen[r] {
			continue
		}
		seen[r] = true
		x := uint32((i % cols) * gw)
		glyphs = append(glyphs, GlyphEntry{ID: string(r), X: x, Y: 0, Width: gw, Height: gh, Advance: gw})
		for yy := 2; yy < gh-2; yy++ {
			for xx := 2; xx < gw-2; xx++ {
				if (xx+yy)%7 < 4 {
					off := yy*stride + int(x) + xx
					payload[off] = 255
					payload[off+1] = 255
					payload[off+2] = 255
					payload[off+3] = 255
				}
			}
		}
	}
	sum := sha256.Sum256(payload)
	runs := []TextRun{{Text: text, X: 96, Y: 500, SizePx: 40, RGBA: [4]uint8{255, 255, 255, 255}, Opacity: 1}}
	return &GlyphAtlasMetadata{TextureID: "glyphs-" + hex.EncodeToString(sum[:8]), FontFamily: "canonical-sans", FontWeight: 500, FallbackOrder: []string{"Noto Sans CJK JP", "Segoe UI", "sans-serif"}, Width: uint32(w), Height: uint32(h), RowStride: uint32(stride), GlyphCount: uint32(len(glyphs)), MissingGlyphID: "?", Payload: payload, AssetHash: hex.EncodeToString(sum[:]), Glyphs: glyphs, TextRuns: runs}
}
