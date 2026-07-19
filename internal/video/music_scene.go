package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	musicAtlasFontsOnce sync.Once
	musicAtlasFonts     map[uint16]*opentype.Font
)

// The 48px Noto raster is stored in a padded 64px cell. A 72px layout em
// matches libass/FreeType's hinted small-size ink and advances; treating the
// cell width itself as the em made the 22px time label about 17% too wide.
const musicAtlasLayoutEm float32 = 72

func parsedMusicAtlasFonts() map[uint16]*opentype.Font {
	musicAtlasFontsOnce.Do(func() {
		musicAtlasFonts = make(map[uint16]*opentype.Font, 3)
		for weight, path := range map[uint16]string{
			400: "fonts/NotoSansJP-Regular.ttf",
			500: "fonts/NotoSansJP-Medium.ttf",
			600: "fonts/NotoSansJP-SemiBold.ttf",
		} {
			data, err := embeddedFonts.ReadFile(path)
			if err != nil {
				continue
			}
			if parsed, err := opentype.Parse(data); err == nil {
				musicAtlasFonts[weight] = parsed
			}
		}
	})
	return musicAtlasFonts
}

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
	fingerprint := make([]uint16, len(input.Analysis.Features.Fingerprint64))
	for i, v := range input.Analysis.Features.Fingerprint64 {
		fingerprint[i] = uint16(math.Round(sceneClamp01(v) * 65535))
	}
	scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, FrameIndex: frameIndex, PTSNs: ptsNS, SpectrumQ16: spectrum, FingerprintQ16: fingerprint, RMSQ15: uint16(math.Round(sceneClamp01(rms) * 32767)), PeakQ15: uint16(math.Round(sceneClamp01(peak) * 32767))}, Layout: musicSceneLayout(layout), Dynamics: musicSceneDynamics(input.Analysis.Features, current, duration, ratio), Palette: palette}
	if input.ArtworkTexture != nil {
		a := *input.ArtworkTexture
		a.Payload = append([]byte(nil), input.ArtworkTexture.Payload...)
		scene.Artwork = &a
	} else if a, ok := normalizeArtwork(input.ArtworkPath); ok {
		scene.Artwork = &a
	}
	if input.BackgroundArtworkTexture != nil {
		a := *input.BackgroundArtworkTexture
		a.Payload = append([]byte(nil), input.BackgroundArtworkTexture.Payload...)
		scene.BackgroundArtwork = &a
	}
	if input.PreparedGlyphAtlas == nil {
		scene.GlyphAtlas = normalizeGlyphs(input.Metadata, layout, palette.Primary, current, duration)
	}
	if input.TextOverlay != nil {
		scene.TextOverlay = input.TextOverlay
	}
	if input.WaveformTexture != nil {
		scene.WaveformTexture = input.WaveformTexture
	}
	if input.PreparedForeground != nil {
		applyGPUForegroundMode(&scene, *input.PreparedForeground)
	}
	if input.PreparedGlyphAtlas != nil {
		scene.GlyphAtlas = input.PreparedGlyphAtlas.atlasAt(current, scene.Palette.Primary)
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
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillGradient(img, p.Start, p.End)
	foreground := color.RGBA{255, 255, 255, 224}
	drawFingerprint(img, features.Fingerprint64, color.RGBA{255, 255, 255, uint8(math.Round(0.26 * 255))}, w)
	if glyph, err := renderGlyphWithGo(FontSet{}, foreground, w); err == nil {
		draw.Draw(img, img.Bounds(), glyph, image.Point{}, draw.Over)
	}
	payload := img.Pix
	sum := sha256.Sum256(payload)
	return ArtworkMetadata{TextureID: "fallback-artwork-" + hex.EncodeToString(sum[:8]), Width: w, Height: h, RowStride: w * 4, Format: PixelRGBA8, ColorSpace: ColorSRGB, Alpha: true, Payload: payload, AssetHash: hex.EncodeToString(sum[:])}
}

