package video

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func AudioVisualizerFFmpegArgs(audioPath, assPath, fontDir, id string, preset QualityPreset) []string {
	return audioVisualizerFFmpegArgs(audioPath, assPath, fontDir, id, preset, nil)
}

func audioVisualizerFFmpegArgs(audioPath, assPath, fontDir, id string, preset QualityPreset, mode *ForegroundMode) []string {
	return audioVisualizerFFmpegArgsWithEncoder(audioPath, assPath, fontDir, id, preset, mode, CPUVideoEncoder(EncoderStandard), "")
}

// audioVisualizerFFmpegArgsWithEncoder builds the render command. audioFilter,
// when non-empty, is applied to the audio input (e.g. loudnorm for music) so
// both the burned-in waveform and the output audio reflect the normalized
// signal; the audio is split after filtering to feed showwaves and the muxed
// output from the same filtered stream.
func audioVisualizerFFmpegArgsWithEncoder(audioPath, assPath, fontDir, id string, preset QualityPreset, mode *ForegroundMode, encoder VideoEncoderProfile, audioFilter string) []string {
	args := audioVisualizerCoreArgsWithEncoder(audioPath, assPath, fontDir, preset, mode, encoder, audioFilter, 0, 0)
	return append(args,
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_segment_filename", "%s/"+segmentPattern(id),
		"-hls_flags", "independent_segments",
		"%s/"+playlistName(id),
	)
}

// audioVisualizerCoreArgsWithEncoder builds the shared render command up to
// (but excluding) the output muxer: inputs, filter graph, stream maps, video
// encoder, and audio encode options. Callers append an HLS or MP4 tail.
// edgeFadeSeconds > 0 burns a black fade-in/out (and matching audio fade)
// into the first/last edgeFadeSeconds of the track (曲の切り替わり用).
func audioVisualizerCoreArgsWithEncoder(audioPath, assPath, fontDir string, preset QualityPreset, mode *ForegroundMode, encoder VideoEncoderProfile, audioFilter string, edgeFadeSeconds, totalDurationSeconds float64) []string {
	return audioVisualizerCoreArgsWithEncodeOptions(audioPath, assPath, fontDir, preset, mode, encoder, audioFilter, edgeFadeSeconds, totalDurationSeconds, staticContentEncodeOptions)
}

func audioVisualizerCoreArgsWithEncodeOptions(audioPath, assPath, fontDir string, preset QualityPreset, mode *ForegroundMode, encoder VideoEncoderProfile, audioFilter string, edgeFadeSeconds, totalDurationSeconds float64, encodeOptions func(VideoEncoderProfile) []string) []string {
	return audioVisualizerCoreArgsWithEncoderArgs(audioPath, assPath, fontDir, preset, mode, encoder, audioFilter, edgeFadeSeconds, totalDurationSeconds, nil, encodeOptions)
}

func audioVisualizerCoreArgsWithEncoderArgs(audioPath, assPath, fontDir string, preset QualityPreset, mode *ForegroundMode, encoder VideoEncoderProfile, audioFilter string, edgeFadeSeconds, totalDurationSeconds float64, encoderArgs func(VideoEncoderProfile, QualityPreset) []string, encodeOptions func(VideoEncoderProfile) []string) []string {
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
		waveColor = fmt.Sprintf("#%02X%02X%02X@0.55", mode.AccentColor.R, mode.AccentColor.G, mode.AccentColor.B)
	}
	// When an audio filter is set, run [1:a] through it once and split the
	// result so the waveform and the muxed audio share the filtered stream.
	wavesInput, audioMap, audioPrefix := "1:a", "1:a", ""
	if audioFilter != "" {
		audioPrefix = fmt.Sprintf("[1:a]%s,asplit=2[aud][wsrc];", audioFilter)
		wavesInput, audioMap = "wsrc", "[aud]"
	}
	// The piped frames are already yuv420p (converted on the render workers), so
	// the encoder skips a whole-frame CPU color conversion and the pipe carries
	// ~2.6x less data than rgba.
	filterComplex := audioPrefix + fmt.Sprintf(
		"[%s]showwaves=s=%dx%d:rate=30:mode=line:colors=%s[wave];[0:v][wave]overlay=%d:%d[vid];[vid]ass=filename='%s':fontsdir='%s'[out]",
		wavesInput, waveW, waveH, waveColor, waveX, waveY,
		escapeFilterPath(assPath),
		escapeFilterPath(fontDir),
	)
	videoMap := "[out]"
	if edgeFadeSeconds > 0 && totalDurationSeconds > edgeFadeSeconds*2 {
		fadeOutStart := totalDurationSeconds - edgeFadeSeconds
		filterComplex += fmt.Sprintf(";[out]fade=t=in:st=0:d=%.2f,fade=t=out:st=%.2f:d=%.2f[vfade]",
			edgeFadeSeconds, fadeOutStart, edgeFadeSeconds)
		videoMap = "[vfade]"
		audioSrc := audioMap
		if audioSrc == "1:a" {
			audioSrc = "[1:a]"
		}
		filterComplex += fmt.Sprintf(";%safade=t=in:st=0:d=%.2f,afade=t=out:st=%.2f:d=%.2f[afade]",
			audioSrc, edgeFadeSeconds, fadeOutStart, edgeFadeSeconds)
		audioMap = "[afade]"
	}
	args := []string{
		"-v", "error",
		"-f", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", "30",
		"-i", "pipe:0",
		"-i", audioPath,
		"-filter_complex", filterComplex,
		"-map", videoMap,
		"-map", audioMap,
	}
	if encoderArgs == nil {
		encoderArgs = func(profile VideoEncoderProfile, preset QualityPreset) []string {
			return profile.FFmpegArgs(preset, "medium")
		}
	}
	args = append(args, encoderArgs(encoder, preset)...)
	if encodeOptions != nil {
		args = append(args, encodeOptions(encoder)...)
	}
	return append(args,
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ar", "48000",
		"-ac", "2",
		"-pix_fmt", "yuv420p",
	)
}

