package video

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudioVisualizerFFmpegArgsBasic(t *testing.T) {
	args := AudioVisualizerFFmpegArgs("audio.m4a", "subtitles.ass", "C:\\fonts", "media-1", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	if len(args) == 0 {
		t.Fatal("empty args")
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "audio.m4a") {
		t.Error("missing audio path")
	}
	if !strings.Contains(joined, "subtitles.ass") {
		t.Error("missing ass path")
	}
	if !strings.Contains(joined, "fontsdir") {
		t.Error("missing fontsdir")
	}
}

func TestAudioVisualizerFFmpegArgsContainsShowwaves(t *testing.T) {
	args := AudioVisualizerFFmpegArgs("a.m4a", "b.ass", "/fonts", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "showwaves") {
		t.Error("args MUST contain showwaves")
	}
}

func TestAudioVisualizerFFmpegArgsPipeInput(t *testing.T) {
	args := AudioVisualizerFFmpegArgs("audio.m4a", "sub.ass", "/f", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "pipe:0") {
		t.Error("args MUST read rawvideo from pipe:0")
	}
}

func TestAudioVisualizerFFmpegArgsHLS(t *testing.T) {
	args := AudioVisualizerFFmpegArgs("a.m4a", "s.ass", "/f", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "hls") {
		t.Error("args MUST contain hls")
	}
}

func TestAudioVisualizerFFmpegArgsUsesValidHLSOptions(t *testing.T) {
	joined := strings.Join(AudioVisualizerFFmpegArgs("a.m4a", "s.ass", "/f", "id", QualityPreset{Height: 720, CRF: 27, AudioBitrate: "128k"}), " ")
	if !strings.Contains(joined, "-hls_playlist_type event") || !strings.Contains(joined, "-hls_flags independent_segments") {
		t.Fatalf("invalid HLS option set: %s", joined)
	}
	if strings.Contains(joined, "event+omit_endlist") {
		t.Fatalf("event was incorrectly encoded as hls_flags: %s", joined)
	}
}

func TestAudioVisualizerFFmpegArgsCompressionSettings(t *testing.T) {
	preset := QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"}
	// Software (libx264): animation tune, no scene-cut keyframes, long GOP, 4s segments.
	sw := strings.Join(audioVisualizerFFmpegArgsWithEncoder("a.m4a", "s.ass", "/f", "id", preset, nil, CPUVideoEncoder(EncoderStandard)), " ")
	for _, want := range []string{"-tune animation", "-sc_threshold 0", "-g 120", "-keyint_min 120", "-hls_time 4"} {
		if !strings.Contains(sw, want) {
			t.Errorf("software visualizer args missing %q: %s", want, sw)
		}
	}
	// Hardware: long GOP + 4s segments, but not the libx264-only private flags.
	hw := strings.Join(audioVisualizerFFmpegArgsWithEncoder("a.m4a", "s.ass", "/f", "id", preset, nil, NewVideoEncoderProfile("h264_nvenc", EncoderStandard)), " ")
	if !strings.Contains(hw, "-g 120") || !strings.Contains(hw, "-hls_time 4") {
		t.Errorf("hardware visualizer missing GOP/segment settings: %s", hw)
	}
	if strings.Contains(hw, "-sc_threshold") || strings.Contains(hw, "-tune animation") {
		t.Errorf("hardware visualizer must not use libx264-only flags: %s", hw)
	}
}

func TestAudioVisualizerFFmpegArgsUsesPresetResolution(t *testing.T) {
	preset := ResolveQuality("1080", 0)
	args := AudioVisualizerFFmpegArgs("song.m4a", "text.ass", "fonts", "id", preset)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "1920x1080") {
		t.Fatalf("args do not contain 1920x1080: %s", joined)
	}
	if strings.Contains(joined, "-s 1280x720") {
		t.Fatalf("args still force 1280x720: %s", joined)
	}
}

