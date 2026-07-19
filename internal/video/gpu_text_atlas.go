package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalMusicASS uses assForegroundColor(..., 0.88), which libass exposes
// to FFmpeg as an 8-bit colour alpha of 224. The atlas itself is deliberately
// rendered opaque so its alpha channel remains the original libass coverage
// mask; the GPU applies the canonical colour alpha with ff_blend_mask's exact
// integer arithmetic.
const canonicalASSTextColorAlpha = uint8(224)

// PreparedGlyphAtlas contains immutable, bounded libass run rasters plus the
// lightweight event schedule used to select their GPU placement each frame.
// It never contains a full-screen raster.
type PreparedGlyphAtlas struct {
	Atlas      GlyphAtlasMetadata
	Events     []preparedGlyphEvent
	TimeEvents map[int][]preparedGlyphEvent
}

type preparedGlyphEvent struct {
	ID                       string
	AtlasRect                SceneRect
	InkOffsetX, InkOffsetY   int
	InkWidth, InkHeight      int
	Clip                     SceneRect
	StartSeconds, EndSeconds float64
	StartX, EndX, AnchorY    float64
	FadeInSeconds            float64
	FadeOutSeconds           float64
}

type assRunTile struct {
	id               string
	pix              *image.RGBA
	offsetX, offsetY int
	atlasRect        SceneRect
}

