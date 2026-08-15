package video

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

// CanonicalMusicSceneBuilder keeps immutable scene assets resident for the
// lifetime of one render job. The old per-frame normalization path decoded the
// artwork file, parsed/rasterized the font, recomputed the palette, rebuilt the
// loudness envelope, and hashed a large JSON scene for every frame.
type CanonicalMusicSceneBuilder struct {
	input         AudioRenderInput
	artwork       *ArtworkMetadata
	glyphTemplate *GlyphAtlasMetadata
	textOverlay   *TextOverlayMetadata
	palette       MusicScenePalette
	layout        MusicSceneLayout
	baseDynamics  MusicSceneDynamics
	fps           int
	duration      float64
}

// NewCanonicalMusicSceneBuilder prepares the immutable part of a canonical
// scene once. The returned builder is not safe for concurrent use; a render
// job owns one builder and calls Scene in frame order.
func NewCanonicalMusicSceneBuilder(input AudioRenderInput) *CanonicalMusicSceneBuilder {
	duration := input.Analysis.Duration
	if duration < 0 || math.IsNaN(duration) {
		duration = 0
	}
	fps := input.Analysis.FPS
	if fps <= 0 {
		fps = 30
	}
	layout, _ := LayoutForSize(1280, 720)
	var artwork ArtworkMetadata
	if normalized, ok := normalizeArtwork(input.ArtworkPath); ok {
		artwork = normalized
	} else {
		artwork = fallbackArtwork(input.Analysis.Features)
	}
	palette := canonicalScenePaletteForArtwork(input, &artwork)
	glyphTemplate := normalizeGlyphs(input.Metadata, layout, palette.Primary, 0, duration)
	textOverlay := input.TextOverlay
	if textOverlay == nil && glyphTemplate != nil {
		textOverlay = canonicalTextOverlayFromAtlas(input.Metadata, glyphTemplate)
	}
	return &CanonicalMusicSceneBuilder{
		input:         input,
		artwork:       &artwork,
		glyphTemplate: glyphTemplate,
		textOverlay:   textOverlay,
		palette:       palette,
		layout:        musicSceneLayout(layout),
		baseDynamics:  musicSceneDynamics(input.Analysis.Features, 0, duration, 0),
		fps:           fps,
		duration:      duration,
	}
}

// Scene constructs only the frame-varying portion of the canonical scene.
// Immutable raster assets are shared by pointer and are never mutated.
func (b *CanonicalMusicSceneBuilder) Scene(frameIndex uint64, ptsNS int64) MusicScenePayload {
	var spectrum []uint16
	var rms, peak float64
	if len(b.input.Analysis.Frames) > 0 {
		idx := int(frameIndex)
		if idx >= len(b.input.Analysis.Frames) {
			idx = len(b.input.Analysis.Frames) - 1
		}
		f := b.input.Analysis.Frames[idx]
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
	current := float64(frameIndex) / float64(b.fps)
	ratio := 0.0
	if b.duration > 0 {
		ratio = sceneClamp01(current / b.duration)
	}
	fingerprint := make([]uint16, len(b.input.Analysis.Features.Fingerprint64))
	for i, v := range b.input.Analysis.Features.Fingerprint64 {
		fingerprint[i] = uint16(math.Round(sceneClamp01(v) * 65535))
	}
	dynamics := b.baseDynamics
	dynamics.CurrentSeconds = current
	dynamics.DurationSeconds = b.duration
	dynamics.ProgressRatio = ratio
	dynamics.EdgeFadeAlpha, dynamics.EndFadeAlpha = musicSceneFadeAlpha(current, b.duration)
	scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, FrameIndex: frameIndex, PTSNs: ptsNS, SpectrumQ16: spectrum, FingerprintQ16: fingerprint, RMSQ15: uint16(math.Round(sceneClamp01(rms) * 32767)), PeakQ15: uint16(math.Round(sceneClamp01(peak) * 32767))}, PCMF32LE: canonicalScenePCMWindow(b.input.Analysis.PCMInterleavedS16, frameIndex, b.fps), Layout: b.layout, Dynamics: dynamics, Palette: b.palette, Artwork: b.artwork, TextOverlay: b.textOverlay}
	if b.glyphTemplate != nil {
		glyph := *b.glyphTemplate
		glyph.TextRuns = canonicalGlyphTextRuns(b.input.Metadata, layoutFromScene(b.layout), b.palette.Primary, current, b.duration)
		scene.GlyphAtlas = &glyph
	}
	if len(b.input.Analysis.WaveformFrames) > 0 {
		waveformIndex := int(frameIndex)
		if waveformIndex >= len(b.input.Analysis.WaveformFrames) {
			waveformIndex = len(b.input.Analysis.WaveformFrames) - 1
		}
		if waveformIndex >= 0 && len(b.input.Analysis.WaveformFrames[waveformIndex]) > 0 {
			scene.Feature.WaveformQ16 = normalizeSceneWaveformQ16(b.input.Analysis.WaveformFrames[waveformIndex])
		}
	}
	if b.input.WaveformTexture != nil {
		scene.WaveformTexture = b.input.WaveformTexture
	}
	scene.Fingerprint = musicSceneFingerprint(scene)
	return scene
}