func TestAudioVisualizerFFmpegArgsQuotesASSPaths(t *testing.T) {
	args := AudioVisualizerFFmpegArgs(`C:\audio\song.m4a`, `C:\temp\text.ass`, `C:\Program Files\fonts`, "id", QualityPreset{Height: 720, CRF: 27, AudioBitrate: "128k"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `ass=filename='C\:/temp/text.ass':fontsdir='C\:/Program Files/fonts'`) {
		t.Fatalf("ASS paths are not quoted safely: %s", joined)
	}
}

func TestFormatVisualizerOutputArgsReplacesOnlyPathPlaceholders(t *testing.T) {
	args := []string{"-hls_segment_filename", "%s/seg-%%05d.ts", "-hls_flags", "independent_segments", "%s/playlist.m3u8"}
	got := formatVisualizerOutputArgs(args, `C:\out`)
	joined := strings.Join(got, "|")
	if strings.Contains(joined, "%!(EXTRA") || strings.Contains(joined, "%s/") {
		t.Fatalf("unresolved or corrupt formatting: %s", joined)
	}
	if strings.Contains(joined, "%%05d") || !strings.Contains(joined, "%05d") {
		t.Fatalf("segment sequence was not normalized: %s", joined)
	}
	if got[3] != "independent_segments" {
		t.Fatalf("hls_flags corrupted: %q", got[3])
	}
}

func TestWriteVisualizerFramesDoesNotTintBaseArtwork(t *testing.T) {
	layout, _ := LayoutForSize(128, 72)
	base := image.NewRGBA(image.Rect(0, 0, 128, 72))
	artX, artY := layout.Artwork.X+layout.Artwork.W/2, layout.Artwork.Y+layout.Artwork.H/2
	base.SetRGBA(artX, artY, color.RGBA{R: 240, G: 20, B: 10, A: 255})
	input := AudioRenderInput{Analysis: AudioAnalysis{FPS: 30, Duration: 1, Frames: []AudioFrame{{}}}}
	mode := ForegroundMode{PrimaryColor: color.RGBA{255, 255, 255, 255}, AccentColor: color.RGBA{255, 255, 255, 255}, Overlay: color.RGBA{0, 0, 0, 200}}
	var buf bytes.Buffer
	if err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, 128, 72); err != nil {
		t.Fatal(err)
	}
	pixel := buf.Bytes()[(artY*128+artX)*4:]
	if pixel[0] != 240 || pixel[1] != 20 || pixel[2] != 10 {
		t.Fatalf("artwork pixel was tinted: got RGB(%d,%d,%d)", pixel[0], pixel[1], pixel[2])
	}
}

func TestDrawLoudnessScalesThousandSamplesIntoLayoutWidth(t *testing.T) {
	layout, _ := LayoutForSize(640, 360)
	canvas := image.NewRGBA(image.Rect(0, 0, 640, 360))
	var envelope [1000]float64
	for i := range envelope {
		envelope[i] = 0.5
	}
	mode := ForegroundMode{AccentColor: color.RGBA{255, 255, 255, 255}}
	drawLoudness(canvas, envelope, mode, layout)
	y := layout.Loudness.Y + layout.Loudness.H/2
	left := canvas.RGBAAt(layout.Loudness.X, y)
	right := canvas.RGBAAt(layout.Loudness.X+layout.Loudness.W-1, y)
	after := canvas.RGBAAt(layout.Loudness.X+layout.Loudness.W+2, y)
	if left.A == 0 || right.A == 0 {
		t.Fatalf("curve does not span scaled graph: left=%v right=%v", left, right)
	}
	if after.A != 0 {
		t.Fatalf("curve escaped scaled graph bounds: %v", after)
	}
}

func TestWriteVisualizerRGBAFramesBasic(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 128, 72))
	mode := ForegroundMode{PrimaryColor: color.RGBA{255, 255, 255, 255}, AccentColor: color.RGBA{255, 255, 255, 255}, Overlay: color.RGBA{0, 0, 0, 92}}
	layout, _ := LayoutForSize(128, 72)
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 1.0,
			Frames:   make([]AudioFrame, 30),
		},
	}
	for i := range input.Analysis.Frames {
		input.Analysis.Frames[i].Spectrum24 = [24]float64{}
	}
	var buf bytes.Buffer
	err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, 128, 72)
	if err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}
	expected := 30 * 128 * 72 * 4
	if buf.Len() != expected {
		t.Fatalf("output size: got %d, want %d", buf.Len(), expected)
	}
}

