package video

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"imagepadserver/internal/appicon"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// runFFmpegOnce runs a short one-shot ffmpeg command, folding stderr into the
// error on failure.
func runFFmpegOnce(ctx context.Context, ffmpeg string, args []string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	untrack := TrackStartedFFmpeg(cmd)
	defer untrack()
	if err := cmd.Run(); err != nil {
		detail := stderr.String()
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		return fmt.Errorf("%w: %s", err, detail)
	}
	return nil
}

// radioEdgeFadeSeconds is the black fade burned into the start and end of
// every pre-rendered playlist track so track changes cross through black.
const radioEdgeFadeSeconds = 0.7

// RadioTrackFileName is the on-disk name of a pre-rendered playlist track.
// MP4 keeps AAC global headers so downstream copy remuxes stay clean.
func RadioTrackFileName(trackID string) string {
	return "radio-track-" + trackID + ".mp4"
}

// RenderRadioTrack renders the audio visualizer for one playlist track into a
// single MP4 file (with black edge fades) and returns its absolute path. The
// heavy lifting is the same pipeline as RunAudioVisualizerHLS; only the
// output muxer and the edge fades differ. progress (nil ok) receives the
// render fraction (0..1).
func RenderRadioTrack(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, trackID string, preset QualityPreset, progress func(float64)) (string, error) {
	outPath := filepath.Join(outDir, RadioTrackFileName(trackID))
	buildArgs := func(assPath, fontDir string, mode *ForegroundMode, encoder VideoEncoderProfile) []string {
		return audioVisualizerMP4ArgsWithEncoder(input.SourcePath, assPath, fontDir, outPath, preset, mode, encoder, audioLoudnormFilter(input.Kind), input.Analysis.Duration)
	}
	err := runAudioVisualizerEncode(ctx, outDir, ffmpeg, input, "radio-"+trackID, preset, buildArgs, func() { _ = os.Remove(outPath) }, progress)
	if err != nil {
		return "", fmt.Errorf("render radio track: %w", err)
	}
	return outPath, nil
}

func audioVisualizerMP4ArgsWithEncoder(audioPath, assPath, fontDir, outPath string, preset QualityPreset, mode *ForegroundMode, encoder VideoEncoderProfile, audioFilter string, durationSeconds float64) []string {
	args := audioVisualizerCoreArgsWithEncoder(audioPath, assPath, fontDir, preset, mode, encoder, audioFilter, radioEdgeFadeSeconds, durationSeconds)
	return append(args,
		"-movflags", "+faststart",
		"-f", "mp4",
		"-y",
		outPath,
	)
}

// WriteRadioFallbackLogo writes the app logo used by the active standby feeder.
func WriteRadioFallbackLogo(outDir string) (string, error) {
	path := filepath.Join(outDir, "radio-fallback-logo.png")
	if stat, err := os.Stat(path); err == nil && stat.Size() > 0 {
		return path, nil
	}
	if err := os.WriteFile(path, appIconPNG(), 0600); err != nil {
		return "", err
	}
	return path, nil
}

func appIconPNG() []byte {
	return appicon.IconPNG
}

// WriteRadioFallbackPanels writes reusable translucent rounded panels for the
// active standby feeder. FFmpeg draws the animated layers live, while these
// small PNGs keep the rounded UI geometry precise without adding dependencies.
func WriteRadioFallbackPanels(outDir string) (string, string, error) {
	messagePath := filepath.Join(outDir, "radio-fallback-message-panel.png")
	iconPath := filepath.Join(outDir, "radio-fallback-icon-panel.png")
	if err := writeRoundedPanelIfMissing(messagePath, 572, 282, 36, color.RGBA{18, 29, 48, 118}, color.RGBA{158, 215, 255, 116}); err != nil {
		return "", "", err
	}
	if err := writeRoundedPanelIfMissing(iconPath, 288, 288, 38, color.RGBA{255, 255, 255, 12}, color.RGBA{255, 255, 255, 25}); err != nil {
		return "", "", err
	}
	return messagePath, iconPath, nil
}

