package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	duration := input.Analysis.Duration
	if duration < 0 || math.IsNaN(duration) { duration = 0 }
	fps := input.Analysis.FPS
	if fps <= 0 { fps = 30 }
	current := float64(frameIndex) / float64(fps)
	ratio := 0.0
	if duration > 0 { ratio = sceneClamp01(current / duration) }
	layout, _ := LayoutForSize(1280, 720)
	palette := canonicalScenePalette(input)
	scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, FrameIndex: frameIndex, PTSNs: ptsNS, SpectrumQ16: spectrum, RMSQ15: uint16(math.Round(sceneClamp01(rms) * 32767)), PeakQ15: uint16(math.Round(sceneClamp01(peak) * 32767))}, Layout: musicSceneLayout(layout), Dynamics: musicSceneDynamics(input.Analysis.Features, current, duration, ratio), Palette: palette}
	if a, ok := normalizeArtwork(input.ArtworkPath); ok {
		scene.Artwork = &a
	}
	scene.GlyphAtlas = normalizeGlyphs(input.Metadata, layout, palette.Primary)
	scene.Fingerprint = musicSceneFingerprint(scene)
	return scene
}

// canonicalScenePalette keeps GPU colors tied to the same feature palette used
// by the CPU visualizer. Artwork contributes a restrained darkened average for
// the background, preserving readability while avoiding a fixed cyan scene.
func canonicalScenePalette(input AudioRenderInput) MusicScenePalette {
	p := PaletteForFeatures(input.Analysis.Features)
	primary := [4]uint8{255, 255, 255, 255}
	accent := [4]uint8{p.End.R, p.End.G, p.End.B, 255}
	background := [4]uint8{p.Start.R / 3, p.Start.G / 3, p.Start.B / 3, 255}
	if a, ok := normalizeArtwork(input.ArtworkPath); ok && len(a.Payload) >= 4 {
		var r, g, b, n uint64
		for i := 0; i+3 < len(a.Payload); i += 4 {
			if a.Payload[i+3] < 16 { continue }
			r += uint64(a.Payload[i]); g += uint64(a.Payload[i+1]); b += uint64(a.Payload[i+2]); n++
		}
		if n > 0 {
			background = [4]uint8{uint8(r / n / 3), uint8(g / n / 3), uint8(b / n / 3), 255}
		}
	}
	return MusicScenePalette{Primary: primary, Accent: accent, Background: background, Overlay: [4]uint8{0, 0, 0, 92}, BlurStrength: 220, Readability: 220}
}

func musicSceneLayout(l VisualizerLayout) MusicSceneLayout {
	r := func(v Rect) SceneRect { return SceneRect{X:v.X, Y:v.Y, W:v.W, H:v.H} }
	return MusicSceneLayout{Artwork:r(l.Artwork), Title:r(l.Title), Artist:r(l.Artist), Album:r(l.Album), Spectrum:r(l.Spectrum), Loudness:r(l.Loudness), Progress:r(l.Progress), Time:r(l.Time)}
}

func musicSceneDynamics(features AudioFeatures, current, duration, ratio float64) MusicSceneDynamics {
	toQ := func(v float64) uint16 { v = sceneClamp01(v); return uint16(math.Round(v*65535)) }
	env := make([]uint16, len(features.LoudnessEnvelope)); trend := SmoothLoudnessTrend(features.LoudnessEnvelope, duration)
	for i, v := range features.LoudnessEnvelope { env[i] = toQ(v) }
	trendQ := make([]uint16, len(trend)); for i, v := range trend { trendQ[i] = toQ(v) }
	alphaIn, alphaOut := float32(1), float32(1)
	if current < radioEdgeFadeSeconds { alphaIn = float32(sceneClamp01(current/radioEdgeFadeSeconds)) }
	if duration > 0 && duration-current < radioEdgeFadeSeconds { alphaOut = float32(sceneClamp01((duration-current)/radioEdgeFadeSeconds)) }
	return MusicSceneDynamics{CurrentSeconds: current, DurationSeconds: duration, ProgressRatio: ratio, EdgeFadeAlpha: alphaIn, EndFadeAlpha: alphaOut, LoudnessEnvelope: env, LoudnessTrend: trendQ, LoudnessGuides: [4]uint16{toQ(.25),toQ(.5),toQ(.75),toQ(1)}}
}

func musicSceneFingerprint(scene MusicScenePayload) string {
	scene.Fingerprint = ""
	b, _ := json.Marshal(scene)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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

func normalizeGlyphs(meta AudioMetadata, layout VisualizerLayout, primary [4]uint8) *GlyphAtlasMetadata {
	fields := []struct { text string; rect Rect; size float32 }{
		{strings.TrimSpace(meta.Title), layout.Title, 48},
		{strings.TrimSpace(meta.Artist), layout.Artist, 28},
		{strings.TrimSpace(meta.Album), layout.Album, 24},
	}
	var all []rune
	for _, f := range fields { all = append(all, []rune(f.text)...); if f.text != "" { all = append(all, '·') } }
	if len(all) > 0 && all[len(all)-1] == '·' { all = all[:len(all)-1] }
	if len(all) == 0 {
		return nil
	}
	const gw, gh, cols = 24, 40, 32
	runes := all
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
	runs := make([]TextRun, 0, len(fields))
	for _, f := range fields {
		if f.text == "" { continue }
		runs = append(runs, TextRun{Text: f.text, X: float32(f.rect.X), Y: float32(f.rect.Y), SizePx: f.size, RGBA: primary, Opacity: 1})
	}
	return &GlyphAtlasMetadata{TextureID: "glyphs-" + hex.EncodeToString(sum[:8]), FontFamily: "canonical-sans", FontWeight: 500, FallbackOrder: []string{"Noto Sans CJK JP", "Segoe UI", "sans-serif"}, Width: uint32(w), Height: uint32(h), RowStride: uint32(stride), GlyphCount: uint32(len(glyphs)), MissingGlyphID: "?", Payload: payload, AssetHash: hex.EncodeToString(sum[:]), Glyphs: glyphs, TextRuns: runs}
}