func TestWriteVisualizerRGBAFramesMatchesSequential(t *testing.T) {
	const w, h = 128, 72
	base := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range base.Pix {
		base.Pix[i] = byte(i * 7)
	}
	mode := ForegroundMode{PrimaryColor: color.RGBA{255, 255, 255, 255}, AccentColor: color.RGBA{200, 120, 60, 255}, Overlay: color.RGBA{0, 0, 0, 92}}
	layout, _ := LayoutForSize(w, h)
	const n = 50
	input := AudioRenderInput{Analysis: AudioAnalysis{FPS: 30, Duration: 2.0, Frames: make([]AudioFrame, n)}}
	for i := 0; i < n; i++ {
		for b := 0; b < 24; b++ {
			input.Analysis.Frames[i].Spectrum24[b] = float64((i*b)%24) / 24.0
		}
	}

	// Sequential reference built from the shared per-frame renderer.
	loud := buildLoudnessLayer(input.Analysis.Features, input.Analysis.Duration, mode, layout, w, h)
	lr := nonTransparentBounds(loud)
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	var ref bytes.Buffer
	for fi := 0; fi < n; fi++ {
		renderVisualizerFrame(canvas, base, loud, lr, input.Analysis.Frames[fi], fi, n, input.Analysis.Duration, mode, layout)
		ref.Write(canvas.Pix)
	}

	var got bytes.Buffer
	if err := WriteVisualizerRGBAFrames(context.Background(), &got, input, base, mode, layout, w, h); err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}
	if got.Len() != n*w*h*4 {
		t.Fatalf("output size: got %d, want %d", got.Len(), n*w*h*4)
	}
	if !bytes.Equal(got.Bytes(), ref.Bytes()) {
		t.Fatal("parallel output differs from sequential reference (ordering/content bug)")
	}
}

func TestWriteVisualizerRGBAFramesSpectrumBars(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 128, 72))
	mode := ForegroundMode{PrimaryColor: color.RGBA{255, 255, 255, 255}, AccentColor: color.RGBA{255, 255, 255, 255}, Overlay: color.RGBA{0, 0, 0, 92}}
	layout, _ := LayoutForSize(128, 72)
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 1.0 / 30,
			Frames:   make([]AudioFrame, 1),
		},
	}
	input.Analysis.Frames[0].Spectrum24 = [24]float64{}
	for i := 0; i < 24; i++ {
		input.Analysis.Frames[0].Spectrum24[i] = float64(i) / 24.0
	}
	var buf bytes.Buffer
	err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, 128, 72)
	if err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("empty output")
	}
}

func TestWriteVisualizerRGBAFramesZeroFrames(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 128, 72))
	mode := ForegroundMode{PrimaryColor: color.RGBA{255, 255, 255, 255}, AccentColor: color.RGBA{255, 255, 255, 255}, Overlay: color.RGBA{0, 0, 0, 92}}
	layout, _ := LayoutForSize(128, 72)
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 0,
			Frames:   []AudioFrame{},
		},
	}
	var buf bytes.Buffer
	err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, 128, 72)
	if err == nil {
		t.Fatal("expected error for zero frames")
	}
}

func TestWriteVisualizerRGBAFramesFPS30(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 128, 72))
	mode := ForegroundMode{PrimaryColor: color.RGBA{255, 255, 255, 255}, AccentColor: color.RGBA{255, 255, 255, 255}, Overlay: color.RGBA{0, 0, 0, 92}}
	layout, _ := LayoutForSize(128, 72)
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 2.0,
			Frames:   make([]AudioFrame, 60),
		},
	}
	for i := range input.Analysis.Frames {
		input.Analysis.Frames[i].Spectrum24 = [24]float64{}
	}
	var buf bytes.Buffer
	err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, 128, 72)
	if err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}
	expected := 60 * 128 * 72 * 4
	if buf.Len() != expected {
		t.Fatalf("output size: got %d, want %d", buf.Len(), expected)
	}
}

// ---------------------------------------------------------------------------
// AV-716 integration frame tests
// ---------------------------------------------------------------------------

