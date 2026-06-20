package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func AudioVisualizerFFmpegArgs(audioPath, assPath, fontDir, id string, preset QualityPreset) []string {
	return audioVisualizerFFmpegArgs(audioPath, assPath, fontDir, id, preset, nil)
}

func audioVisualizerFFmpegArgs(audioPath, assPath, fontDir, id string, preset QualityPreset, mode *ForegroundMode) []string {
	height := preset.Height
	if height <= 0 {
		height = 720
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}
	waveW := int(math.Round(752 * float64(width) / 1280))
	waveH := int(math.Round(168 * float64(height) / 720))
	waveX := int(math.Round(432 * float64(width) / 1280))
	waveY := int(math.Round(320 * float64(height) / 720))
	waveColor := "#FFFFFF@0.55"
	if mode != nil {
		waveColor = fmt.Sprintf("#%02X%02X%02X@0.55", mode.Color.R, mode.Color.G, mode.Color.B)
	}
	return []string{
		"-v", "error",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", "30",
		"-i", "pipe:0",
		"-i", audioPath,
		"-filter_complex",
		fmt.Sprintf(
			"[1:a]showwaves=s=%dx%d:rate=30:mode=line:colors=%s[wave];[0:v]format=yuv420p[vis];[vis][wave]overlay=%d:%d[vid];[vid]ass=filename='%s':fontsdir='%s'[out]",
			waveW, waveH, waveColor, waveX, waveY,
			escapeFilterPath(assPath),
			escapeFilterPath(fontDir),
		),
		"-map", "[out]",
		"-map", "1:a",
		"-c:v", "libx264",
		"-preset", "medium",
		"-crf", fmt.Sprintf("%d", preset.CRF),
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ar", "48000",
		"-ac", "2",
		"-pix_fmt", "yuv420p",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_segment_filename", "%s/" + segmentPattern(id),
		"-hls_flags", "independent_segments",
		"%s/" + playlistName(id),
	}
}

func escapeFilterPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.ReplaceAll(p, ":", "\\:")
	return p
}

func formatVisualizerOutputArgs(args []string, outDir string) []string {
	formatted := append([]string(nil), args...)
	prefix := filepath.ToSlash(outDir) + "/"
	for i, arg := range formatted {
		if strings.Contains(arg, "%s/") {
			formatted[i] = strings.Replace(arg, "%s/", prefix, 1)
			formatted[i] = strings.ReplaceAll(formatted[i], "%%", "%")
		}
	}
	return formatted
}

