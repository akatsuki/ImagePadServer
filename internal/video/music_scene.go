package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	_ "image/png"
	"math"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// CanonicalMusicScene is the sole normalization boundary for both single HLS
// and playlist rendering. It intentionally does not mutate AudioRenderInput.
func CanonicalMusicScene(input AudioRenderInput, frameIndex uint64, ptsNS int64) MusicScenePayload {
	var spectrum []uint16
	var rms, peak float64
	if len(input.Analysis.Frames) > 0 {
		idx := int(frameIndex)
		if idx >= len(input.Analysis.Frames) {
			idx = len(input.Analysis.Frames) - 1
		}
		f := input.Analysis.Frames[idx]
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
	if duration < 0 || math.IsNaN(duration) {
		duration = 0
	}
	fps := input.Analysis.FPS
	if fps <= 0 {
		fps = 30
	}
	current := float64(frameIndex) / float64(fps)
	ratio := 0.0
	if duration > 0 {
		ratio = sceneClamp01(current / duration)
	}
	layout, _ := LayoutForSize(1280, 720)
	palette := canonicalScenePalette(input)
	scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, FrameIndex: frameIndex, PTSNs: ptsNS, SpectrumQ16: spectrum, RMSQ15: uint16(math.Round(sceneClamp01(rms) * 32767)), PeakQ15: uint16(math.Round(sceneClamp01(peak) * 32767))}, Layout: musicSceneLayout(layout), Dynamics: musicSceneDynamics(input.Analysis.Features, current, duration, ratio), Palette: palette}
	if a, ok := normalizeArtwork(input.ArtworkPath); ok {
		scene.Artwork = &a
	} else {
		// Keep the fallback tile explicit in the canonical scene so GPU and CPU
		// routes both render an artwork element when the source has no cover.
		a := fallbackArtwork(input.Analysis.Features)
		scene.Artwork = &a
	}
	scene.GlyphAtlas = normalizeGlyphs(input.Metadata, layout, palette.Primary, current, duration)
	if input.TextOverlay != nil {
		scene.TextOverlay = input.TextOverlay
	} else {
		scene.TextOverlay = RenderCanonicalTextOverlay(input.Metadata, layout, 1280, 720)
	}
	scene.Fingerprint = musicSceneFingerprint(scene)
	return scene
}

// RenderCanonicalTextOverlay emits the bounded RGBA overlay raster and its
// provenance. It is a diagnostic transport artifact; ASS/production routes
// remain unchanged.
func RenderCanonicalTextOverlay(meta AudioMetadata, layout VisualizerLayout, width, height uint32) *TextOverlayMetadata {
	if width == 0 || height == 0 {
		return nil
	}
	atlas := normalizeGlyphs(meta, layout, [4]uint8{255, 255, 255, 255}, 0, 0)
	if atlas == nil {
		return nil
	}
	sum := sha256.Sum256(atlas.Payload)
	return &TextOverlayMetadata{Kind: "atlas", Title: strings.TrimSpace(meta.Title), Artist: strings.TrimSpace(meta.Artist), Album: strings.TrimSpace(meta.Album), FontFamily: "Go Regular", FontWeight: 400, SizePx: 48, RGBA: [4]uint8{255, 255, 255, 255}, Opacity: 1, Width: atlas.Width, Height: atlas.Height, RowStride: atlas.RowStride, Format: PixelRGBA8, ColorSpace: ColorSRGB, Premultiplied: true, Payload: atlas.Payload, AssetHash: hex.EncodeToString(sum[:]), RendererID: "imagepad-canonical-overlay", RendererVersion: "1"}
}

func fallbackArtwork(features AudioFeatures) ArtworkMetadata {
	const w, h = 128, 128
	p := PaletteForFeatures(features)
	payload := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			t := uint32(y * 255 / (h - 1))
			payload[i] = uint8((uint32(p.Start.R)*(255-t) + uint32(p.End.R)*t) / 255)
			payload[i+1] = uint8((uint32(p.Start.G)*(255-t) + uint32(p.End.G)*t) / 255)
			payload[i+2] = uint8((uint32(p.Start.B)*(255-t) + uint32(p.End.B)*t) / 255)
			payload[i+3] = 255
		}
	}
	// Deterministic music-note silhouette, matching the CPU fallback's central
	// icon without introducing a font dependency into the GPU scene producer.
	for y := 38; y < 98; y++ {
		for x := 62; x < 72; x++ {
			if y < 78 || x < 68 {
				i := (y*w + x) * 4
				payload[i], payload[i+1], payload[i+2] = 255, 255, 255
			}
		}
	}
	for y := 86; y < 104; y++ {
		for x := 42; x < 70; x++ {
			if ((x-56)*(x-56))/196+((y-95)*(y-95))/81 <= 1 {
				i := (y*w + x) * 4
				payload[i], payload[i+1], payload[i+2] = 255, 255, 255
			}
		}
	}
	sum := sha256.Sum256(payload)
	return ArtworkMetadata{TextureID: "fallback-artwork-" + hex.EncodeToString(sum[:8]), Width: w, Height: h, RowStride: w * 4, Format: PixelRGBA8, ColorSpace: ColorSRGB, Alpha: true, Payload: payload, AssetHash: hex.EncodeToString(sum[:])}
}