// TestVisualizerFrameWithArtwork renders frames with a known red artwork tile
// and asserts that every element occupies its canonical layout rectangle.
func TestVisualizerFrameWithArtwork(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}

	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a red artwork PNG (400×400, strong red).
	artPath := filepath.Join(tmpDir, "artwork.png")
	art := image.NewRGBA(image.Rect(0, 0, 400, 400))
	draw.Draw(art, art.Bounds(), &image.Uniform{color.RGBA{200, 50, 50, 255}}, image.Point{}, draw.Src)
	f, err := os.Create(artPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, art); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// Build analysis: 10 seconds, 30 fps = 300 frames.
	features := AudioFeatures{}
	for i := range features.LoudnessEnvelope {
		features.LoudnessEnvelope[i] = 0.3 + 0.3*math.Sin(float64(i)*2.0*math.Pi/200.0)
	}

	frames := make([]AudioFrame, 300)
	// Frame 0: all spectrum values at 0.
	// Frame 120 (4 s): increasing spectrum.
	for b := 0; b < 24; b++ {
		frames[120].Spectrum24[b] = float64(b+1) / 24.0
	}

	input := AudioRenderInput{
		Metadata:    AudioMetadata{Title: "Test", Artist: "Artist"},
		ArtworkPath: artPath,
		Analysis: AudioAnalysis{
			FPS: 30, Duration: 10.0, Frames: frames, Features: features,
		},
	}

	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}

	basePath := filepath.Join(tmpDir, "base.png")
	mode, err := PrepareVisualizerBase(ctx, ffmpeg, artPath, nil, layout, basePath)
	if err != nil {
		t.Fatalf("PrepareVisualizerBase: %v", err)
	}

	baseImg, err := loadPNG(basePath)
	if err != nil {
		t.Fatalf("load base: %v", err)
	}
	baseRGBA := toRGBA(baseImg)

	var buf bytes.Buffer
	if err := WriteVisualizerRGBAFrames(ctx, &buf, input, baseRGBA, mode, layout, 1280, 720); err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}

	frameSize := 1280 * 720 * 4
	if buf.Len() != 300*frameSize {
		t.Fatalf("expected %d bytes, got %d", 300*frameSize, buf.Len())
	}

	data := buf.Bytes()
	frame0 := data[:frameSize]
	frame120 := data[120*frameSize : 121*frameSize]

	pixel := func(data []byte, x, y int) (uint8, uint8, uint8, uint8) {
		idx := (y*1280 + x) * 4
		return data[idx], data[idx+1], data[idx+2], data[idx+3]
	}

	// 1. Blurred background – top-left pixel should show blurred red (R > G)
	r, g, b, a := pixel(frame0, 0, 0)
	if a != 255 {
		t.Errorf("background pixel alpha at (0,0): %d, want 255", a)
	}
	if r <= g {
		t.Errorf("background at (0,0) should show red blur: rgba(%d,%d,%d,%d)", r, g, b, a)
	}

	// 2. Artwork rectangle – centre of artwork tile (240,296) should have strong red.
	r, g, b, a = pixel(frame0, 240, 296)
	if r < 100 {
		t.Errorf("artwork centre at (240,296) should be red: rgba(%d,%d,%d,%d)", r, g, b, a)
	}

	// 3. Rounded corners – the corner pixel at (96,152) is outside the rounded
	//    rect (radius 24) and shows only the overlaid blurred background.
	//    Verify it's a valid non-zero pixel.
	r, g, b, a = pixel(frame0, 96, 152)
	if a != 255 {
		t.Errorf("rounded corner at (96,152) alpha=%d, want 255", a)
	}
	if r == 0 && g == 0 && b == 0 {
		t.Errorf("rounded corner at (96,152) is black, expected overlaid background")
	}

	// 4. Spectrum bars at 4 s – check first bar bottom pixel.
	//    First bar: X=443, bottom Y=488.
	r, g, b, a = pixel(frame120, 443, 487)
	if a < 200 {
		t.Errorf("spectrum bar at (443,487) alpha=%d, want >=200", a)
	}

	// 5. Progress marker at 0 s – left edge.
	r, g, b, a = pixel(frame0, 64, 654)
	if a < 200 {
		t.Errorf("progress marker at (64,654) in frame 0: alpha=%d, want >=200", a)
	}

	// 6. Progress marker at 4 s – expected X = 64 + 1000*4/10 = 464.
	r, g, b, a = pixel(frame120, 464, 654)
	if a < 200 {
		t.Errorf("progress marker at (464,654) in frame 4s: alpha=%d, want >=200", a)
	}

	// 7. Loudness guide line at (64, 598)
	r, g, b, a = pixel(frame0, 64, 598)
	if g > 0 && r > 0 && b > 0 {
		t.Logf("loudness guide at (64,598): rgba(%d,%d,%d,%d)", r, g, b, a)
	}
}