func prepareExactGlyphAtlas(ctx context.Context, ffmpeg string, metadata AudioMetadata, duration float64, layout VisualizerLayout, width, height int) (*PreparedGlyphAtlas, error) {
	if strings.TrimSpace(metadata.Title) == "" && strings.TrimSpace(metadata.Artist) == "" && strings.TrimSpace(metadata.Album) == "" && duration <= 0 {
		return nil, nil
	}
	fonts, err := VisualizerFonts()
	if err != nil {
		return nil, err
	}
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		return nil, err
	}
	fontDir := filepath.Dir(fonts.Regular400)
	tiles := map[string]*assRunTile{}
	order := make([]*assRunTile, 0, 16)
	addTile := func(id, text, family string, weight, size, alignment int) (*assRunTile, error) {
		key := fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%s", family, weight, size, alignment, text)
		if tile := tiles[key]; tile != nil {
			return tile, nil
		}
		pix, offsetX, offsetY, renderErr := renderBoundedASSRun(ctx, ffmpeg, fontDir, family, weight, size, alignment, text)
		if renderErr != nil {
			return nil, renderErr
		}
		tile := &assRunTile{id: id, pix: pix, offsetX: offsetX, offsetY: offsetY}
		tiles[key] = tile
		order = append(order, tile)
		return tile, nil
	}

	total := duration
	if total <= 0 {
		total = 1
	}
	prepared := &PreparedGlyphAtlas{}
	fullClip := SceneRect{W: width, H: height}
	addStaticOrScroll := func(id, text, family string, weight, size int, rect Rect) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		tile, tileErr := addTile(id, text, family, weight, size, 4)
		if tileErr != nil {
			return tileErr
		}
		clipPad := ASSClipPadding(width)
		clip := SceneRect{X: rect.X, Y: rect.Y - clipPad, W: rect.W, H: rect.H + 2*clipPad}
		textWidth, measureErr := MeasureASSEncodedWidth(ctx, ffmpeg, family, weight, fontDir, text, size)
		if measureErr != nil {
			return measureErr
		}
		anchorY := float64(rect.Y + rect.H/2)
		if textWidth <= rect.W {
			prepared.Events = append(prepared.Events, newPreparedGlyphEvent(tile, clip, 0, total, float64(rect.X), float64(rect.X), anchorY, 0, 0))
			return nil
		}
		overflow, hold, move, cycle := scrollCycle(float64(textWidth), float64(rect.W), float64(width))
		endX := float64(rect.X) - overflow - 40.0*float64(width)/1280.0*scrollExtraMoveSeconds
		for cycleStart := 0.0; cycleStart < total; cycleStart += cycle {
			pauseEnd := math.Min(total, cycleStart+hold)
			prepared.Events = append(prepared.Events, newPreparedGlyphEvent(tile, clip, cycleStart, pauseEnd, float64(rect.X), float64(rect.X), anchorY, 0.3, 0))
			if pauseEnd >= total {
				break
			}
			scrollEnd := math.Min(total, pauseEnd+move)
			prepared.Events = append(prepared.Events, newPreparedGlyphEvent(tile, clip, pauseEnd, scrollEnd, float64(rect.X), endX, anchorY, 0, 0.3))
		}
		return nil
	}

	if err = addStaticOrScroll("title", metadata.Title, faces.SemiBold600.ASSFamily, 600, scaledFontSize(48, width), layout.Title); err != nil {
		return nil, fmt.Errorf("prepare GPU title glyphs: %w", err)
	}
	if err = addStaticOrScroll("artist", metadata.Artist, faces.Medium500.ASSFamily, 500, scaledFontSize(28, width), layout.Artist); err != nil {
		return nil, fmt.Errorf("prepare GPU artist glyphs: %w", err)
	}
	if err = addStaticOrScroll("album", metadata.Album, faces.Regular400.ASSFamily, 400, scaledFontSize(24, width), layout.Album); err != nil {
		return nil, fmt.Errorf("prepare GPU album glyphs: %w", err)
	}
	timeX := float64(layout.Time.X + layout.Time.W/2)
	timeY := float64(layout.Time.Y + layout.Time.H/2)
	prepared.TimeEvents = make(map[int][]preparedGlyphEvent)
	totalSeconds := max(1, int(math.Ceil(total)))
	for second := 0; second < totalSeconds; second++ {
		label := FormatMediaTime(second) + " / " + FormatMediaTime(int(math.Floor(duration)))
		tile, tileErr := addTile(fmt.Sprintf("time-%d", second), label, faces.Medium500.ASSFamily, 500, scaledFontSize(22, width), 5)
		if tileErr != nil {
			return nil, fmt.Errorf("prepare GPU time glyphs: %w", tileErr)
		}
		prepared.TimeEvents[second] = append(prepared.TimeEvents[second], newPreparedGlyphEvent(tile, fullClip, float64(second), math.Min(total, float64(second+1)), timeX, timeX, timeY, 0, 0))
	}
	if len(order) == 0 {
		return nil, nil
	}
	if err := packASSRunTiles(order, &prepared.Atlas); err != nil {
		return nil, err
	}
	for i := range prepared.Events {
		for _, tile := range order {
			if tile.id == prepared.Events[i].ID {
				prepared.Events[i].AtlasRect = tile.atlasRect
				break
			}
		}
	}
	for second, events := range prepared.TimeEvents {
		for i := range events {
			for _, tile := range order {
				if tile.id == events[i].ID {
					events[i].AtlasRect = tile.atlasRect
					break
				}
			}
		}
		prepared.TimeEvents[second] = events
	}
	return prepared, nil
}

func newPreparedGlyphEvent(tile *assRunTile, clip SceneRect, start, end, startX, endX, anchorY, fadeIn, fadeOut float64) preparedGlyphEvent {
	return preparedGlyphEvent{ID: tile.id, InkOffsetX: tile.offsetX, InkOffsetY: tile.offsetY, InkWidth: tile.pix.Bounds().Dx(), InkHeight: tile.pix.Bounds().Dy(), Clip: clip, StartSeconds: start, EndSeconds: end, StartX: startX, EndX: endX, AnchorY: anchorY, FadeInSeconds: fadeIn, FadeOutSeconds: fadeOut}
}