func clampByte(v float64) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v + 0.5)
}

// rgbaToYUV420p converts a packed RGBA frame (canvas.Pix, width*height*4 bytes)
// into planar yuv420p / I420 (width*height*3/2 bytes) using BT.709 limited
// range — the standard matrix for HD H.264, matching what ffmpeg's format
// filter produced — so the output is visually identical, just computed here on
// the render workers instead of single-threaded inside ffmpeg. width and height
// must be even.
func rgbaToYUV420p(pix []byte, width, height int, dst []byte) {
	ySize := width * height
	cw, ch := width/2, height/2
	uOff, vOff := ySize, ySize+cw*ch
	for y := 0; y < height; y++ {
		in := y * width * 4
		out := y * width
		for x := 0; x < width; x++ {
			i := in + x*4
			r, g, b := float64(pix[i]), float64(pix[i+1]), float64(pix[i+2])
			dst[out+x] = clampByte(16 + 0.18258588*r + 0.61423059*g + 0.06200706*b)
		}
	}
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			x0, y0 := cx*2, cy*2
			var rs, gs, bs float64
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					i := ((y0+dy)*width + (x0 + dx)) * 4
					rs += float64(pix[i])
					gs += float64(pix[i+1])
					bs += float64(pix[i+2])
				}
			}
			r, g, b := rs/4, gs/4, bs/4
			dst[uOff+cy*cw+cx] = clampByte(128 - 0.10064373*r - 0.33857195*g + 0.43921569*b)
			dst[vOff+cy*cw+cx] = clampByte(128 + 0.43921569*r - 0.39894216*g - 0.04027352*b)
		}
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
// and writes it to dst. Retained for tests and pixel inspection; production
// uses the yuv420p path via writeVisualizerFrames.
func WriteVisualizerRGBAFrames(ctx context.Context, dst io.Writer, input AudioRenderInput, base *image.RGBA, mode ForegroundMode, layout VisualizerLayout, width, height int) error {
	return writeVisualizerFrames(ctx, dst, input, base, mode, layout, width, height, false)
}