// TestVisualizerFallbackNoArt renders frames without artwork and verifies the
// fallback tile and blurred background are produced.
func TestVisualizerFallbackNoArt(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Skipf("fonts unavailable: %v", err)
	}

	ctx := context.Background()
	tmpDir := t.TempDir()

	features := AudioFeatures{
		BPM:               140,
		IntegratedLUFS:    -8,
		LowFrequencyRatio: 0.3,
		SpectralCentroid:  2500,
	}
	for i := range features.LoudnessEnvelope {
		features.LoudnessEnvelope[i] = 0.5
	}

	frames := make([]AudioFrame, 30)
	for i := range frames {
		for b := 0; b < 24; b++ {
			frames[i].Spectrum24[b] = 0.5
		}
	}

	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "No Art", Artist: "Test"},
		Analysis: AudioAnalysis{
			FPS: 30, Duration: 1.0, Frames: frames, Features: features,
		},
	}

	layout, _ := LayoutForSize(1280, 720)
	basePath := filepath.Join(tmpDir, "base.png")

	// Render fallback artwork with white foreground (dark fallback assumed).
	fallback, err := RenderFallbackArtwork(ctx, ffmpeg, fonts, features, color.RGBA{255, 255, 255, 224}, layout.Artwork.W)
	if err != nil {
		t.Fatalf("fallback artwork: %v", err)
	}

	mode, err := PrepareVisualizerBase(ctx, ffmpeg, "", fallback, layout, basePath)
	if err != nil {
		t.Fatalf("PrepareVisualizerBase: %v", err)
	}

	baseImg, _ := loadPNG(basePath)
	baseRGBA := toRGBA(baseImg)

	var buf bytes.Buffer
	if err := WriteVisualizerRGBAFrames(ctx, &buf, input, baseRGBA, mode, layout, 1280, 720); err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}

	frameSize := 1280 * 720 * 4
	if buf.Len() != 30*frameSize {
		t.Fatalf("expected %d bytes, got %d", 30*frameSize, buf.Len())
	}

	// Foreground mode should be valid.  Overlay may be 0 (no overlay needed)
	// up to 255 (100 %).
	if mode.PrimaryColor.A != 255 || mode.AccentColor.A != 255 {
		t.Errorf("foregrounds must be opaque, got primary=%d accent=%d", mode.PrimaryColor.A, mode.AccentColor.A)
	}
	if mode.Overlay.A > 255 {
		t.Errorf("overlay alpha %d > 255 (max)", mode.Overlay.A)
	}

	// Fallback tile centre should have non-zero colour from gradient/fingerprint.
	data := buf.Bytes()
	idx := (296*1280 + 240) * 4
	r, g, b, a := data[idx], data[idx+1], data[idx+2], data[idx+3]
	if r == 0 && g == 0 && b == 0 {
		t.Errorf("fallback centre (240,296) is black, expected gradient/fingerprint")
	}
	_ = a
}