// CanonicalMusicScene is the one-shot compatibility wrapper. Hot render loops
// should create one CanonicalMusicSceneBuilder and reuse it.
func CanonicalMusicScene(input AudioRenderInput, frameIndex uint64, ptsNS int64) MusicScenePayload {
	return NewCanonicalMusicSceneBuilder(input).Scene(frameIndex, ptsNS)
}

func layoutFromScene(layout MusicSceneLayout) VisualizerLayout {
	toRect := func(r SceneRect) Rect { return Rect{X: r.X, Y: r.Y, W: r.W, H: r.H} }
	return VisualizerLayout{Artwork: toRect(layout.Artwork), Title: toRect(layout.Title), Artist: toRect(layout.Artist), Album: toRect(layout.Album), Spectrum: toRect(layout.Spectrum), Loudness: toRect(layout.Loudness), Progress: toRect(layout.Progress), Time: toRect(layout.Time)}
}

func musicSceneFadeAlpha(current, duration float64) (float32, float32) {
	alphaIn, alphaOut := float32(1), float32(1)
	if current < radioEdgeFadeSeconds {
		alphaIn = float32(sceneClamp01(current / radioEdgeFadeSeconds))
	}
	if duration > 0 && duration-current < radioEdgeFadeSeconds {
		alphaOut = float32(sceneClamp01((duration - current) / radioEdgeFadeSeconds))
	}
	return alphaIn, alphaOut
}

func canonicalScenePCMWindow(pcm []int16, frameIndex uint64, fps int) []byte {
	if len(pcm) < 2 {
		return nil
	}
	if fps <= 0 {
		fps = 30
	}
	startFrame := int(math.Round(float64(frameIndex) * float64(sampleRate) / float64(fps)))
	startSample := startFrame * 2
	out := make([]byte, MusicMaxPCMBytes)
	for i := 0; i < MusicPCMWindowSamples; i++ {
		position := startSample + i*2
		var sample float32
		if position+1 < len(pcm) {
			mono := (int32(pcm[position]) + int32(pcm[position+1])) / 2
			sample = float32(mono) / 32768.0
		}
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(sample))
	}
	return out
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
	var artwork *ArtworkMetadata
	if normalized, ok := normalizeArtwork(input.ArtworkPath); ok {
		artwork = &normalized
	}
	return canonicalScenePaletteForArtwork(input, artwork)
}