// writeVisualizerFrames renders each analysis frame from the pre-rendered base
// image (blurred background + artwork tile + overlay) plus dynamic elements
// (spectrum bars, loudness envelope, progress indicator). When yuv is true it
// emits yuv420p (1.5 B/px) instead of rgba (4 B/px), which more than halves the
// pipe traffic to ffmpeg and lets the encoder skip a whole-frame CPU color
// conversion; the conversion runs on the otherwise-idle render workers.
func writeVisualizerFrames(ctx context.Context, dst io.Writer, input AudioRenderInput, base *image.RGBA, mode ForegroundMode, layout VisualizerLayout, width, height int, yuv bool) error {
	if len(input.Analysis.Frames) == 0 {
		return fmt.Errorf("no analysis frames to render")
	}
	if base == nil {
		return fmt.Errorf("base image is nil")
	}

	totalFrames := len(input.Analysis.Frames)
	duration := input.Analysis.Duration
	frameBytes := width * height * 4
	if yuv {
		frameBytes = width * height * 3 / 2
	}

	// Cache the loudness layer (guide lines + detail curve + trend curve)
	// once per render job instead of recomputing for every frame. Its content
	// only covers part of the frame, so compositing is restricted to that
	// bounding box instead of an expensive whole-frame alpha blend per frame.
	loudnessLayer := buildLoudnessLayer(input.Analysis.Features, duration, mode, layout, width, height)
	loudnessRect := nonTransparentBounds(loudnessLayer)

	// Frames are independent, so they are rendered concurrently across CPUs and
	// written to the encoder in order. This keeps a fast (GPU) encoder fed
	// instead of starving it behind a single-threaded producer.
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers > totalFrames {
		workers = totalFrames
	}
	if workers < 1 {
		workers = 1
	}

	// Bound how far producers may race ahead of the ordered writer via a
	// sliding window. This caps memory (each in-flight frame holds one buffer)
	// and provides backpressure without the buffer-pool starvation that can
	// deadlock an ordered pipeline when one worker outruns the laggard holding
	// the next index to write.
	maxInFlight := workers*2 + 2
	window := make(chan struct{}, maxInFlight)

	type rendered struct {
		idx int
		buf []byte
	}
	jobs := make(chan int)
	results := make(chan rendered, workers)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			canvas := image.NewRGBA(image.Rect(0, 0, width, height))
			for fi := range jobs {
				renderVisualizerFrame(canvas, base, loudnessLayer, loudnessRect,
					input.Analysis.Frames[fi], fi, totalFrames, duration, mode, layout)
				buf := make([]byte, frameBytes)
				if yuv {
					rgbaToYUV420p(canvas.Pix, width, height, buf)
				} else {
					copy(buf, canvas.Pix)
				}
				select {
				case results <- rendered{fi, buf}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Feeder: take a window slot before dispatching each frame so producers
	// cannot outrun the writer by more than maxInFlight frames.
	go func() {
		defer close(jobs)
		for i := 0; i < totalFrames; i++ {
			select {
			case window <- struct{}{}:
			case <-ctx.Done():
				return
			}
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	// Ordered writer: emit frames to the encoder strictly in index order,
	// releasing one window slot per written frame.
	pending := make(map[int][]byte)
	next := 0
	var writeErr error
	for r := range results {
		pending[r.idx] = r.buf
		for {
			buf, ok := pending[next]
			if !ok {
				break
			}
			if writeErr == nil {
				if _, err := dst.Write(buf); err != nil {
					writeErr = err
					cancel() // stop producers; keep draining their buffers
				}
			}
			delete(pending, next)
			<-window
			next++
		}
	}

	if writeErr != nil {
		return fmt.Errorf("frame write: %w", writeErr)
	}
	if err := ctx.Err(); err != nil && next < totalFrames {
		return err
	}
	return nil
}

// renderVisualizerFrame composites one frame into canvas: base image, spectrum
// bars, the cached loudness layer (restricted to its bounding box), and the
// progress marker. It only reads shared inputs, so it is safe to run on many
// goroutines with a per-goroutine canvas.
func renderVisualizerFrame(canvas, base, loudnessLayer *image.RGBA, loudnessRect image.Rectangle, frame AudioFrame, fi, totalFrames int, duration float64, mode ForegroundMode, layout VisualizerLayout) {
	draw.Draw(canvas, canvas.Bounds(), base, image.Point{}, draw.Src)
	drawSpectrumFixedFade(canvas, frame.Spectrum24, mode, layout)
	if !loudnessRect.Empty() {
		draw.Draw(canvas, loudnessRect, loudnessLayer, loudnessRect.Min, draw.Over)
	}
	currentSeconds := float64(fi) / float64(totalFrames) * duration
	drawProgress(canvas, mode, layout, currentSeconds, duration)
}

// nonTransparentBounds returns the smallest rectangle covering every pixel of
// img with a non-zero alpha. It returns the empty rectangle when img is fully
// transparent.
func nonTransparentBounds(img *image.RGBA) image.Rectangle {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y
	found := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[(y-b.Min.Y)*img.Stride:]
		for x := b.Min.X; x < b.Max.X; x++ {
			if row[(x-b.Min.X)*4+3] != 0 {
				found = true
				if x < minX {
					minX = x
				}
				if x >= maxX {
					maxX = x + 1
				}
				if y < minY {
					minY = y
				}
				if y >= maxY {
					maxY = y + 1
				}
			}
		}
	}
	if !found {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX, maxY)
}

// buildLoudnessLayer produces the cached whole-track loudness graph. The stored
// envelope holds absolute RMS values; normalizeRelativeLoudness rescales them
// against the track's own peak so the graph fills the panel regardless of the
// song's absolute level. Without this, quiet tracks pin every point and line to
// the bottom edge.
func buildLoudnessLayer(features AudioFeatures, duration float64, mode ForegroundMode, layout VisualizerLayout, width, height int) *image.RGBA {
	envelope := normalizeRelativeLoudness(features.LoudnessEnvelope)
	trend := SmoothLoudnessTrend(envelope, duration)
	return renderLoudnessLayer(envelope, trend, mode, layout, width, height)
}

// ---------------------------------------------------------------------------
// Whole-track loudness envelope (spec section 12)
// ---------------------------------------------------------------------------

// drawLoudness draws four guide lines and the 1000-sample loudness curve.
func drawLoudness(canvas *image.RGBA, envelope [1000]float64, mode ForegroundMode, layout VisualizerLayout) {
	// Guide lines (four evenly-spaced horizontal lines).
	guideColor := mode.AccentColor
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
	lineColor := mode.AccentColor
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

// RenderLoudnessMaskCPU renders only the canonical loudness layer for parity
// probes, including the four guides and 1000-sample envelope.
func RenderLoudnessMaskCPU(width, height int, envelope [1000]float64, mode ForegroundMode, layout VisualizerLayout) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	drawLoudness(img, envelope, mode, layout)
	return img
}

// drawProgress draws the progress track rectangle and circular position marker.
func drawProgress(canvas *image.RGBA, mode ForegroundMode, layout VisualizerLayout, currentSeconds, duration float64) {
	trackColor := mode.AccentColor
	trackColor.A = uint8(math.Round(0.35 * 255.0))

	markerColor := mode.AccentColor
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

// RenderProgressMaskCPU renders only the canonical progress layer for parity
// probes, using the production rail/thumb implementation.
func RenderProgressMaskCPU(width, height int, mode ForegroundMode, layout VisualizerLayout, currentSeconds, duration float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	drawProgress(img, mode, layout, currentSeconds, duration)
	return img
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
	if executable := strings.TrimSpace(os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")); executable != "" {
		return runAudioVisualizerHLSGPU(ctx, outDir, ffmpeg, executable, input, id, preset)
	}
	return ErrGPURequired
}

// RunAudioVisualizerHLSCPUReference is retained for deterministic reference
// tests and migration comparisons. It must not be used by production routes.
func RunAudioVisualizerHLSCPUReference(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, id string, preset QualityPreset) error {
	buildArgs := func(assPath, fontDir string, mode *ForegroundMode, encoder VideoEncoderProfile) []string {
		args := audioVisualizerFFmpegArgsWithEncoder(input.SourcePath, assPath, fontDir, id, preset, mode, encoder, audioLoudnormFilter(input.Kind))
		// Keep the CPU reference on the same canonical 30 Hz frame clock as
		// the GPU route; this prevents source-duration rounding from adding a
		// mux tail frame.
		limit := []string{"-frames:v", strconv.Itoa(canonicalMusicVideoFrameCount(input.Analysis))}
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-f" && args[i+1] == "hls" {
				args = append(args[:i], append(limit, args[i:]...)...)
				break
			}
		}
		return formatVisualizerOutputArgs(args, outDir)
	}
	return runAudioVisualizerEncode(ctx, outDir, ffmpeg, input, id, preset, EncoderStandard, buildArgs, func() { removeHLSForID(outDir, id) }, nil)
}

// runAudioVisualizerHLSGPU owns dynamic visuals in the wgpu sidecar and lets
// FFmpeg perform only raw-frame encoding and HLS muxing. This deliberately
// contains no showwaves/showfreqs/ASS filters, preventing double rendering.
func runAudioVisualizerHLSGPU(ctx context.Context, outDir, ffmpeg, sidecarExe string, input AudioRenderInput, id string, preset QualityPreset) error {
	height := preset.Height
	if height <= 0 {
		height = 720
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}
	// The GPU compositor must consume the same immutable base raster as the
	// CPU reference (blur, overlay, artwork mask and shadow). Generate it once
	// before the frame loop; dynamic layers remain GPU-owned.
	baseTexture := input.BaseTexture
	// Experimental GPU-base mode deliberately skips the CPU base raster when a
	// real artwork source is available. The canonical scene transports the
	// normalized artwork payload and the Rust compositor owns crop/blur/shadow
	// composition. Keep this opt-in: the default path remains byte-for-byte
	// compatible with the existing CPU-derived base texture.
	useGPUArtworkBase := shouldUseGPUArtworkBase(input)
	gpuLayout, layoutErr := LayoutForSize(width, height)
	if layoutErr != nil {
		return fmt.Errorf("GPU base layout: %w", layoutErr)
	}
	var gpuMode ForegroundMode
	if baseTexture == nil && useGPUArtworkBase {
		if _, ok := normalizeArtwork(input.ArtworkPath); !ok {
			return fmt.Errorf("GPU artwork base: artwork path is not decodable: %q", input.ArtworkPath)
		}
		gpuMode = gpuArtworkForegroundMode(input)
	}
	if baseTexture == nil && !useGPUArtworkBase {
		fonts, fontErr := VisualizerFonts()
		if fontErr != nil {
			return fmt.Errorf("GPU base fonts: %w", fontErr)
		}
		var fallback *image.RGBA
		var fallbackRenderer func(color.RGBA) (*image.RGBA, error)
		if input.ArtworkPath == "" {
			fallback, fontErr = RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, color.RGBA{255, 255, 255, 224}, gpuLayout.Artwork.W)
			if fontErr != nil {
				return fmt.Errorf("GPU base fallback artwork: %w", fontErr)
			}
			fallbackRenderer = func(accent color.RGBA) (*image.RGBA, error) {
				return RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, accent, gpuLayout.Artwork.W)
			}
		}
		var base *image.RGBA
		var baseErr error
		base, gpuMode, baseErr = RenderVisualizerBaseCPUWithFallback(ctx, ffmpeg, input.ArtworkPath, fallback, fallbackRenderer, gpuLayout)
		if baseErr != nil {
			return fmt.Errorf("GPU base raster: %w", baseErr)
		}
		meta, metaErr := NewBaseTextureMetadata("music-base-"+id, base, ColorSRGB)
		if metaErr != nil {
			return fmt.Errorf("GPU base metadata: %w", metaErr)
		}
		baseTexture = &meta
	}
	// GPU text mode keeps font discovery/atlas preparation on the CPU but
	// delegates glyph compositing to the sidecar's WGSL path.  The default
	// remains the libass reference route until parity passes the strict gate.
	useGPUText := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_TEXT_SHADER")) == "1"
	// Use the same libass raster as the CPU path when the caller did not
	// provide one. This prevents the legacy glyph atlas from becoming a
	// second, visually different production text renderer.
	// Production GPU frames intentionally omit screen_rgba text. Text remains
	// available through the scene contract/diagnostic sentinels, while the
	// encoded pass below applies the canonical ASS renderer after YUV output.
	var textOverlay *TextOverlayMetadata

	// Build the same ASS asset and libass measurement metadata as the CPU path.
	fonts, fontErr := VisualizerFonts()
	if fontErr != nil {
		return fmt.Errorf("GPU ASS fonts: %w", fontErr)
	}
	faces, faceErr := ResolveVisualizerFontFaces(fonts)
	if faceErr != nil {
		return fmt.Errorf("GPU ASS faces: %w", faceErr)
	}
	fontDir := filepath.Dir(fonts.Regular400)
	metrics := map[string]TextMetrics{}
	for _, spec := range []struct {
		key, text, family string
		weight, size      int
	}{
		{"title", input.Metadata.Title, faces.SemiBold600.ASSFamily, 600, scaledFontSize(48, width)},
		{"artist", input.Metadata.Artist, faces.Medium500.ASSFamily, 500, scaledFontSize(28, width)},
		{"album", input.Metadata.Album, faces.Regular400.ASSFamily, 400, scaledFontSize(24, width)},
	} {
		if spec.text == "" {
			continue
		}
		mw, me := MeasureASSEncodedWidth(ctx, ffmpeg, spec.family, spec.weight, fontDir, spec.text, spec.size)
		if me != nil {
			return fmt.Errorf("GPU ASS measure %s: %w", spec.key, me)
		}
		metrics[spec.key] = TextMetrics{Width: mw}
	}
	var assPath string
	if !useGPUText {
		assText, assErr := BuildVisualizerASSWithMode(input.Metadata, input.Analysis.Duration, gpuLayout, fonts, metrics, gpuMode, width, height)
		if assErr != nil {
			return fmt.Errorf("GPU ASS build: %w", assErr)
		}
		assFile, assErr := os.CreateTemp(outDir, "gpu-visualizer-*.ass")
		if assErr != nil {
			return fmt.Errorf("GPU ASS temp: %w", assErr)
		}
		assPath = assFile.Name()
		if _, assErr = assFile.WriteString(assText); assErr == nil {
			assErr = assFile.Close()
		} else {
			_ = assFile.Close()
		}
		if assErr != nil {
			_ = os.Remove(assPath)
			return fmt.Errorf("GPU ASS write: %w", assErr)
		}
		defer os.Remove(assPath)
	}
	waveW := int(math.Round(752 * float64(width) / 1280))
	waveH := int(math.Round(168 * float64(height) / 720))
	waveColor := "#FFFFFF@0.55"
	if gpuMode.AccentColor.A != 0 {
		waveColor = fmt.Sprintf("#%02X%02X%02X@0.55", gpuMode.AccentColor.R, gpuMode.AccentColor.G, gpuMode.AccentColor.B)
	}
	// GPU loudness mode transports the canonical Q16 envelope/trend in the
	// scene dynamics storage buffer. The Rust compositor draws the traces and
	// guides analytically; do not build or upload the CPU raster in this mode.
	useGPULoudness := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_LOUDNESS_SHADER")) == "1"
	var loudnessTextureMeta *BaseTextureMetadata
	if !useGPULoudness {
		// Reference-compatible path: pre-render the canonical whole-track graph
		// once and transport it as an immutable GPU texture.
		loudnessLayer := buildLoudnessLayer(input.Analysis.Features, input.Analysis.Duration, gpuMode, gpuLayout, width, height)
		meta, loudnessMetaErr := NewBaseTextureMetadata("loudness-"+id, loudnessLayer, ColorSRGB)
		if loudnessMetaErr != nil {
			return fmt.Errorf("GPU loudness metadata: %w", loudnessMetaErr)
		}
		loudnessTextureMeta = &meta
	}
	audioFilter := audioLoudnormFilter(input.Kind)
	if audioFilter == "" {
		audioFilter = "anull"
	}
	// The GPU path uses the same duration-derived 30 Hz clock as the CPU
	// reference. Scene sampling itself clamps to the final analysis sample when
	// a partial tail tick has no corresponding analysis frame; silently adding
	// synthetic frames would make the two renderers diverge.
	frameCount := canonicalMusicVideoFrameCount(input.Analysis)
	tmp, err := os.CreateTemp(outDir, "gpu-video-*.ts")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	assFilter := ""
	if !useGPUText {
		assFilter = "ass" + "=filename='" + escapeFilterPath(assPath) + "':fontsdir='" + escapeFilterPath(fontDir) + "'"
	}
	useWaveFilter := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_WAVE_FILTER")) == "1"
	if useGPUText {
		useWaveFilter = false
	}
	useSpectrumCanonical := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_SPECTRUM_CANONICAL")) == "1"
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", width, height), "-r", "30", "-i", "pipe:0"}
	if useWaveFilter {
		args = append(args, "-i", input.SourcePath, "-filter_complex", fmt.Sprintf("[1:a]%s=s=%dx%d:rate=30:mode=line:colors=%s[wave];[0:v][wave]overlay=%d:%d[v0];[v0]%s[v]", "show"+"waves", waveW, waveH, waveColor, gpuLayout.Spectrum.X, gpuLayout.Spectrum.Y, assFilter), "-map", "[v]")
	} else if !useGPUText {
		args = append(args, "-vf", assFilter)
	}
	args = append(args, "-frames:v", strconv.Itoa(frameCount), "-fps_mode", "cfr", "-an", "-sws_flags", "bicubic+accurate_rnd+full_chroma_int", "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv", "-f", "mpegts", tmpPath)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	// A pipe Write may block while FFmpeg is stalled (for example while it is
	// flushing an HLS segment). Close the pipe on cancellation so the blocked
	// writer wakes up and CommandContext can reap FFmpeg deterministically.
	stopPipe := make(chan struct{})
	defer close(stopPipe)
	go func() {
		select {
		case <-ctx.Done():
			_ = in.Close()
		case <-stopPipe:
		}
	}()
	sidecar, err := StartSidecar(ctx, sidecarExe, "music-"+id)
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("%w: start sidecar: %v", ErrGPURendererUnavailable, err)
	}
	defer sidecar.Close()
	if err := sidecar.Hello(ctx, "music-"+id); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("%w: sidecar hello: %v", ErrGPURendererUnavailable, err)
	}
	// GPU-only production can require the sidecar to advertise its native
	// YUV420P transport.  Keep this opt-in while the transport is being rolled
	// out, but fail closed when explicitly requested: never silently convert the
	// GPU RGBA result on the CPU.
	yuvRequired := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_YUV_REQUIRED")) == "1"
	if yuvRequired {
		if err := sidecar.RequireYUV420Output(); err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("%w: sidecar YUV420P output required: %v", ErrGPURendererUnavailable, err)
		}
	}
	frames := frameCount
	postYUVFilter := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_SPECTRUM_POST_YUV_FILTER")) == "1"
	useGPUWaveform := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_WAVEFORM_SHADER")) == "1"
	postYUV := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_SPECTRUM_POST_YUV")) == "1" && !postYUVFilter
	if yuvRequired && (postYUV || postYUVFilter) {
		_ = cmd.Process.Kill()
		return errors.New("GPU YUV-required route is incompatible with post-YUV filters")
	}
	if yuvRequired && strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_WAVE_FILTER")) == "1" {
		_ = cmd.Process.Kill()
		return errors.New("GPU YUV-required route is incompatible with wave filter")
	}
	var postYUVSpectrum [][]byte
	postArtifacts := gpuPostYUVArtifacts{}
	postYUVMax := 3
	if postYUV && frames < postYUVMax {
		postYUVMax = frames
	}
	// Dynamic GPU mode owns waveform and spectrum primitives in WGSL. Avoid
	// starting FFmpeg's showwaves producer entirely; the loop still receives a
	// correctly sized zero payload for legacy metadata branches that are
	// explicitly enabled alongside the experiment.
	useGPUDynamic := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_DYNAMIC_SHADER")) == "1"
	if useGPUDynamic {
		useGPUWaveform = true
		useSpectrumCanonical = false
		postYUV = false
		postYUVFilter = false
	}
	waveFrames := make(chan []byte, 2)
	waveErr := make(chan error, 1)
	waveCtx, waveCancel := context.WithCancel(ctx)
	defer waveCancel()
	if !useGPUDynamic {
		go func() {
			err := StreamAudioWaveFrames(waveCtx, ffmpeg, input.SourcePath, waveW, waveH, frames, waveColor, audioLoudnormFilter(input.Kind), func(_ int, rgba []byte) error {
				select {
				case waveFrames <- rgba:
					return nil
				case <-waveCtx.Done():
					return waveCtx.Err()
				}
			})
			close(waveFrames)
			waveErr <- err
		}()
	}
	for i := 0; i < frames; i++ {
		var wave []byte
		if useGPUDynamic {
			wave = make([]byte, waveW*waveH*4)
		} else {
			var ok bool
			wave, ok = <-waveFrames
			if !ok {
				if e := <-waveErr; e != nil {
					_ = cmd.Process.Kill()
					return fmt.Errorf("audio wave stream: %w", e)
				}
				return fmt.Errorf("audio wave stream ended early at frame %d", i)
			}
		}
		ptsNS := int64(float64(i) * float64(time.Second) / 30)
		scene := CanonicalMusicScene(input, uint64(i), ptsNS)
		if useGPUWaveform {
			if i < len(input.Analysis.WaveformFrames) && len(input.Analysis.WaveformFrames[i]) > 0 {
				scene.Feature.WaveformQ16 = input.Analysis.WaveformFrames[i]
			} else {
				scene.Feature.WaveformQ16 = waveformEnvelopeQ16(wave, waveW, waveH)
			}
		}
		if useGPUWaveform && len(scene.Feature.WaveformQ16) > 0 {
			// GPU waveform primitive owns this layer; do not also upload the
			// legacy FFmpeg raster texture in the GPU-only path.
			scene.WaveformTexture = nil
		}
		// GPU text mode owns glyph compositing in WGSL. The reference route
		// disables both scene text payloads and applies canonical ASS later.
		if useGPUText {
			scene.TextOverlay = nil
		} else {
			scene.TextOverlay = nil
			scene.GlyphAtlas = nil
		}
		// The CPU base builder is also the source of truth for foreground
		// colors. Keep GPU dynamic layers on that same palette; the generic
		// feature palette is only a fallback for diagnostic scenes.
		if gpuMode.PrimaryColor.A != 0 || gpuMode.AccentColor.A != 0 {
			scene.Palette.Primary = [4]uint8{gpuMode.PrimaryColor.R, gpuMode.PrimaryColor.G, gpuMode.PrimaryColor.B, gpuMode.PrimaryColor.A}
			scene.Palette.Accent = [4]uint8{gpuMode.AccentColor.R, gpuMode.AccentColor.G, gpuMode.AccentColor.B, gpuMode.AccentColor.A}
		}
		if baseTexture != nil {
			scene.BaseTexture = baseTexture
		}
		if textOverlay != nil {
			scene.TextOverlay = textOverlay
		}
		if loudnessTextureMeta != nil {
			scene.LoudnessTexture = loudnessTextureMeta
		} else {
			// Keep the analytic GPU dynamics path authoritative. In particular,
			// nil is required so the sidecar does not disable its shader branch.
			scene.LoudnessTexture = nil
		}
		if !useGPUDynamic {
			waveStride := ((waveW*4 + int(GPURowAlignment) - 1) / int(GPURowAlignment)) * int(GPURowAlignment)
			wavePayload := make([]byte, waveStride*waveH)
			for y := 0; y < waveH; y++ {
				copy(wavePayload[y*waveStride:y*waveStride+waveW*4], wave[y*waveW*4:(y+1)*waveW*4])
			}
			waveMeta := BaseTextureMetadata{TextureID: fmt.Sprintf("wave-%s-%d", id, i), Width: uint32(waveW), Height: uint32(waveH), RowStride: uint32(waveStride), Format: PixelRGBA8, ColorSpace: ColorSRGB, Payload: wavePayload}
			scene.WaveformTexture = &waveMeta
		}
		if useSpectrumCanonical || postYUV || postYUVFilter {
			// The canonical layer already contains the showwaves raster; do not
			// upload the source waveform as well or it would be composited twice.
			scene.WaveformTexture = nil
			frameIndex := i
			if frameIndex >= len(input.Analysis.Frames) {
				frameIndex = len(input.Analysis.Frames) - 1
			}
			var composite *image.RGBA
			if postYUVFilter {
				// Post-YUV FFmpeg supplies the waveform; retain only the analytic
				// bars in the GPU scene so neither layer is omitted or duplicated.
				var spectrum [24]float64
				copy(spectrum[:], input.Analysis.Frames[frameIndex].Spectrum24[:])
				composite = RenderSpectrumMaskCPU(width, height, spectrum, gpuMode, gpuLayout)
			} else {
				composite = RenderSpectrumCompositeTextureCPU(width, height, input.Analysis.Frames[frameIndex].Spectrum24, wave, waveW, waveH, gpuMode, gpuLayout)
			}
			meta, metaErr := NewBaseTextureMetadata(fmt.Sprintf("spectrum-%s-%d", id, i), composite, ColorSRGB)
			if metaErr != nil {
				_ = cmd.Process.Kill()
				return fmt.Errorf("spectrum texture metadata: %w", metaErr)
			}
			if useSpectrumCanonical {
				scene.SpectrumTexture = &meta
			}
			if postYUV && i < postYUVMax {
				// Main GPU YUV already contains the analytic bars. Post-YUV
				// overlay must carry only the FFmpeg showwaves raster.
				postYUVSpectrum = append(postYUVSpectrum, append([]byte(nil), wave...))
			}
		}
		if postYUVFilter {
			// The canonical showwaves raster is inserted after GPU YUV conversion.
			// Keep the GPU scene free of spectrum/wave layers to avoid double draw.
			scene.WaveformTexture = nil
			// SpectrumTexture remains the bars-only texture prepared above.
		}
		if useWaveFilter {
			scene.WaveformTexture = nil
		}
		var yuv []byte
		if yuvRequired {
			yuvFrame, renderErr := sidecar.RenderSceneYUV(ctx, uint32(width), uint32(height), uint64(i), ptsNS, &scene)
			if renderErr != nil {
				_ = cmd.Process.Kill()
				return fmt.Errorf("%w: render yuv frame: %v", ErrGPURendererUnavailable, renderErr)
			}
			yuv, renderErr = yuvFrame.PackedBytes()
			if renderErr != nil {
				_ = cmd.Process.Kill()
				return fmt.Errorf("%w: pack yuv frame: %v", ErrGPURendererUnavailable, renderErr)
			}
		} else {
			frame, renderErr := sidecar.RenderScene(ctx, uint32(width), uint32(height), uint64(i), ptsNS, &scene)
			if renderErr != nil {
				_ = cmd.Process.Kill()
				return fmt.Errorf("%w: render frame: %v", ErrGPURendererUnavailable, renderErr)
			}
			packed, renderErr := GPUFrameToPackedRGBA(frame)
			if renderErr != nil {
				_ = cmd.Process.Kill()
				return fmt.Errorf("%w: pack frame: %v", ErrGPURendererUnavailable, renderErr)
			}
			yuv = make([]byte, width*height*3/2)
			rgbaToYUV420p(packed, width, height, yuv)
		}
		if err := writeGPUFrame(ctx, in, yuv); err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("GPU HLS frame write: %w", err)
		}
	}
	if postYUV && len(postYUVSpectrum) > 0 {
		postArtifacts.spectrumRaw = filepath.Join(outDir, ".spectrum-post-yuv-"+id+".rgba")
		defer postArtifacts.cleanup()
		if _, err := writeSpectrumRawFrames(ctx, postArtifacts.spectrumRaw, postYUVSpectrum, waveW, waveH, postYUVMax); err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("post-yuv spectrum raw: %w", err)
		}
	}
	if !useGPUDynamic {
		if e := <-waveErr; e != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("audio wave stream: %w", e)
		}
	}
	_ = in.Close()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("GPU HLS encode: %w: %s", err, trimOutput(stderr.Bytes()))
	}
	if postYUV && postArtifacts.spectrumRaw != "" {
		postArtifacts.overlayTS = filepath.Join(outDir, ".spectrum-overlay-"+id+".ts")
		overlayArgs := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", tmpPath, "-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", waveW, waveH), "-r", "30", "-i", postArtifacts.spectrumRaw, "-filter_complex", fmt.Sprintf("[0:v][1:v]overlay=%d:%d:format=auto[v]", gpuLayout.Spectrum.X, gpuLayout.Spectrum.Y), "-map", "[v]", "-frames:v", strconv.Itoa(postYUVMax), "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv", "-f", "mpegts", postArtifacts.overlayTS}
		overlayCmd := exec.CommandContext(ctx, ffmpeg, overlayArgs...)
		hideWindow(overlayCmd)
		var overlayErr bytes.Buffer
		overlayCmd.Stderr = &overlayErr
		if err := overlayCmd.Run(); err != nil {
			return fmt.Errorf("GPU post-YUV spectrum overlay: %w: %s", err, trimOutput(overlayErr.Bytes()))
		}
		tmpPath = postArtifacts.overlayTS
		frameCount = postYUVMax
	}
	if postYUVFilter && !useGPUText {
		filterTS := filepath.Join(outDir, ".spectrum-filter-"+id+".ts")
		defer os.Remove(filterTS)
		graph := buildMusicPostYUVFilter(waveW, waveH, gpuLayout.Spectrum.X, gpuLayout.Spectrum.Y, waveColor, audioFilter, assPath, fontDir)
		filterArgs := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", tmpPath, "-i", input.SourcePath, "-filter_complex", graph, "-map", "[out]", "-frames:v", strconv.Itoa(frameCount), "-an", "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv", "-f", "mpegts", filterTS}
		filterCmd := exec.CommandContext(ctx, ffmpeg, filterArgs...)
		hideWindow(filterCmd)
		var filterErr bytes.Buffer
		filterCmd.Stderr = &filterErr
		if err := filterCmd.Run(); err != nil {
			return fmt.Errorf("GPU post-YUV filter: %w: %s", err, trimOutput(filterErr.Bytes()))
		}
		tmpPath = filterTS
	}
	// Mux audio in a separate pass. Keeping audio away from the raw-video
	// encoder prevents FFmpeg's audio EOF from truncating the final video GOP.
	finalArgs := []string{"-hide_banner", "-loglevel", "error", "-i", tmpPath, "-i", input.SourcePath, "-map", "0:v:0", "-map", "1:a:0", "-af", audioFilter, "-fps_mode", "passthrough", "-frames:v", strconv.Itoa(frameCount), "-max_interleave_delta", "0", "-c:v", "copy", "-c:a", "aac", "-ar", "48000", "-ac", "2", "-f", "hls", "-hls_time", "4", "-hls_list_size", "0", "-hls_playlist_type", "event", "-hls_flags", "independent_segments", "-hls_segment_filename", filepath.Join(outDir, segmentPattern(id)), filepath.Join(outDir, playlistName(id))}
	finalCmd := exec.CommandContext(ctx, ffmpeg, finalArgs...)
	hideWindow(finalCmd)
	var finalErr bytes.Buffer
	finalCmd.Stderr = &finalErr
	if err := finalCmd.Run(); err != nil {
		return fmt.Errorf("GPU HLS mux: %w: %s", err, trimOutput(finalErr.Bytes()))
	}
	return nil
}