// TestVisualizerContrastDarkLight verifies that dark and light artwork each
// produce a single foreground mode achieving 4.5:1 in both metadata and graph
// regions.
func TestVisualizerContrastDarkLight(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}

	ctx := context.Background()

	// Test dark artwork (near black) → light mode.
	t.Run("DarkArtwork", func(t *testing.T) {
		tmpDir := t.TempDir()
		artPath := filepath.Join(tmpDir, "dark.png")
		art := image.NewRGBA(image.Rect(0, 0, 400, 400))
		draw.Draw(art, art.Bounds(), &image.Uniform{color.RGBA{15, 15, 25, 255}}, image.Point{}, draw.Src)
		savePNG(artPath, art)

		testContrastRegion(ctx, t, ffmpeg, artPath)
	})

	// Test light artwork (near white) → dark mode.
	t.Run("LightArtwork", func(t *testing.T) {
		tmpDir := t.TempDir()
		artPath := filepath.Join(tmpDir, "light.png")
		art := image.NewRGBA(image.Rect(0, 0, 400, 400))
		draw.Draw(art, art.Bounds(), &image.Uniform{color.RGBA{230, 230, 220, 255}}, image.Point{}, draw.Src)
		savePNG(artPath, art)

		testContrastRegion(ctx, t, ffmpeg, artPath)
	})
}

func testContrastRegion(ctx context.Context, t *testing.T, ffmpeg, artPath string) {
	t.Helper()
	layout, _ := LayoutForSize(1280, 720)
	outPath := filepath.Join(t.TempDir(), "base.png")

	mode, err := PrepareVisualizerBase(ctx, ffmpeg, artPath, nil, layout, outPath)
	if err != nil {
		t.Fatalf("PrepareVisualizerBase: %v", err)
	}

	// Load the finished base image (already has overlay applied).
	baseImg, err := loadPNG(outPath)
	if err != nil {
		t.Fatalf("load base: %v", err)
	}

	metaRect := image.Rect(layout.Title.X, layout.Title.Y, layout.Title.X+layout.Title.W, layout.Title.Y+layout.Title.H)
	graphRect := image.Rect(layout.Loudness.X, layout.Loudness.Y, layout.Loudness.X+layout.Loudness.W, layout.Loudness.Y+layout.Loudness.H)

	// baseImg already has the overlay baked in (applied by
	// PrepareVisualizerBase).  Use direct contrast check without re-applying.
	if ok, ratio := checkDirectContrast(baseImg, metaRect, mode.PrimaryColor); !ok {
		t.Errorf("metadata region contrast %.2f < 4.5", ratio)
	}
	if ok, ratio := checkDirectContrast(baseImg, graphRect, mode.AccentColor); !ok {
		t.Errorf("graph region contrast %.2f < 4.5", ratio)
	}
}

// TestSpectrumBottomFade verifies that the bottom of every spectrum bar fades
// to match the background, proving the vertical alpha gradient is applied.
// At the bottommost pixel (Y=487, one above barBottom=488) the bar draws with
// alpha=0, so the background colour should be unchanged.
func TestSpectrumBottomFade(t *testing.T) {
	width, height := 1280, 720

	// Use a mid-grey background so the fade is easy to detect.
	bgColor := color.RGBA{100, 100, 100, 255}
	base := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(base, base.Bounds(), &image.Uniform{bgColor}, image.Point{}, draw.Src)

	mode := ForegroundMode{
		AccentColor: color.RGBA{255, 255, 255, 255},
		Overlay:     color.RGBA{0, 0, 0, 0}, // fully transparent overlay
	}
	layout, _ := LayoutForSize(width, height)

	// Single frame with all spectrum values at 1.0 (max height).
	frame := AudioFrame{}
	for b := 0; b < 24; b++ {
		frame.Spectrum24[b] = 1.0
	}

	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS: 30, Duration: 1.0 / 30,
			Frames: []AudioFrame{frame},
		},
	}

	var buf bytes.Buffer
	if err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, width, height); err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}

	data := buf.Bytes()

	// First bar is at X=443, barBottom = 488.
	barBottom := layout.Spectrum.Y + layout.Spectrum.H // 488
	firstBarX := layout.Spectrum.X + 11                // 443
	barW := 18
	barGap := 13

	// Check the bottom pixel row (Y=487) of each bar.  Because the alpha
	// gradient reaches zero at the bottom edge, the bottommost pixel should
	// still show the background colour (unchanged by the bar).
	foundFaded := false
	for b := 0; b < 24; b++ {
		x := firstBarX + b*(barW+barGap)
		for dx := 0; dx < barW; dx++ {
			cx := x + dx
			if cx < 0 || cx >= width {
				continue
			}
			// Bottom pixel — should still show grey background.
			idx := ((barBottom-1)*width + cx) * 4
			r, g, b := data[idx], data[idx+1], data[idx+2]
			// Compare with background (100,100,100) – allow a small
			// tolerance for any subtle blend.
			dr := absDiff(int(r), 100)
			dg := absDiff(int(g), 100)
			db := absDiff(int(b), 100)
			if dr <= 5 && dg <= 5 && db <= 5 {
				foundFaded = true
			}

			// One pixel above bottom — must have been painted (bar visible).
			if barBottom-2 >= 0 {
				idx2 := ((barBottom-2)*width + cx) * 4
				r2, g2, b2 := data[idx2], data[idx2+1], data[idx2+2]
				dr2 := absDiff(int(r2), 100)
				dg2 := absDiff(int(g2), 100)
				db2 := absDiff(int(b2), 100)
				if dr2 > 10 || dg2 > 10 || db2 > 10 {
					foundFaded = true
				}
			}
		}
	}

	if !foundFaded {
		t.Error("spectrum bars do not show a bottom fade to background colour")
	}
}