func artworkMetadataFromRGBA(textureID string, img *image.RGBA) (ArtworkMetadata, error) {
	if img == nil || img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		return ArtworkMetadata{}, errors.New("invalid artwork image")
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	rowStride := ((uint32(w*4) + GPURowAlignment - 1) / GPURowAlignment) * GPURowAlignment
	payload := make([]byte, int(rowStride)*h)
	for y := 0; y < h; y++ {
		dst := y * int(rowStride)
		copy(payload[dst:dst+w*4], img.Pix[y*img.Stride:y*img.Stride+w*4])
	}
	sum := sha256.Sum256(payload)
	return ArtworkMetadata{TextureID: textureID, Width: uint32(w), Height: uint32(h), RowStride: rowStride, Format: PixelRGBA8, ColorSpace: ColorSRGB, Alpha: true, Payload: payload, AssetHash: hex.EncodeToString(sum[:])}, nil
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
	return MusicScenePalette{Primary: primary, Accent: accent, Background: background, Overlay: [4]uint8{0, 0, 0, 92}, FallbackEnd: [4]uint8{p.End.R, p.End.G, p.End.B, 255}, BlurStrength: 220, Readability: 220}
}

func musicSceneLayout(l VisualizerLayout) MusicSceneLayout {
	r := func(v Rect) SceneRect { return SceneRect{X: v.X, Y: v.Y, W: v.W, H: v.H} }
	return MusicSceneLayout{Artwork: r(l.Artwork), Title: r(l.Title), Artist: r(l.Artist), Album: r(l.Album), Spectrum: r(l.Spectrum), Loudness: r(l.Loudness), Progress: r(l.Progress), Time: r(l.Time)}
}