// shouldUseGPUArtworkBase gates the opt-in base migration. A caller-provided
// BaseTexture remains authoritative, and fallback artwork is intentionally not
// routed through this experiment because its CPU-generated silhouette is not
// the same payload as a real cover image.
func shouldUseGPUArtworkBase(input AudioRenderInput) bool {
	return strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_BASE_SHADER")) == "1" &&
		input.BaseTexture == nil && strings.TrimSpace(input.ArtworkPath) != ""
}

// gpuArtworkForegroundMode supplies the palette roles needed by the dynamic
// shader/ASS metadata without rasterizing a CPU base. The artwork itself is
// still decoded once as an input payload; crop, blur, shadow and composite are
// performed by the wgpu sidecar.
func gpuArtworkForegroundMode(input AudioRenderInput) ForegroundMode {
	p := PaletteForFeatures(input.Analysis.Features)
	primary := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	accent := color.RGBA{R: p.End.R, G: p.End.G, B: p.End.B, A: 255}
	overlay := color.RGBA{A: 92}
	return ForegroundMode{PrimaryColor: primary, AccentColor: accent, Overlay: overlay, Color: accent}
}

// waveformEnvelopeQ16 converts the already-decoded audio raster into bounded
// transport samples for the GPU waveform shader. It does not draw anything;
// the sidecar performs the final line rasterization.
func waveformEnvelopeQ16(rgba []byte, width, height int) []uint16 {
	if width <= 0 || height <= 0 || len(rgba) < width*height*4 {
		return nil
	}
	out := make([]uint16, width)
	for x := 0; x < width; x++ {
		var weighted, total uint64
		for y := 0; y < height; y++ {
			i := (y*width + x) * 4
			a := uint64(rgba[i+3])
			if a > 0 {
				weighted += uint64(height-1-y) * a
				total += a
			}
		}
		if total > 0 {
			denom := height - 1
			if denom < 1 {
				denom = 1
			}
			out[x] = uint16((weighted * 65535) / (total * uint64(denom)))
		}
	}
	return out
}