func writeRoundedPanelIfMissing(path string, width, height, radius int, fill, stroke color.RGBA) error {
	if stat, err := os.Stat(path); err == nil && stat.Size() > 0 {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if insideRoundedRect(x, y, width, height, radius) {
				img.SetRGBA(x, y, fill)
			}
		}
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if insideRoundedRect(x, y, width, height, radius) && !insideRoundedRect(x-2, y-2, width-4, height-4, radius-2) {
				img.SetRGBA(x, y, stroke)
			}
		}
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, img)
}

func insideRoundedRect(x, y, width, height, radius int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if radius <= 0 {
		return x >= 0 && y >= 0 && x < width && y < height
	}
	if x < 0 || y < 0 || x >= width || y >= height {
		return false
	}
	cx := x
	if x >= width-radius {
		cx = width - radius - 1
	} else if x >= radius {
		return true
	}
	cy := y
	if y >= height-radius {
		cy = height - radius - 1
	} else if y >= radius {
		return true
	}
	dx := float64(x - cx)
	dy := float64(y - cy)
	return dx*dx+dy*dy <= float64(radius*radius)
}

// RadioFallbackFeederArgs generates the standby screen live as MPEG-TS. It is
// intentionally not a looped MP4, so waiting streams stay visually active until
// a real track feeder takes over.
func RadioFallbackRenderSize(preset QualityPreset) (int, int) {
	height := preset.Height
	if height <= 0 {
		height = 720
	}
	if height > 360 {
		height = 360
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}
	return width, height
}

func radioFallbackOutputSize(preset QualityPreset) (int, int) {
	height := preset.Height
	if height <= 0 {
		height = 720
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}
	return width, height
}

func RadioFallbackFeederArgs(preset QualityPreset, renderWidth, renderHeight int, timestampOffset float64) []string {
	outputWidth, outputHeight := radioFallbackOutputSize(preset)
	if renderWidth <= 0 || renderHeight <= 0 {
		renderWidth, renderHeight = RadioFallbackRenderSize(preset)
	}
	audioBitrate := preset.AudioBitrate
	if audioBitrate == "" {
		audioBitrate = "128k"
	}
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "-s", fmt.Sprintf("%dx%d", renderWidth, renderHeight), "-r", "30", "-i", "pipe:0",
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo",
		"-map", "0:v:0", "-map", "1:a:0",
	}
	if renderWidth != outputWidth || renderHeight != outputHeight {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d:flags=bicubic", outputWidth, outputHeight))
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-b:v", "300k", "-pix_fmt", "yuv420p", "-g", "60",
		"-c:a", "aac", "-b:a", audioBitrate, "-ar", "48000", "-ac", "2",
	)
	if timestampOffset > 0 {
		args = append(args, "-output_ts_offset", strconv.FormatFloat(timestampOffset, 'f', 3, 64))
	}
	return append(args, "-f", "mpegts", "pipe:1")
}

type RadioFallbackRenderer struct {
	width, height int
	scale         float64
	logo          *image.RGBA
	semiBold      font.Face
	medium        font.Face
	cubes         []radioFallbackCube
}

type radioFallbackCube struct {
	x, y, size, ax, ay, fx, fy, phase float64
	alpha                             uint8
	col                               color.RGBA
}

// NewRadioFallbackRenderer creates the active standby renderer used by the
// playlist fallback stream. It intentionally shares the same visual constants
// as the approved v6 preview: centered logo/message layout, slow color drift,
// broad translucent wave sheets, and small drifting cube bubbles.
func NewRadioFallbackRenderer(width, height int, logoPath, semiboldFontPath, mediumFontPath string) (*RadioFallbackRenderer, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid fallback size %dx%d", width, height)
	}
	scale := float64(height) / 720.0
	logo, err := loadScaledFallbackLogo(logoPath, int(math.Round(240*scale)))
	if err != nil {
		return nil, err
	}
	semiBold, err := loadFallbackFontFace(semiboldFontPath, 39*scale)
	if err != nil {
		return nil, err
	}
	medium, err := loadFallbackFontFace(mediumFontPath, 23*scale)
	if err != nil {
		semiBold.Close()
		return nil, err
	}
	return &RadioFallbackRenderer{
		width:    width,
		height:   height,
		scale:    scale,
		logo:     logo,
		semiBold: semiBold,
		medium:   medium,
		cubes:    fallbackCubes(width, height, scale),
	}, nil
}