func musicSceneDynamics(features AudioFeatures, current, duration, ratio float64) MusicSceneDynamics {
	toQ := func(v float64) uint16 { v = sceneClamp01(v); return uint16(math.Round(v * 65535)) }
	// drawLoudness renders the within-track dB-normalized envelope, not the raw
	// RMS values. Transport that same canonical curve so the GPU uses identical
	// sample heights instead of a visually flatter raw-amplitude graph.
	normalized := normalizeRelativeLoudness(features.LoudnessEnvelope)
	env := make([]uint16, len(normalized))
	trend := SmoothLoudnessTrend(normalized, duration)
	for i, v := range normalized {
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
	// Guide values encode bottom-relative positions matching drawLoudness's
	// fixed offsets (6/80, 28/80, 50/80, 72/80).
	return MusicSceneDynamics{CurrentSeconds: current, DurationSeconds: duration, ProgressRatio: ratio, EdgeFadeAlpha: alphaIn, EndFadeAlpha: alphaOut, LoudnessEnvelope: env, LoudnessTrend: trendQ, LoudnessGuides: [4]uint16{toQ(1 - 6.0/80.0), toQ(1 - 28.0/80.0), toQ(1 - 50.0/80.0), toQ(1 - 72.0/80.0)}}
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
	type glyphKey struct {
		r      rune
		weight uint16
	}
	var all []glyphKey
	for _, f := range fields {
		for _, r := range f.text {
			all = append(all, glyphKey{r: r, weight: f.weight})
		}
	}
	if len(all) == 0 {
		return nil
	}
	// Rasterize the exact embedded Noto faces used by the CPU/libass path. A
	// glyph ID includes its weight because the same rune can occur in title,
	// artist and album runs with different faces.
	const gw, gh, cols = 64, 64, 16
	keys := make([]glyphKey, 0, min(len(all)+1, 256))
	seen := make(map[glyphKey]bool, min(len(all)+1, 256))
	for _, key := range all {
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
		// Reserve one cell for the weight-independent missing glyph.
		if len(keys) == 255 {
			break
		}
	}
	keys = append(keys, glyphKey{r: '?', weight: 0})
	w := cols * gw
	h := ((len(keys) + cols - 1) / cols) * gh
	stride := (w*4 + 255) &^ 255
	payload := make([]byte, stride*h)
	parsedFonts := parsedMusicAtlasFonts()
	faces := make(map[uint16]font.Face, 3)
	defer func() {
		for _, face := range faces {
			_ = face.Close()
		}
	}()
	faceForWeight := func(weight uint16) font.Face {
		if weight == 0 {
			weight = 400
		}
		if face := faces[weight]; face != nil {
			return face
		}
		parsed := parsedFonts[weight]
		if parsed == nil {
			parsed, _ = opentype.Parse(goregular.TTF)
		}
		// libass's 48pt title occupies roughly a 48px em at the canonical
		// 1280x720 canvas. Keep the raster face at 48px inside the bounded 64px
		// cell; the consumer scales the cell by run_size/64.
		face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 48, DPI: 72, Hinting: font.HintingNone})
		if err != nil {
			return nil
		}
		faces[weight] = face
		return face
	}
	glyphs := make([]GlyphEntry, 0, len(keys))
	advanceByID := make(map[string]float32, len(keys))
	for i, key := range keys {
		x := uint32((i % cols) * gw)
		y := uint32((i / cols) * gh)
		advance := float32(gw)
		rasterized := false
		fontFace := faceForWeight(key.weight)
		if fontFace != nil {
			_, _, ok := fontFace.GlyphBounds(key.r)
			if ok {
				dst := image.NewRGBA(image.Rect(0, 0, gw, gh))
				d := &font.Drawer{Dst: dst, Src: image.NewUniform(color.White), Face: fontFace,
					Dot: fixed.Point26_6{X: fixed.I(4), Y: fixed.I(52)}}
				d.DrawString(string(key.r))
				for yy := 0; yy < gh; yy++ {
					dstOff := yy * dst.Stride
					payloadOff := (int(y)+yy)*stride + int(x)*4
					copy(payload[payloadOff:payloadOff+gw*4], dst.Pix[dstOff:dstOff+gw*4])
				}
				adv, ok := fontFace.GlyphAdvance(key.r)
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
						off := (int(y)+yy)*stride + (int(x)+xx)*4
						payload[off], payload[off+1], payload[off+2], payload[off+3] = 255, 255, 255, 255
					}
				}
			}
		}
		id := musicGlyphID(key.weight, key.r)
		glyphs = append(glyphs, GlyphEntry{ID: id, X: x, Y: y, Width: gw, Height: gh, Advance: advance})
		advanceByID[id] = advance
	}
	sum := sha256.Sum256(payload)
	runs := make([]TextRun, 0, len(fields))
	for _, f := range fields {
		if f.text == "" {
			continue
		}
		x := float32(f.rect.X)
		if f.center {
			var textWidth float32
			for _, r := range f.text {
				textWidth += advanceByID[musicGlyphID(f.weight, r)] * f.size / musicAtlasLayoutEm
			}
			x += (float32(f.rect.W) - textWidth) / 2
		}
		// TextRun coordinates are top-left screen bounds; CPU ASS positions
		// title/artist/album at the vertical center of each rect.
		y := float32(f.rect.Y) + (float32(f.rect.H)-f.size)/2
		if f.center {
			// Align the atlas ink with libass's small-font baseline.
			y += 2
		}
		runs = append(runs, TextRun{Text: f.text, X: x, Y: y, SizePx: f.size, RGBA: primary, Opacity: 1, FontFamily: "Noto Sans JP", FontWeight: f.weight})
	}
	return &GlyphAtlasMetadata{TextureID: "glyphs-" + hex.EncodeToString(sum[:8]), FontFamily: "Noto Sans JP", FontWeight: 400, FallbackOrder: []string{"Noto Sans CJK JP", "Segoe UI", "sans-serif"}, Width: uint32(w), Height: uint32(h), RowStride: uint32(stride), GlyphCount: uint32(len(glyphs)), MissingGlyphID: "?", Payload: payload, AssetHash: hex.EncodeToString(sum[:]), Glyphs: glyphs, TextRuns: runs}
}

func musicGlyphID(weight uint16, r rune) string {
	if weight == 0 {
		return string(r)
	}
	return strconv.Itoa(int(weight)) + ":" + string(r)
}