// canonicalScenePalette keeps GPU colors tied to the same feature palette used
// by the CPU visualizer. Artwork contributes a restrained darkened average for
// the background, preserving readability while avoiding a fixed cyan scene.
func canonicalScenePalette(input AudioRenderInput) MusicScenePalette {
	p := PaletteForFeatures(input.Analysis.Features)
	primary := [4]uint8{255, 255, 255, 255}
	accent := [4]uint8{p.End.R, p.End.G, p.End.B, 255}
	background := [4]uint8{p.Start.R, p.Start.G, p.Start.B, 255}
	if a, ok := normalizeArtwork(input.ArtworkPath); ok && len(a.Payload) >= 4 {
		var r, g, b, n uint64
		for i := 0; i+3 < len(a.Payload); i += 4 {
			if a.Payload[i+3] < 16 {
				continue
			}
			r += uint64(a.Payload[i])
			g += uint64(a.Payload[i+1])
			b += uint64(a.Payload[i+2])
			n++
		}
		if n > 0 {
			background = [4]uint8{uint8(r / n / 3), uint8(g / n / 3), uint8(b / n / 3), 255}
		}
	}
	return MusicScenePalette{Primary: primary, Accent: accent, Background: background, Overlay: [4]uint8{0, 0, 0, 92}, BlurStrength: 220, Readability: 220}
}

func musicSceneLayout(l VisualizerLayout) MusicSceneLayout {
	r := func(v Rect) SceneRect { return SceneRect{X: v.X, Y: v.Y, W: v.W, H: v.H} }
	return MusicSceneLayout{Artwork: r(l.Artwork), Title: r(l.Title), Artist: r(l.Artist), Album: r(l.Album), Spectrum: r(l.Spectrum), Loudness: r(l.Loudness), Progress: r(l.Progress), Time: r(l.Time)}
}