func (p *PreparedGlyphAtlas) atlasAt(current float64, primary [4]uint8) *GlyphAtlasMetadata {
	if p == nil || len(p.Atlas.Payload) == 0 {
		return nil
	}
	atlas := p.Atlas
	atlas.BitmapRuns = nil
	appendEvent := func(event preparedGlyphEvent) {
		if current < event.StartSeconds || current >= event.EndSeconds || event.EndSeconds <= event.StartSeconds {
			return
		}
		progress := (current - event.StartSeconds) / (event.EndSeconds - event.StartSeconds)
		anchorX := event.StartX + (event.EndX-event.StartX)*progress
		opacity := 1.0
		if event.FadeInSeconds > 0 && current < event.StartSeconds+event.FadeInSeconds {
			opacity = (current - event.StartSeconds) / event.FadeInSeconds
		}
		if event.FadeOutSeconds > 0 && current > event.EndSeconds-event.FadeOutSeconds {
			opacity = math.Min(opacity, (event.EndSeconds-current)/event.FadeOutSeconds)
		}
		screen := SceneRect{X: int(math.Round(anchorX)) + event.InkOffsetX, Y: int(math.Round(event.AnchorY)) + event.InkOffsetY, W: event.InkWidth, H: event.InkHeight}
		atlasRect, clippedScreen, ok := clipGlyphBitmap(event.AtlasRect, screen, event.Clip)
		if !ok {
			return
		}
		rgba := primary
		rgba[3] = canonicalASSTextColorAlpha
		atlas.BitmapRuns = append(atlas.BitmapRuns, GlyphBitmapRun{ID: event.ID, AtlasRect: atlasRect, ScreenRect: clippedScreen, RGBA: rgba, Opacity: float32(math.Max(0, math.Min(1, opacity)))})
	}
	for _, event := range p.Events {
		appendEvent(event)
	}
	for _, event := range p.TimeEvents[int(math.Floor(current))] {
		appendEvent(event)
	}
	return &atlas
}

func clipGlyphBitmap(atlas, screen, clip SceneRect) (SceneRect, SceneRect, bool) {
	x0, y0 := max(screen.X, clip.X), max(screen.Y, clip.Y)
	x1, y1 := min(screen.X+screen.W, clip.X+clip.W), min(screen.Y+screen.H, clip.Y+clip.H)
	if x1 <= x0 || y1 <= y0 {
		return SceneRect{}, SceneRect{}, false
	}
	dx, dy := x0-screen.X, y0-screen.Y
	return SceneRect{X: atlas.X + dx, Y: atlas.Y + dy, W: x1 - x0, H: y1 - y0}, SceneRect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, true
}