func loadScaledFallbackLogo(path string, size int) (*image.RGBA, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	src, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst, nil
}

func loadFallbackFontFace(path string, size float64) (font.Face, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, err := opentype.Parse(data)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

func fallbackCubes(width, height int, scale float64) []radioFallbackCube {
	colors := []color.RGBA{
		{214, 247, 255, 255},
		{142, 231, 199, 255},
		{185, 199, 255, 255},
		{120, 210, 255, 255},
		{176, 245, 222, 255},
	}
	cubes := make([]radioFallbackCube, 0, 126)
	for i := 0; i < 126; i++ {
		cubes = append(cubes, radioFallbackCube{
			x:     float64(8 + (i*157)%max(16, width-16)),
			y:     float64(6 + (i*89)%max(12, height-12)),
			size:  float64(4+(i*7)%15) * scale,
			ax:    float64(10+(i*5)%33) * scale,
			ay:    float64(14+(i*11)%45) * scale,
			fx:    0.05 + float64(i%9)*0.014,
			fy:    0.07 + float64(i%7)*0.021,
			phase: float64(i) * 0.73,
			alpha: uint8(16 + (i*13)%29),
			col:   colors[i%len(colors)],
		})
	}
	return cubes
}

func (r *RadioFallbackRenderer) Close() error {
	if closer, ok := r.semiBold.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	if closer, ok := r.medium.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	return nil
}

func (r *RadioFallbackRenderer) RenderRGB(seconds float64) []byte {
	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	r.drawBackground(img, seconds)
	r.drawWaves(img, seconds)
	r.drawCubes(img, seconds)
	r.drawGlow(img, seconds)
	r.drawPanelsAndText(img)
	if seconds < 0.9 {
		a := uint8(math.Round(255 * (1 - seconds/0.9)))
		alphaRect(img, image.Rect(0, 0, r.width, r.height), color.RGBA{0, 0, 0, a})
	}
	out := make([]byte, r.width*r.height*3)
	j := 0
	for y := 0; y < r.height; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < r.width; x++ {
			i := x * 4
			out[j+0] = row[i+0]
			out[j+1] = row[i+1]
			out[j+2] = row[i+2]
			j += 3
		}
	}
	return out
}

func (r *RadioFallbackRenderer) drawBackground(img *image.RGBA, t float64) {
	baseR := 9 + 18*math.Sin(t*0.15)
	baseG := 21 + 20*math.Sin(t*0.11+1.6)
	baseB := 42 + 26*math.Sin(t*0.09+3.0)
	for y := 0; y < r.height; y++ {
		yn := float64(y) / float64(max(1, r.height))
		for x := 0; x < r.width; x++ {
			xn := float64(x) / float64(max(1, r.width))
			rv := baseR + 11*xn + 8*math.Sin((xn+yn)*5.2+t*0.21)
			gv := baseG + 15*yn + 8*math.Sin(xn*6.8+t*0.16)
			bv := baseB + 19*(1-yn) + 10*math.Sin(yn*6.1+t*0.18)
			img.SetRGBA(x, y, color.RGBA{fallbackClampByte(rv), fallbackClampByte(gv), fallbackClampByte(bv), 255})
		}
	}
}