func canonicalScenePaletteForArtwork(input AudioRenderInput, artwork *ArtworkMetadata) MusicScenePalette {
	p := PaletteForFeatures(input.Analysis.Features)
	primary := [4]uint8{255, 255, 255, 255}
	accent := [4]uint8{p.End.R, p.End.G, p.End.B, 255}
	background := [4]uint8{p.Start.R, p.Start.G, p.Start.B, 255}
	if artwork != nil && len(artwork.Payload) >= 4 {
		var r, g, b, n uint64
		for i := 0; i+3 < len(artwork.Payload); i += 4 {
			if artwork.Payload[i+3] < 16 {
				continue
			}
			r += uint64(artwork.Payload[i])
			g += uint64(artwork.Payload[i+1])
			b += uint64(artwork.Payload[i+2])
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
	// Guide values encode bottom-relative positions matching drawLoudness's
	// fixed offsets (6/80, 28/80, 50/80, 72/80).
	return MusicSceneDynamics{CurrentSeconds: current, DurationSeconds: duration, ProgressRatio: ratio, EdgeFadeAlpha: alphaIn, EndFadeAlpha: alphaOut, LoudnessEnvelope: env, LoudnessTrend: trendQ, LoudnessGuides: [4]uint16{toQ(1 - 6.0/80.0), toQ(1 - 28.0/80.0), toQ(1 - 50.0/80.0), toQ(1 - 72.0/80.0)}}
}

func musicSceneFingerprint(scene MusicScenePayload) string {
	// Hash the canonical values and content hashes without serializing the
	// immutable artwork/glyph/text rasters into a new JSON document for every
	// frame. AssetHash already commits those byte payloads; PCM is the only
	// large frame-local byte payload and is hashed directly.
	h := sha256.New()
	writeU64 := func(value uint64) {
		var bytes [8]byte
		binary.LittleEndian.PutUint64(bytes[:], value)
		_, _ = h.Write(bytes[:])
	}
	writeString := func(value string) {
		writeU64(uint64(len(value)))
		_, _ = h.Write([]byte(value))
	}
	writeU64(uint64(scene.Schema))
	writeU64(scene.Feature.FrameIndex)
	writeU64(uint64(scene.Feature.PTSNs))
	writeU64(uint64(scene.Feature.RMSQ15))
	writeU64(uint64(scene.Feature.PeakQ15))
	for _, value := range scene.Feature.SpectrumQ16 {
		writeU64(uint64(value))
	}
	for _, value := range scene.Feature.WaveformQ16 {
		writeU64(uint64(value))
	}
	for _, value := range scene.Feature.FingerprintQ16 {
		writeU64(uint64(value))
	}
	for _, value := range scene.Dynamics.LoudnessEnvelope {
		writeU64(uint64(value))
	}
	for _, value := range scene.Dynamics.LoudnessTrend {
		writeU64(uint64(value))
	}
	writeString(scene.ArtworkHash())
	writeString(scene.GlyphHash())
	writeString(scene.TextOverlayHash())
	_, _ = h.Write(scene.PCMF32LE)
	return hex.EncodeToString(h.Sum(nil))
}

func (s MusicScenePayload) ArtworkHash() string {
	if s.Artwork == nil {
		return ""
	}
	return s.Artwork.AssetHash
}

func (s MusicScenePayload) GlyphHash() string {
	if s.GlyphAtlas == nil {
		return ""
	}
	return s.GlyphAtlas.AssetHash
}

func (s MusicScenePayload) TextOverlayHash() string {
	if s.TextOverlay == nil {
		return ""
	}
	return s.TextOverlay.AssetHash
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
	// Keep the atlas asset hash stable while the clock text changes. The
	// previous implementation added only the digits visible in the current
	// timestamp, which could trigger a needless GPU atlas rebuild mid-track.
	all = append(all, []rune("0123456789")...)
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
	runs := canonicalGlyphTextRuns(meta, layout, primary, current, duration)
	return &GlyphAtlasMetadata{TextureID: "glyphs-" + hex.EncodeToString(sum[:8]), FontFamily: "Go Regular", FontWeight: 400, FallbackOrder: []string{"Noto Sans CJK JP", "Segoe UI", "sans-serif"}, Width: uint32(w), Height: uint32(h), RowStride: uint32(stride), GlyphCount: uint32(len(glyphs)), MissingGlyphID: "?", Payload: payload, AssetHash: hex.EncodeToString(sum[:]), Glyphs: glyphs, TextRuns: runs}
}

func canonicalGlyphTextRuns(meta AudioMetadata, layout VisualizerLayout, primary [4]uint8, current, duration float64) []TextRun {
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
	runs := make([]TextRun, 0, len(fields))
	for _, f := range fields {
		if f.text == "" {
			continue
		}
		x := float32(f.rect.X)
		if f.center {
			approxWidth := float32(len([]rune(f.text))) * f.size * 0.6
			x += (float32(f.rect.W) - approxWidth) / 2
		}
		y := float32(f.rect.Y) + (float32(f.rect.H)-f.size)/2
		runs = append(runs, TextRun{Text: f.text, X: x, Y: y, SizePx: f.size, RGBA: primary, Opacity: 1, FontFamily: "Noto Sans JP", FontWeight: f.weight})
	}
	return runs
}

func canonicalTextOverlayFromAtlas(meta AudioMetadata, atlas *GlyphAtlasMetadata) *TextOverlayMetadata {
	if atlas == nil {
		return nil
	}
	sum := sha256.Sum256(atlas.Payload)
	return &TextOverlayMetadata{Kind: "atlas", Title: strings.TrimSpace(meta.Title), Artist: strings.TrimSpace(meta.Artist), Album: strings.TrimSpace(meta.Album), FontFamily: "Go Regular", FontWeight: 400, SizePx: 48, RGBA: [4]uint8{255, 255, 255, 255}, Opacity: 1, Width: atlas.Width, Height: atlas.Height, RowStride: atlas.RowStride, Format: PixelRGBA8, ColorSpace: ColorSRGB, Premultiplied: true, Payload: atlas.Payload, AssetHash: hex.EncodeToString(sum[:]), RendererID: "imagepad-canonical-overlay", RendererVersion: "1"}
}