// canonicalMusicVideoFrameCount returns the 30 Hz output clock used by the
// CPU filter graph. Analysis samples can be shorter than the media duration;
// the final tick is still a real video frame and must be represented in HLS.
func canonicalMusicVideoFrameCount(analysis AudioAnalysis) int {
	frames := int(math.Ceil(analysis.Duration * 30))
	if frames < 1 {
		frames = len(analysis.Frames)
	}
	if frames < 1 {
		frames = 1
	}
	return frames
}

// writeGPUFrame makes cancellation observable even when the downstream
// encoder stops reading its stdin. The close-on-cancel watcher above unblocks
// the pipe writer; this wrapper preserves short-write detection.
func writeGPUFrame(ctx context.Context, dst io.Writer, frame []byte) error {
	done := make(chan error, 1)
	go func() {
		n, err := dst.Write(frame)
		if err == nil && n != len(frame) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// visualizerProgressWriter reports the fraction of raw frame bytes streamed
// into ffmpeg, which tracks encode progress closely because the encoder
// consumes the pipe at its own pace.
type visualizerProgressWriter struct {
	w       io.Writer
	total   int64
	written int64
	lastPct int
	report  func(float64)
}

func (p *visualizerProgressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.total > 0 && p.report != nil {
		if pct := int(p.written * 100 / p.total); pct > p.lastPct {
			p.lastPct = pct
			p.report(float64(p.written) / float64(p.total))
		}
	}
	return n, err
}

// runAudioVisualizerEncode drives the shared visualizer pipeline (base image,
// ASS subtitles, frame streaming into ffmpeg) with the output format supplied
// by buildArgs; cleanup removes partial output when an encode attempt fails.
// progress, when non-nil, receives the render fraction (0..1).
func runAudioVisualizerEncode(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, id string, preset QualityPreset, purpose EncoderPurpose, buildArgs func(assPath, fontDir string, mode *ForegroundMode, encoder VideoEncoderProfile) []string, cleanup func(), progress func(float64)) error {
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
	var fallbackRenderer func(color.RGBA) (*image.RGBA, error)
	if input.ArtworkPath == "" {
		fallback, err = RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, color.RGBA{255, 255, 255, 224}, layout.Artwork.W)
		if err != nil {
			return fmt.Errorf("fallback artwork: %w", err)
		}
		fallbackRenderer = func(accent color.RGBA) (*image.RGBA, error) {
			return RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, accent, layout.Artwork.W)
		}
	}

	mode, err := prepareVisualizerBase(ctx, ffmpeg, input.ArtworkPath, fallback, fallbackRenderer, layout, basePath)
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
	// Only measure fields that carry text. Measuring an empty string renders a
	// blank frame and fails with "no text pixels found", so empty title/artist
	// (e.g. a local file with no metadata tags) is skipped here exactly as the
	// album field already is.
	if input.Metadata.Title != "" {
		titleW, err := MeasureASSEncodedWidth(ctx, ffmpeg, faces.SemiBold600.ASSFamily, 600, fontDir, input.Metadata.Title, titleSize)
		if err != nil {
			return fmt.Errorf("measure title: %w", err)
		}
		metrics["title"] = TextMetrics{Width: titleW}
	}
	if input.Metadata.Artist != "" {
		artistW, err := MeasureASSEncodedWidth(ctx, ffmpeg, faces.Medium500.ASSFamily, 500, fontDir, input.Metadata.Artist, artistSize)
		if err != nil {
			return fmt.Errorf("measure artist: %w", err)
		}
		metrics["artist"] = TextMetrics{Width: artistW}
	}
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

	selected := SelectVideoEncoder(ctx, ffmpeg, purpose)
	attempt := func(encoder VideoEncoderProfile) error {
		args := buildArgs(assPath, fontDir, &mode, encoder)
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		hideWindow(cmd)
		frameReader, frameWriter, pipeErr := os.Pipe()
		if pipeErr != nil {
			return fmt.Errorf("pipe: %w", pipeErr)
		}
		defer frameReader.Close()
		cmd.Stdin = frameReader

		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		frameErrCh := make(chan error, 1)
		go func() {
			defer frameWriter.Close()
			var dst io.Writer = frameWriter
			if progress != nil {
				total := int64(len(input.Analysis.Frames)) * int64(width*height*3/2)
				if total > 0 {
					dst = &visualizerProgressWriter{w: frameWriter, total: total, report: progress}
				}
			}
			frameErrCh <- writeVisualizerFrames(ctx, dst, input, baseRGBA, mode, layout, width, height, true)
		}()

		runErr := cmd.Run()
		if runErr != nil {
			_ = frameWriter.Close()
		}
		frameErr := <-frameErrCh
		if runErr != nil {
			return fmt.Errorf("ffmpeg %s: %w\n%s", encoder.Name, runErr, stderr.String())
		}
		if frameErr != nil {
			return fmt.Errorf("frame writer: %w", frameErr)
		}
		return nil
	}
	return runVideoEncodeWithFallback(ctx, selected, cleanup, attempt)
}