func renderBoundedASSRun(ctx context.Context, ffmpeg, fontDir, family string, weight, size, alignment int, text string) (*image.RGBA, int, int, error) {
	measured, err := MeasureASSEncodedWidth(ctx, ffmpeg, family, weight, fontDir, text, size)
	if err != nil {
		return nil, 0, 0, err
	}
	canvasW := min(4096, max(128, measured+size*4))
	canvasH := min(512, max(96, size*3))
	anchorX := size * 2
	if alignment == 5 {
		anchorX = canvasW / 2
	}
	anchorY := canvasH / 2
	var ass strings.Builder
	ass.WriteString("[Script Info]\nScriptType: v4.00+\n")
	ass.WriteString(fmt.Sprintf("PlayResX: %d\nPlayResY: %d\n", canvasW, canvasH))
	ass.WriteString("ScaledBorderAndShadow: yes\nWrapStyle: 2\n\n[V4+ Styles]\n")
	ass.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	// Opaque white preserves the raw libass coverage mask. Applying the 0.88
	// canonical text opacity here would quantize 256 coverage levels down to
	// 225, making FFmpeg's direct YUV blend impossible to reproduce exactly.
	writeStyle(&ass, "Run", family, size, weight, alignment, "&H00FFFFFF")
	ass.WriteString("\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	writeDialogue(&ass, "0:00:00.00", "0:00:01.00", "Run", fmt.Sprintf("\\q2\\pos(%d,%d)", anchorX, anchorY), escapeASSText(text))
	tmp, err := os.MkdirTemp("", "imagepad-gpu-run-")
	if err != nil {
		return nil, 0, 0, err
	}
	defer os.RemoveAll(tmp)
	assPath := filepath.Join(tmp, "run.ass")
	if err := os.WriteFile(assPath, []byte(ass.String()), 0644); err != nil {
		return nil, 0, 0, err
	}
	payload, stride, err := RenderASSOverlayRGBA(ctx, ffmpeg, assPath, fontDir, uint32(canvasW), uint32(canvasH))
	if err != nil {
		return nil, 0, 0, err
	}
	minX, minY, maxX, maxY := canvasW, canvasH, -1, -1
	for y := 0; y < canvasH; y++ {
		for x := 0; x < canvasW; x++ {
			if payload[y*int(stride)+x*4+3] != 0 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < minX || maxY < minY {
		return nil, 0, 0, fmt.Errorf("libass produced no pixels for %q", text)
	}
	out := image.NewRGBA(image.Rect(0, 0, maxX-minX+1, maxY-minY+1))
	for y := 0; y < out.Bounds().Dy(); y++ {
		copy(out.Pix[y*out.Stride:y*out.Stride+out.Bounds().Dx()*4], payload[(minY+y)*int(stride)+minX*4:(minY+y)*int(stride)+(maxX+1)*4])
	}
	return out, minX - anchorX, minY - anchorY, nil
}

func packASSRunTiles(tiles []*assRunTile, atlas *GlyphAtlasMetadata) error {
	const maxWidth = 4096
	x, y, rowH, usedW := 0, 0, 0, 0
	for _, tile := range tiles {
		w, h := tile.pix.Bounds().Dx(), tile.pix.Bounds().Dy()
		if w > maxWidth || h > int(MusicMaxArtworkDimension) {
			return fmt.Errorf("bounded glyph run %q exceeds atlas limits", tile.id)
		}
		if x > 0 && x+w > maxWidth {
			x, y, rowH = 0, y+rowH+1, 0
		}
		if y+h > int(MusicMaxArtworkDimension) {
			return fmt.Errorf("bounded glyph atlas exceeds %d pixels", MusicMaxArtworkDimension)
		}
		tile.atlasRect = SceneRect{X: x, Y: y, W: w, H: h}
		x += w + 1
		usedW = max(usedW, x-1)
		rowH = max(rowH, h)
	}
	usedH := y + rowH
	stride := (usedW*4 + int(GPURowAlignment) - 1) &^ (int(GPURowAlignment) - 1)
	if uint64(stride)*uint64(usedH) > MusicMaxArtworkBytes {
		return fmt.Errorf("bounded glyph atlas exceeds %d bytes", MusicMaxArtworkBytes)
	}
	payload := make([]byte, stride*usedH)
	for _, tile := range tiles {
		for yy := 0; yy < tile.atlasRect.H; yy++ {
			copy(payload[(tile.atlasRect.Y+yy)*stride+tile.atlasRect.X*4:], tile.pix.Pix[yy*tile.pix.Stride:yy*tile.pix.Stride+tile.atlasRect.W*4])
		}
	}
	sum := sha256.Sum256(payload)
	atlas.TextureID = "libass-runs-" + hex.EncodeToString(sum[:8])
	atlas.FontFamily = "Noto Sans JP"
	atlas.FontWeight = 400
	atlas.Width = uint32(usedW)
	atlas.Height = uint32(usedH)
	atlas.RowStride = uint32(stride)
	atlas.GlyphCount = uint32(len(tiles))
	atlas.MissingGlyphID = "libass-missing"
	atlas.Payload = payload
	atlas.AssetHash = hex.EncodeToString(sum[:])
	return nil
}