func (r *RadioFallbackRenderer) drawWaves(img *image.RGBA, t float64) {
	waves := []struct {
		base, amp, thick, speed, freq, phase float64
		col                                  color.RGBA
	}{
		{242, 50, 146, 0.24, 148, 0.2, color.RGBA{82, 203, 255, 72}},
		{360, 68, 184, 0.17, 190, 1.85, color.RGBA{92, 241, 188, 66}},
		{492, 60, 168, 0.12, 226, 3.55, color.RGBA{172, 134, 255, 62}},
	}
	for wi, wave := range waves {
		for band := 0; band < 2; band++ {
			thick := wave.thick * (0.72 + 0.26*math.Sin(t*(0.11+float64(wi)*0.04)+float64(band))) * r.scale
			breath := 1.0 + 0.14*math.Sin(t*(0.13+float64(wi)*0.03)+float64(band)*2.0)
			phase := wave.phase + float64(band)*1.7
			col := wave.col
			if band == 1 {
				col.A = uint8(float64(col.A) * 0.70)
			}
			for x := -12; x < r.width+12; x += max(4, int(8*r.scale)) {
				xf := float64(x)
				y := wave.base*r.scale + wave.amp*r.scale*math.Sin(xf/(wave.freq*r.scale*breath)+t*wave.speed+phase)
				y += 18 * r.scale * math.Sin(xf/(wave.freq*r.scale*0.47)-t*wave.speed*0.86+phase*0.4)
				alphaRect(img, image.Rect(x, int(y-thick/2), x+max(8, int(16*r.scale)), int(y+thick/2)), col)
			}
		}
	}
}

func (r *RadioFallbackRenderer) drawCubes(img *image.RGBA, t float64) {
	for _, c := range r.cubes {
		cx := c.x + c.ax*math.Sin(t*c.fx+c.phase)
		cy := c.y + c.ay*math.Cos(t*c.fy+c.phase*0.8) - math.Mod(t*4.4, 92*r.scale)
		if cy < -24*r.scale {
			cy += float64(r.height) + 48*r.scale
		}
		size := c.size * (0.76 + 0.34*math.Sin(t*0.68+c.phase))
		half := size / 2
		a := uint8(float64(c.alpha) * (0.70 + 0.30*math.Sin(t*0.74+c.phase)))
		col := c.col
		col.A = a
		alphaRect(img, image.Rect(int(cx-half), int(cy-half), int(cx+half), int(cy+half)), col)
	}
}

func (r *RadioFallbackRenderer) drawGlow(img *image.RGBA, t float64) {
	alphaRect(img, scaleRect(70, 130, 508, 590, r.scale), color.RGBA{94, 210, 255, uint8(12 + 5*math.Sin(t*0.68))})
	alphaRect(img, scaleRect(520, 168, 1200, 570, r.scale), color.RGBA{105, 245, 200, 10})
}

func (r *RadioFallbackRenderer) drawPanelsAndText(img *image.RGBA) {
	cy := r.height / 2
	logoSize := r.logo.Bounds().Dx()
	logoX := int(math.Round(146 * r.scale))
	logoY := cy - logoSize/2 + int(math.Round(5*r.scale))
	iconPanel := image.Rect(logoX-int(math.Round(24*r.scale)), cy-int(math.Round(144*r.scale)), logoX-int(math.Round(24*r.scale))+int(math.Round(288*r.scale)), cy+int(math.Round(144*r.scale)))
	messagePanel := image.Rect(int(math.Round(588*r.scale)), cy-int(math.Round(141*r.scale)), int(math.Round((588+572)*r.scale)), cy+int(math.Round(141*r.scale)))
	alphaRoundedRect(img, iconPanel, int(math.Round(38*r.scale)), color.RGBA{255, 255, 255, 12}, color.RGBA{255, 255, 255, 25})
	draw.Draw(img, image.Rect(logoX, logoY, logoX+logoSize, logoY+logoSize), r.logo, image.Point{}, draw.Over)
	alphaRoundedRect(img, messagePanel, int(math.Round(36*r.scale)), color.RGBA{18, 29, 48, 118}, color.RGBA{158, 215, 255, 116})

	textX := messagePanel.Min.X + int(math.Round(50*r.scale))
	textCenter := messagePanel.Min.Y + messagePanel.Dy()/2
	lift := int(math.Round(14 * r.scale))
	drawText(img, r.semiBold, textX, textCenter-int(math.Round(91*r.scale))-lift, "ImagePadServer Playlist", color.RGBA{243, 248, 255, 246})
	drawText(img, r.semiBold, textX, textCenter-int(math.Round(28*r.scale))-lift, "配信待機中", color.RGBA{123, 240, 200, 250})
	drawText(img, r.medium, textX, textCenter+int(math.Round(29*r.scale))-lift, "曲を追加するか、再生を押すと始まります", color.RGBA{213, 225, 239, 228})
	drawText(img, r.medium, textX, textCenter+int(math.Round(76*r.scale))-lift, "RTSP / HLS ready", color.RGBA{159, 180, 200, 220})
}