func musicSceneDynamics(features AudioFeatures, current, duration, ratio float64) MusicSceneDynamics {
	toQ := func(v float64) uint16 { v = sceneClamp01(v); return uint16(math.Round(v * 65535)) }
	env := make([]uint16, len(features.LoudnessEnvelope))
	trend := SmoothLoudnessTrend(features.LoudnessEnvelope, duration)
	for i, v := range features.LoudnessEnvelope {
		env[i] = toQ(v)
	}
	trendQ := make([]uint16, len(trend))
	for i, v := range trend {
		trendQ[i] = toQ(v)
	}
	alphaIn, alphaOut := float32(1), float32(1)
	if current < radioEdgeFadeSeconds {
		alphaIn = float32(sceneClamp01(current / radioEdgeFadeSeconds))
	}
	if duration > 0 && duration-current < radioEdgeFadeSeconds {
		alphaOut = float32(sceneClamp01((duration - current) / radioEdgeFadeSeconds))
	}
	return MusicSceneDynamics{CurrentSeconds: current, DurationSeconds: duration, ProgressRatio: ratio, EdgeFadeAlpha: alphaIn, EndFadeAlpha: alphaOut, LoudnessEnvelope: env, LoudnessTrend: trendQ, LoudnessGuides: [4]uint16{toQ(.25), toQ(.5), toQ(.75), toQ(1)}}
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

func normalizeGlyphs(meta AudioMetadata, layout VisualizerLayout, primary [4]uint8, current, duration float64) *GlyphAtlasMetadata {
	fields := []struct {
		text   string
		rect   Rect
		size   float32
		weight uint16
		center bool
	}{
		{strings.TrimSpace(meta.Title), layout.Title, 48, 600, false},
		{strings.TrimSpace(meta.Artist), layout.Artist, 28, 500, false},
		{strings.TrimSpace(meta.Album), layout.Album, 24, 400, false},
		{FormatMediaTime(int(math.Max(0, math.Floor(current)))) + " / " + FormatMediaTime(int(math.Max(0, math.Floor(duration)))), layout.Time, 22, 500, true},
	}
	var all []rune
	for _, f := range fields {
		all = append(all, []rune(f.text)...)
		if f.text != "" {
			all = append(all, '·')
		}
	}
	if len(all) > 0 && all[len(all)-1] == '·' {
		all = all[:len(all)-1]
	}
	if len(all) == 0 {
		return nil
	}
	// Use a real, deterministic embedded font for glyph coverage. The atlas
	// remains fixed-cell and bounded so the Rust side needs no protocol change;
	// unsupported runes use the existing deterministic placeholder pattern.
	const gw, gh, cols = 64, 64, 16
	runes := all
	if len(runes) > 256 {
		runes = runes[:256]
	}
	w := cols * gw
	h := gh
	stride := (w*4 + 255) &^ 255
	payload := make([]byte, stride*h)
	face, faceErr := opentype.Parse(goregular.TTF)
	var fontFace font.Face
	if faceErr == nil {
		fontFace, faceErr = opentype.NewFace(face, &opentype.FaceOptions{Size: 48, DPI: 72, Hinting: font.HintingNone})
		if faceErr != nil {
			fontFace = nil
		}
	}
	if fontFace != nil {
		defer fontFace.Close()
	}
	glyphs := make([]GlyphEntry, 0, len(runes))
	seen := map[rune]bool{}
	for i, r := range runes {
		if seen[r] {
			continue
		}
		seen[r] = true
		x := uint32((i % cols) * gw)
		advance := float32(gw)
		rasterized := false
		if fontFace != nil {
			_, _, ok := fontFace.GlyphBounds(r)
			if ok {
				dst := image.NewRGBA(image.Rect(int(x), 0, int(x)+gw, gh))
				d := &font.Drawer{Dst: dst, Src: image.NewUniform(color.White), Face: fontFace,
					Dot: fixed.Point26_6{X: fixed.I(int(x) + 4), Y: fixed.I(52)}}
				d.DrawString(string(r))
				for yy := 0; yy < gh; yy++ {
					copy(payload[yy*stride+int(x)*4:yy*stride+(int(x)+gw)*4], dst.Pix[yy*dst.Stride:yy*dst.Stride+gw*4])
				}
				adv, ok := fontFace.GlyphAdvance(r)
				if ok && adv > 0 {
					advance = float32(adv) / 64
					if advance > gw {
						advance = gw
					}
				}
				rasterized = true
			}
		}
		if !rasterized {
			// Deterministic tofu fallback for unsupported Unicode (including
			// Japanese when GoRegular has no glyph), preserving prior behavior.
			for yy := 4; yy < gh-4; yy++ {
				for xx := 4; xx < gw-4; xx++ {
					if xx == 4 || xx == gw-5 || yy == 4 || yy == gh-5 || (xx+yy)%11 < 2 {
						off := yy*stride + (int(x)+xx)*4
						payload[off], payload[off+1], payload[off+2], payload[off+3] = 255, 255, 255, 255
					}
				}
			}
		}
		glyphs = append(glyphs, GlyphEntry{ID: string(r), X: x, Y: 0, Width: gw, Height: gh, Advance: advance})
	}
	sum := sha256.Sum256(payload)
	runs := make([]TextRun, 0, len(fields))
	for _, f := range fields {
		if f.text == "" {
			continue
		}
		x := float32(f.rect.X)
		if f.center {
			// The GPU atlas uses a bounded monospace advance. Center the time
			// label in the same canonical rect as ASS alignment 5.
			approxWidth := float32(len([]rune(f.text))) * f.size * 0.6
			x += (float32(f.rect.W) - approxWidth) / 2
		}
		// TextRun coordinates are top-left screen bounds; CPU ASS positions
		// title/artist/album at the vertical center of each rect.
		y := float32(f.rect.Y) + (float32(f.rect.H)-f.size)/2
		runs = append(runs, TextRun{Text: f.text, X: x, Y: y, SizePx: f.size, RGBA: primary, Opacity: 1, FontFamily: "Noto Sans JP", FontWeight: f.weight})
	}
	return &GlyphAtlasMetadata{TextureID: "glyphs-" + hex.EncodeToString(sum[:8]), FontFamily: "Go Regular", FontWeight: 400, FallbackOrder: []string{"Noto Sans CJK JP", "Segoe UI", "sans-serif"}, Width: uint32(w), Height: uint32(h), RowStride: uint32(stride), GlyphCount: uint32(len(glyphs)), MissingGlyphID: "?", Payload: payload, AssetHash: hex.EncodeToString(sum[:]), Glyphs: glyphs, TextRuns: runs}
}