// WriteVisualizerRGBAFrames renders each analysis frame as a raw RGBA image
// and writes it to dst.  Each frame starts from the pre-rendered base image
// (blurred background + artwork tile + overlay) and adds dynamic elements:
// spectrum bars, loudness envelope, and progress indicator.
func WriteVisualizerRGBAFrames(ctx context.Context, dst io.Writer, input AudioRenderInput, base *image.RGBA, mode ForegroundMode, layout VisualizerLayout, width, height int) error {
	if len(input.Analysis.Frames) == 0 {
		return fmt.Errorf("no analysis frames to render")
	}
	if base == nil {
		return fmt.Errorf("base image is nil")
	}

	frameW, frameH := width, height
	canvas := image.NewRGBA(image.Rect(0, 0, frameW, frameH))
	totalFrames := len(input.Analysis.Frames)
	duration := input.Analysis.Duration

	envelope := input.Analysis.Features.LoudnessEnvelope

	// Cache the loudness layer (guide lines + detail curve + trend curve)
	// once per render job instead of recomputing for every frame.
	trend := SmoothLoudnessTrend(envelope, duration)
	loudnessLayer := renderLoudnessLayer(envelope, trend, mode, layout, width, height)

	for fi, frame := range input.Analysis.Frames {
		// Copy base image (background + artwork).
		draw.Draw(canvas, canvas.Bounds(), base, image.Point{}, draw.Src)

		// Spectrum bars.
		drawSpectrum(canvas, frame.Spectrum24, mode, layout)

		// Whole-track loudness envelope (cached — same for every frame).
		draw.Draw(canvas, canvas.Bounds(), loudnessLayer, image.Point{}, draw.Over)

		// Decorative progress bar + position marker.
		currentSeconds := float64(fi) / float64(totalFrames) * duration
		drawProgress(canvas, mode, layout, currentSeconds, duration)

		if err := binary.Write(dst, binary.LittleEndian, canvas.Pix); err != nil {
			return fmt.Errorf("frame %d: %w", fi, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Spectrum bars (spec section 10)
// ---------------------------------------------------------------------------

// drawSpectrum draws 24 fixed-logarithmic frequency bars with a vertical alpha
// gradient: alpha reaches zero at the bottom edge.
func drawSpectrum(canvas *image.RGBA, spectrum [24]float64, mode ForegroundMode, layout VisualizerLayout) {
	s := float64(canvas.Bounds().Dx()) / 1280.0

	barW := int(math.Round(18 * s))
	barGap := int(math.Round(13 * s))
	firstBarX := layout.Spectrum.X + int(math.Round(11*s))
	barBottom := layout.Spectrum.Y + layout.Spectrum.H
	maxBarH := layout.Spectrum.H - int(math.Round(16*s))
	minBarH := int(math.Round(4 * s))
	if minBarH < 1 {
		minBarH = 1
	}

	maxAlpha := uint8(math.Round(0.82 * 255.0))
	barColor := mode.Color
	barColor.A = maxAlpha

	for b := 0; b < 24 && b < len(spectrum); b++ {
		val := spectrum[b]
		if val < 0 {
			val = 0
		}
		if val > 1 {
			val = 1
		}

		barH := minBarH + int(val*float64(maxBarH-minBarH))
		x := firstBarX + b*(barW+barGap)
		y := barBottom - barH

		fadePx := int(math.Round(float64(barH) * 0.2))
		if fadePx < 1 {
			fadePx = 1
		}

		for dx := 0; dx < barW; dx++ {
			for dy := 0; dy < barH; dy++ {
				cx, cy := x+dx, y+dy
				if cx < 0 || cx >= canvas.Bounds().Dx() || cy < 0 || cy >= canvas.Bounds().Dy() {
					continue
				}

				// Vertical alpha gradient: alpha=0 at bottom edge, max
				// opacity reached 20 % of bar height above bottom.
				bottomDist := barH - 1 - dy // 0 at bottom, barH-1 at top
				var alpha uint8
				if bottomDist >= fadePx {
					alpha = maxAlpha
				} else {
					t := float64(bottomDist) / float64(fadePx)
					alpha = uint8(math.Round(float64(maxAlpha) * t))
				}

				c := barColor
				c.A = alpha
				blendPixel(canvas, cx, cy, c)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Whole-track loudness envelope (spec section 12)
// ---------------------------------------------------------------------------

// drawLoudness draws four guide lines and the 1000-sample loudness curve.
func drawLoudness(canvas *image.RGBA, envelope [1000]float64, mode ForegroundMode, layout VisualizerLayout) {
	// Guide lines (four evenly-spaced horizontal lines).
	guideColor := mode.Color
	guideColor.A = uint8(math.Round(0.22 * 255.0))

	guideOffsets := []float64{6.0 / 80.0, 28.0 / 80.0, 50.0 / 80.0, 72.0 / 80.0}
	for _, off := range guideOffsets {
		gy := layout.Loudness.Y + int(math.Round(float64(layout.Loudness.H)*off))
		for x := layout.Loudness.X; x < layout.Loudness.X+layout.Loudness.W; x++ {
			if x >= 0 && x < canvas.Bounds().Dx() && gy >= 0 && gy < canvas.Bounds().Dy() {
				blendPixel(canvas, x, gy, guideColor)
			}
		}
	}

	// Loudness curve.
	lineColor := mode.Color
	lineColor.A = uint8(math.Round(0.80 * 255.0))

	graphBottom := layout.Loudness.Y + layout.Loudness.H
	graphH := float64(layout.Loudness.H)

	lineWidth := max(1, int(math.Round(2*float64(canvas.Bounds().Dx())/1280)))
	for i := 0; i < 1000 && i < len(envelope); i++ {
		val := envelope[i]
		if val < 0 {
			val = 0
		}
		if val > 1 {
			val = 1
		}

		y := graphBottom - int(math.Round(val*graphH))
		if y < layout.Loudness.Y {
			y = layout.Loudness.Y
		}
		if y >= graphBottom {
			y = graphBottom - 1
		}

		x := layout.Loudness.X + int(math.Round(float64(i)*float64(layout.Loudness.W-1)/999.0))
		for dx := 0; dx < lineWidth && x+dx < layout.Loudness.X+layout.Loudness.W; dx++ {
			blendPixel(canvas, x+dx, y, lineColor)
		}
	}
}

// ---------------------------------------------------------------------------
// Decorative playback-position display (spec section 13)
// ---------------------------------------------------------------------------

// drawProgress draws the progress track rectangle and circular position marker.
func drawProgress(canvas *image.RGBA, mode ForegroundMode, layout VisualizerLayout, currentSeconds, duration float64) {
	trackColor := mode.Color
	trackColor.A = uint8(math.Round(0.35 * 255.0))

	markerColor := mode.Color
	markerColor.A = uint8(math.Round(0.88 * 255.0))

	// Track as a rounded pill rectangle.
	cr := int(math.Round(float64(layout.Progress.H) / 2.0))
	if cr < 1 {
		cr = 1
	}
	trackRect := image.Rect(
		layout.Progress.X, layout.Progress.Y,
		layout.Progress.X+layout.Progress.W, layout.Progress.Y+layout.Progress.H,
	)
	drawFilledRoundedRect(canvas, trackRect, cr, trackColor)

	// Circular position marker.
	markerX := layout.Progress.X
	if duration > 0 {
		markerX = layout.Progress.X + int(math.Round(float64(layout.Progress.W)*currentSeconds/duration))
	}
	// Clamp to track bounds.
	if markerX < layout.Progress.X {
		markerX = layout.Progress.X
	}
	if markerX > layout.Progress.X+layout.Progress.W {
		markerX = layout.Progress.X + layout.Progress.W
	}

	markerCenterY := layout.Progress.Y + layout.Progress.H/2
	s := float64(layout.Progress.W) / 1000.0
	markerRadius := int(math.Round(9 * s))
	if markerRadius < 1 {
		markerRadius = 1
	}

	drawCircle(canvas, markerX, markerCenterY, markerRadius, markerColor)
}

// drawCircle draws a filled circle centred at (cx, cy) with the given radius.
func drawCircle(canvas *image.RGBA, cx, cy, radius int, c color.RGBA) {
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx*dx+dy*dy <= radius*radius {
				x, y := cx+dx, cy+dy
				if x >= 0 && x < canvas.Bounds().Dx() && y >= 0 && y < canvas.Bounds().Dy() {
					blendPixel(canvas, x, y, c)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// RunAudioVisualizerHLS
// ---------------------------------------------------------------------------

func RunAudioVisualizerHLS(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, id string, preset QualityPreset) error {
	height := preset.Height
	if height <= 0 {
		height = 720
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}

	// Compute layout.
	layout, err := LayoutForSize(width, height)
	if err != nil {
		return fmt.Errorf("layout: %w", err)
	}

	// Resolve font paths.
	fonts, err := VisualizerFonts()
	if err != nil {
		return fmt.Errorf("fonts: %w", err)
	}

	// Prepare base image (blurred background + artwork tile + overlay).
	basePath := filepath.Join(outDir, id+"-base.png")

	var fallback *image.RGBA
	if input.ArtworkPath == "" {
		fallback, err = RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, color.RGBA{255, 255, 255, 224}, layout.Artwork.W)
		if err != nil {
			return fmt.Errorf("fallback artwork: %w", err)
		}
	}

	mode, err := PrepareVisualizerBase(ctx, ffmpeg, input.ArtworkPath, fallback, layout, basePath)
	if err != nil {
		return fmt.Errorf("prepare base: %w", err)
	}

	// Load base image for frame composition.
	baseImg, err := loadPNG(basePath)
	if err != nil {
		return fmt.Errorf("load base: %w", err)
	}
	baseRGBA := toRGBA(baseImg)

	// Build ASS subtitle file.
	assPath := filepath.Join(outDir, id+".ass")

	// Resolve font faces for PostScript names used by ASS measurement (AV-824).
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		return fmt.Errorf("resolve font faces: %w", err)
	}
	fontDir := filepath.Dir(fonts.Regular400)

	metrics := map[string]TextMetrics{}
	titleSize := scaledFontSize(48, width)
	artistSize := scaledFontSize(28, width)
	albumSize := scaledFontSize(24, width)
	titleW, err := MeasureASSEncodedWidth(ctx, ffmpeg, faces.SemiBold600.ASSFamily, 600, fontDir, input.Metadata.Title, titleSize)
	if err != nil {
		return fmt.Errorf("measure title: %w", err)
	}
	metrics["title"] = TextMetrics{Width: titleW}
	artistW, err := MeasureASSEncodedWidth(ctx, ffmpeg, faces.Medium500.ASSFamily, 500, fontDir, input.Metadata.Artist, artistSize)
	if err != nil {
		return fmt.Errorf("measure artist: %w", err)
	}
	metrics["artist"] = TextMetrics{Width: artistW}
	if input.Metadata.Album != "" {
		albumW, err := MeasureASSEncodedWidth(ctx, ffmpeg, faces.Regular400.ASSFamily, 400, fontDir, input.Metadata.Album, albumSize)
		if err != nil {
			return fmt.Errorf("measure album: %w", err)
		}
		metrics["album"] = TextMetrics{Width: albumW}
	}

	ass, err := BuildVisualizerASSWithMode(input.Metadata, input.Analysis.Duration, layout, fonts, metrics, mode, width, height)
	if err != nil {
		return fmt.Errorf("build ass: %w", err)
	}
	if err := os.WriteFile(assPath, []byte(ass), 0644); err != nil {
		return fmt.Errorf("write ass: %w", err)
	}

	// Build FFmpeg arguments.
	args := formatVisualizerOutputArgs(audioVisualizerFFmpegArgs(input.SourcePath, assPath, fontDir, id, preset, &mode), outDir)

	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	frameReader, frameWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	cmd.Stdin = frameReader

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	frameErrCh := make(chan error, 1)
	go func() {
		defer frameWriter.Close()
		frameErrCh <- WriteVisualizerRGBAFrames(ctx, frameWriter, input, baseRGBA, mode, layout, width, height)
	}()

	if err := cmd.Run(); err != nil {
		frameWriter.Close()
		return fmt.Errorf("ffmpeg: %w\n%s", err, stderr.String())
	}

	if err := <-frameErrCh; err != nil {
		return fmt.Errorf("frame writer: %w", err)
	}

	return nil
}