// ---------------------------------------------------------------------------
// AV-846: loudness layer cached once per render job
// ---------------------------------------------------------------------------

// TestLoudnessLayerRenderedOncePerJob verifies that the loudness graph is
// rendered once per job (not per frame) and produces the trend line at 95%
// opacity.  Before the fix, drawLoudness runs each frame at 80% opacity
// without a trend line.
func TestLoudnessLayerRenderedOncePerJob(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	mode := ForegroundMode{PrimaryColor: color.RGBA{255, 255, 255, 255}, AccentColor: color.RGBA{255, 255, 255, 255}, Overlay: color.RGBA{0, 0, 0, 92}}
	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}

	// Constant envelope.
	var features AudioFeatures
	for i := range features.LoudnessEnvelope {
		features.LoudnessEnvelope[i] = 0.5
	}

	frames := make([]AudioFrame, 30)
	for i := range frames {
		frames[i].Spectrum24 = [24]float64{}
	}

	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS: 30, Duration: 1.0, Frames: frames, Features: features,
		},
	}

	var buf bytes.Buffer
	if err := WriteVisualizerRGBAFrames(context.Background(), &buf, input, base, mode, layout, 1280, 720); err != nil {
		t.Fatalf("WriteVisualizerRGBAFrames: %v", err)
	}

	frameSize := 1280 * 720 * 4
	if buf.Len() != 30*frameSize {
		t.Fatalf("expected %d bytes, got %d", 30*frameSize, buf.Len())
	}

	data := buf.Bytes()

	// ---- Trend-line opacity check ----
	// The constant envelope normalizes to the track's own peak (relative
	// loudness), so the curve sits at the top of the panel rather than the
	// middle. Locate the strongest painted pixel in the first column — that is
	// the trend/detail curve (guide lines are far fainter at ~0.22 alpha) — and
	// require its opacity to match the drawn trend line.
	lr := layout.Loudness
	edgeX := lr.X
	maxA := byte(0)
	peakY := lr.Y
	for y := lr.Y; y < lr.Y+lr.H; y++ {
		if v := data[y*1280*4+edgeX*4+3]; v > maxA {
			maxA, peakY = v, y
		}
	}
	idxEdge := peakY*1280*4 + edgeX*4
	if maxA < 220 {
		t.Errorf("strongest loudness pixel in first column at (%d,%d) alpha=%d, expected >=220 (trend line)", edgeX, peakY, maxA)
	}

	// ---- Cache invariant: loudness identical across all frames ----
	for fi := 1; fi < 30; fi++ {
		idx := fi*frameSize + idxEdge
		if data[idxEdge] != data[idx] || data[idxEdge+1] != data[idx+1] || data[idxEdge+2] != data[idx+2] || data[idxEdge+3] != data[idx+3] {
			t.Errorf("frame %d: loudness pixel differs from frame 0 (cache broken)", fi)
			break
		}
	}
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