func WriteRadioFallbackFrames(ctx context.Context, out io.Writer, renderer *RadioFallbackRenderer) error {
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		frame := renderer.RenderRGB(time.Since(start).Seconds())
		if _, err := out.Write(frame); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func drawText(img *image.RGBA, face font.Face, x, y int, text string, col color.RGBA) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.P(x, y+face.Metrics().Ascent.Ceil()),
	}
	d.DrawString(text)
}

func alphaRoundedRect(img *image.RGBA, rect image.Rectangle, radius int, fill, stroke color.RGBA) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			localX := x - rect.Min.X
			localY := y - rect.Min.Y
			if insideRoundedRect(localX, localY, rect.Dx(), rect.Dy(), radius) {
				alphaPixel(img, x, y, fill)
				if !insideRoundedRect(localX-2, localY-2, rect.Dx()-4, rect.Dy()-4, radius-2) {
					alphaPixel(img, x, y, stroke)
				}
			}
		}
	}
}

func alphaRect(img *image.RGBA, rect image.Rectangle, col color.RGBA) {
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() || col.A == 0 {
		return
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			alphaPixel(img, x, y, col)
		}
	}
}

func alphaPixel(img *image.RGBA, x, y int, src color.RGBA) {
	if !image.Pt(x, y).In(img.Bounds()) || src.A == 0 {
		return
	}
	i := img.PixOffset(x, y)
	a := int(src.A)
	inv := 255 - a
	img.Pix[i+0] = uint8((int(src.R)*a + int(img.Pix[i+0])*inv) / 255)
	img.Pix[i+1] = uint8((int(src.G)*a + int(img.Pix[i+1])*inv) / 255)
	img.Pix[i+2] = uint8((int(src.B)*a + int(img.Pix[i+2])*inv) / 255)
	img.Pix[i+3] = 255
}

func scaleRect(x0, y0, x1, y1 int, scale float64) image.Rectangle {
	return image.Rect(
		int(math.Round(float64(x0)*scale)),
		int(math.Round(float64(y0)*scale)),
		int(math.Round(float64(x1)*scale)),
		int(math.Round(float64(y1)*scale)),
	)
}

func fallbackClampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(math.Round(v))
}

// RadioPublisherArgs is the persistent RTMP publisher: it consumes an
// endless MPEG-TS byte stream on stdin and republishes it to mediamtx. The
// feeder side is responsible for making per-segment timestamps monotonic; the
// publisher must preserve those PTS values so frame pacing is not derived from
// pipe arrival timing.
func RadioPublisherArgs(rtmpURL string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "+genpts",
		"-f", "mpegts",
		"-i", "pipe:0",
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		"-f", "flv",
		rtmpURL,
	}
}

// RadioFeederArgs converts one pre-rendered MP4 into a real-time MPEG-TS
// stream on stdout, for piping into the persistent publisher. loop repeats
// the input forever (filler); startSeconds resumes mid-track. timestampOffset
// shifts output PTS so separately started feeders form one monotonic stream.
func RadioFeederArgs(mediaPath string, startSeconds int, loop bool, timestampOffset float64) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-re",
	}
	if loop {
		args = append(args, "-stream_loop", "-1")
	}
	if startSeconds > 0 {
		args = append(args, "-ss", strconv.Itoa(startSeconds))
	}
	args = append(args,
		"-i", mediaPath,
		"-c", "copy",
	)
	if timestampOffset > 0 {
		args = append(args, "-output_ts_offset", strconv.FormatFloat(timestampOffset, 'f', 3, 64))
	}
	return append(args,
		"-f", "mpegts",
		"pipe:1",
	)
}
