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
	return []string{
		"-v", "error",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", 1280, 720),
		"-r", "30",
		"-i", "pipe:0",
		"-i", audioPath,
		"-filter_complex",
		fmt.Sprintf(
			"[1:a]showwaves=s=752x168:rate=30:mode=line:colors=white@0.55[wave];[0:v]format=yuv420p[vis];[vis][wave]overlay=432:320[vid];[vid]ass=%s:fontsdir=%s[out]",
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
		"-hls_segment_filename", "%s/seg-%%05d.ts",
		"-hls_flags", "event+omit_endlist",
		"%s/playlist.m3u8",
	}
}

func escapeFilterPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.ReplaceAll(p, ":", "\\:")
	return p
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

	for fi, frame := range input.Analysis.Frames {
		// Copy base image (background + artwork).
		draw.Draw(canvas, canvas.Bounds(), base, image.Point{}, draw.Src)

		// Readability overlay — semi-transparent fill over the entire frame.
		overlayRect := image.Rect(0, 0, frameW, frameH)
		draw.Draw(canvas, overlayRect, &image.Uniform{mode.Overlay}, image.Point{}, draw.Over)

		// Spectrum bars.
		drawSpectrum(canvas, frame.Spectrum24, mode, layout)

		// Whole-track loudness envelope.
		drawLoudness(canvas, envelope, mode, layout)

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

		x := layout.Loudness.X + i
		// Line width 2.
		blendPixel(canvas, x, y, lineColor)
		blendPixel(canvas, x+1, y, lineColor)
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
	width, height := 1280, 720

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

	metrics := map[string]TextMetrics{}
	titleMetrics, _ := MeasureTextWithFFmpeg(ctx, ffmpeg, fonts.SemiBold600, input.Metadata.Title, 28)
	artistMetrics, _ := MeasureTextWithFFmpeg(ctx, ffmpeg, fonts.Medium500, input.Metadata.Artist, 20)
	metrics["title"] = titleMetrics
	metrics["artist"] = artistMetrics
	if input.Metadata.Album != "" {
		albumMetrics, _ := MeasureTextWithFFmpeg(ctx, ffmpeg, fonts.Regular400, input.Metadata.Album, 16)
		metrics["album"] = albumMetrics
	}

	ass := BuildVisualizerASS(input.Metadata, input.Analysis.Duration, layout, fonts, metrics)
	if err := os.WriteFile(assPath, []byte(ass), 0644); err != nil {
		return fmt.Errorf("write ass: %w", err)
	}

	// Build FFmpeg arguments.
	args := AudioVisualizerFFmpegArgs(input.SourcePath, assPath, filepath.Dir(fonts.Regular400), id, preset)
	outArg := fmt.Sprintf(args[len(args)-2], outDir)
	playlistArg := fmt.Sprintf(args[len(args)-1], outDir)
	args[len(args)-2] = outArg
	args[len(args)-1] = playlistArg

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
